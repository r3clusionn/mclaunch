// Package launch prepares an instance (version, loader, Java, libraries,
// assets, natives) and starts the game in its own session.
package launch

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"mclaunch/internal/auth"
	"mclaunch/internal/dl"
	"mclaunch/internal/instance"
	"mclaunch/internal/loaders"
	"mclaunch/internal/mojang"
	"mclaunch/internal/paths"
)

// Reporter receives progress for the interface or the terminal.
type Reporter interface {
	Step(msg string)
	Bytes(done, total int64, files, totalFiles int)
}

type nopReporter struct{}

func (nopReporter) Step(string)                  {}
func (nopReporter) Bytes(int64, int64, int, int) {}

// Options for one launch.
type Options struct {
	Account   *auth.Account
	QuickPlay string // a world to load straight into (singleplayer), optional
	GCLog     string // write JVM GC logs here (measurements), optional
}

// Prepare installs everything the instance needs and returns the merged version.
func Prepare(ctx context.Context, inst *instance.Instance, r Reporter) (*mojang.Version, string, error) {
	if r == nil {
		r = nopReporter{}
	}
	r.Step("Reading Minecraft " + inst.Game)
	vanilla, err := mojang.LoadVersion(ctx, inst.Game)
	if err != nil {
		return nil, "", err
	}
	r.Step("Java " + vanilla.JavaComponent())
	java := inst.Java
	if java == "" {
		if java, err = mojang.EnsureJava(ctx, vanilla.JavaComponent(), r.Bytes); err != nil {
			return nil, "", fmt.Errorf("Java: %w", err)
		}
	}
	if inst.VersionID == "" || !exists(paths.VersionJSON(inst.VersionID)) {
		if inst.Loader != loaders.Vanilla && inst.Loader != "" {
			r.Step("Installing " + inst.Loader + " " + inst.LoaderVersion)
			if inst.LoaderVersion == "" {
				vs, err := loaders.Versions(ctx, inst.Loader, inst.Game)
				if err != nil {
					return nil, "", err
				}
				inst.LoaderVersion = loaders.Recommended(vs)
			}
		}
		id, err := loaders.Install(ctx, inst.Loader, inst.Game, inst.LoaderVersion, java, r.Step)
		if err != nil {
			return nil, "", err
		}
		inst.VersionID = id
		inst.Save()
	}
	v, err := mojang.LoadVersion(ctx, inst.VersionID)
	if err != nil {
		return nil, "", err
	}
	if v.JavaComponent() != vanilla.JavaComponent() && inst.Java == "" {
		if java, err = mojang.EnsureJava(ctx, v.JavaComponent(), r.Bytes); err != nil {
			return nil, "", err
		}
	}
	r.Step("Libraries and game files")
	tasks := []dl.Task{v.ClientTask()}
	for _, f := range v.Files(features(inst, nil)) {
		if f.Task.URL != "" {
			tasks = append(tasks, f.Task)
		}
	}
	if err := dl.All(ctx, tasks, r.Bytes); err != nil {
		return nil, "", err
	}
	r.Step("Assets")
	assets, _, err := v.AssetTasks(ctx)
	if err != nil {
		return nil, "", err
	}
	if err := dl.All(ctx, assets, r.Bytes); err != nil {
		return nil, "", err
	}
	return v, java, nil
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func features(inst *instance.Instance, o *Options) mojang.Features {
	f := mojang.Features{"has_custom_resolution": inst.Width > 0 && inst.Height > 0}
	if o != nil && o.QuickPlay != "" {
		f["is_quick_play_singleplayer"] = true
	}
	return f
}

// extractNatives unpacks the native libraries of versions that ship them
// as archives (before 1.19) into a fresh directory.
func extractNatives(v *mojang.Version, inst *instance.Instance, dir string) error {
	os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, f := range v.Files(features(inst, nil)) {
		if !f.Native {
			continue
		}
		z, err := zip.OpenReader(f.Task.Path)
		if err != nil {
			return err
		}
		for _, zf := range z.File {
			skip := zf.FileInfo().IsDir() || strings.HasPrefix(zf.Name, "META-INF/")
			for _, ex := range f.Exclude {
				skip = skip || strings.HasPrefix(zf.Name, ex)
			}
			if skip || strings.Contains(zf.Name, "..") {
				continue
			}
			dst := filepath.Join(dir, filepath.FromSlash(zf.Name))
			os.MkdirAll(filepath.Dir(dst), 0o755)
			r, err := zf.Open()
			if err != nil {
				z.Close()
				return err
			}
			w, err := os.Create(dst)
			if err == nil {
				_, err = io.Copy(w, r)
				w.Close()
			}
			r.Close()
			if err != nil {
				z.Close()
				return err
			}
		}
		z.Close()
	}
	return nil
}

// versionJar is the game jar under the launched version's own name, as the
// official launcher has it: Forge and NeoForge exclude "<version>.jar" by name.
func versionJar(v *mojang.Version) (string, error) {
	src := paths.VersionJar(v.JarID)
	if v.ID == v.JarID {
		return src, nil
	}
	dst := paths.VersionJar(v.ID)
	if exists(dst) {
		return dst, nil
	}
	os.MkdirAll(filepath.Dir(dst), 0o755)
	if err := os.Link(src, dst); err != nil {
		b, rerr := os.ReadFile(src)
		if rerr != nil {
			return "", rerr
		}
		if err := os.WriteFile(dst, b, 0o644); err != nil {
			return "", err
		}
	}
	return dst, nil
}

func gcFlags(inst *instance.Instance, javaMajor int) []string {
	if inst.GC == "zgc" {
		f := []string{"-XX:+UseZGC"}
		if javaMajor >= 21 && javaMajor < 23 {
			f = append(f, "-XX:+ZGenerational") // the default from Java 23
		}
		return append(f, "-XX:+AlwaysPreTouch", "-XX:+UseTransparentHugePages")
	}
	return []string{"-XX:+UseG1GC", "-XX:MaxGCPauseMillis=37", "-XX:+ParallelRefProcEnabled",
		"-XX:+AlwaysPreTouch", "-XX:+UseTransparentHugePages"}
}

// Command builds the game's command line and environment.
func Command(inst *instance.Instance, v *mojang.Version, java string, o *Options) (*exec.Cmd, error) {
	acc := o.Account
	if acc == nil {
		return nil, errors.New("no account")
	}
	natives := filepath.Join(inst.Dir(), "natives")
	if err := extractNatives(v, inst, natives); err != nil {
		return nil, fmt.Errorf("natives: %w", err)
	}
	f := features(inst, o)
	var cp []string
	seen := map[string]bool{}
	for _, lf := range v.Files(f) {
		if lf.Native || seen[lf.Task.Path] {
			continue
		}
		seen[lf.Task.Path] = true
		cp = append(cp, lf.Task.Path)
	}
	jar, err := versionJar(v)
	if err != nil {
		return nil, err
	}
	cp = append(cp, jar)

	_, idx, err := v.AssetTasks(context.Background())
	if err != nil {
		return nil, err
	}
	assetsDir, err := v.VirtualAssets(idx, inst.GameDir())
	if err != nil {
		return nil, err
	}
	userType, token, xuid := "msa", acc.Token, acc.XUID
	if acc.Type == "offline" {
		userType, token, xuid = "legacy", "0", "0"
	}
	vals := map[string]string{
		"auth_player_name": acc.Name, "version_name": v.ID, "game_directory": inst.GameDir(),
		"assets_root": paths.Assets(), "game_assets": assetsDir, "assets_index_name": v.AssetIndex.ID,
		"auth_uuid": acc.ID, "auth_access_token": token, "auth_session": token, "clientid": acc.ClientID,
		"auth_xuid": xuid, "user_type": userType, "user_properties": "{}", "version_type": "mclaunch",
		"natives_directory": natives, "launcher_name": "mclaunch", "launcher_version": "1.0",
		"classpath": strings.Join(cp, ":"), "classpath_separator": ":", "library_directory": paths.Libraries(),
		"resolution_width": fmt.Sprint(inst.Width), "resolution_height": fmt.Sprint(inst.Height),
		"quickPlaySingleplayer": o.QuickPlay, "quickPlayPath": filepath.Join(inst.Dir(), "quickplay.json"),
	}
	jvm, game := v.Expand(f, vals)

	args := []string{fmt.Sprintf("-Xms%dm", inst.MemoryMB), fmt.Sprintf("-Xmx%dm", inst.MemoryMB)}
	args = append(args, gcFlags(inst, v.JavaVersion.MajorVersion)...)
	args = append(args, "-Dlog4j2.formatMsgNoLookups=true", "-Dminecraft.launcher.brand=mclaunch",
		"-Dminecraft.launcher.version=1.0",
		// LWJGL unpacks its natives here instead of /tmp (RAM on this laptop).
		"-Dorg.lwjgl.system.SharedLibraryExtractPath="+filepath.Join(inst.Dir(), "natives"))
	if o.GCLog != "" {
		args = append(args, "-Xlog:gc*:file="+o.GCLog+":time,uptime,level,tags")
	}
	args = append(args, strings.Fields(inst.JVMArgs)...)
	args = append(args, jvm...)
	args = append(args, v.MainClass)
	args = append(args, game...)

	cmd := exec.Command(java, args...)
	cmd.Dir = inst.GameDir()
	env := os.Environ()
	// Mesa (radeonsi): threaded GL is a big win for Minecraft; a shader cache
	// big enough for modpacks.
	env = append(env, "mesa_glthread=true", "MESA_SHADER_CACHE_MAX_SIZE=4G")
	for k, val := range inst.Env {
		env = append(env, k+"="+val)
	}
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd, nil
}

// ---- running -------------------------------------------------------------------

// LogPath is where the game's output of the last launch goes.
func LogPath(inst *instance.Instance) string {
	return filepath.Join(inst.Dir(), "logs", "launch.log")
}

func ensureAudio() {
	sock := filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "pipewire-0")
	if exists(sock) || !exists("/usr/local/bin/audio") {
		return
	}
	exec.Command("/usr/local/bin/audio", "ensure").Run()
	exec.Command("pkill", "-USR1", "-x", "dwmstat").Run()
}

// PowerProfile asks for the game power mode (or back to automatic) through
// the doas rule from powerprofile-game.sh; without the rule nothing happens.
func PowerProfile(mode string) error {
	if !exists("/usr/local/sbin/powerprofile") {
		return nil
	}
	return exec.Command("doas", "-n", "/usr/local/sbin/powerprofile", mode).Run()
}

// Start applies the frame cap for the power source, starts the game with its
// output in launch.log, and switches to the game power mode.
func Start(inst *instance.Instance, v *mojang.Version, java string, o *Options) (*exec.Cmd, error) {
	fps := inst.FPSOnBattery
	if instance.OnAC() {
		fps = inst.FPSOnAC
	}
	inst.SetMaxFPS(fps)
	cmd, err := Command(inst, v, java, o)
	if err != nil {
		return nil, err
	}
	logp := LogPath(inst)
	os.MkdirAll(filepath.Dir(logp), 0o755)
	logf, err := os.Create(logp)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(logf, "mclaunch: %s (%s, %s), %d MB heap, %s, maxFps %d, java %s\n", inst.Name, v.ID,
		v.JavaComponent(), inst.MemoryMB, inst.GC, fps, java)
	cmd.Stdout, cmd.Stderr = logf, logf
	ensureAudio()
	if err := cmd.Start(); err != nil {
		logf.Close()
		return nil, err
	}
	logf.Close() // the game has its own copy
	PowerProfile("game")
	inst.LastPlayed = time.Now()
	inst.Save()
	return cmd, nil
}

// Result of a finished game.
type Result struct {
	ExitCode int
	Seconds  int64
	Crash    string // newest crash report written during the session, if any
}

// Finish records the play time, restores the power profile and finds a
// crash report. Called after the game exits (also from the supervisor).
func Finish(inst *instance.Instance, started time.Time, exitCode int) Result {
	PowerProfile("auto")
	res := Result{ExitCode: exitCode, Seconds: int64(time.Since(started).Seconds())}
	if cur, err := instance.Load(inst.ID); err == nil {
		inst = cur
	}
	inst.PlaySeconds += res.Seconds
	inst.Save()
	reports, _ := filepath.Glob(filepath.Join(inst.GameDir(), "crash-reports", "*.txt"))
	for _, r := range reports {
		if st, err := os.Stat(r); err == nil && st.ModTime().After(started) {
			res.Crash = r
		}
	}
	return res
}
