// Package instance: a game directory with its version, loader and settings.
package instance

import (
	"archive/zip"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"mclaunch/internal/paths"
)

type Instance struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Game          string            `json:"game"`          // Minecraft version
	Loader        string            `json:"loader"`        // vanilla, fabric, quilt, neoforge, forge
	LoaderVersion string            `json:"loaderVersion"` // empty for vanilla
	VersionID     string            `json:"versionId"`     // the installed version to launch
	MemoryMB      int               `json:"memoryMB"`      // heap: -Xms = -Xmx
	GC            string            `json:"gc"`            // "g1" or "zgc"
	JVMArgs       string            `json:"jvmArgs"`
	Java          string            `json:"java"` // custom java binary; empty: Mojang's
	Width         int               `json:"width"`
	Height        int               `json:"height"`
	Env           map[string]string `json:"env"`
	FPSOnAC       int               `json:"fpsOnAC"`      // 0: leave options.txt alone
	FPSOnBattery  int               `json:"fpsOnBattery"` // 0: leave options.txt alone
	Icon          string            `json:"icon"`         // URL (Modrinth pack) or ""
	Created       time.Time         `json:"created"`
	LastPlayed    time.Time         `json:"lastPlayed"`
	PlaySeconds   int64             `json:"playSeconds"`
	ImportedFrom  string            `json:"importedFrom,omitempty"`
}

func (i *Instance) Dir() string     { return filepath.Join(paths.Instances(), i.ID) }
func (i *Instance) GameDir() string { return filepath.Join(i.Dir(), "minecraft") }
func (i *Instance) ModsDir() string { return filepath.Join(i.GameDir(), "mods") }

// Defaults for a new instance on this laptop (see How it works in the README).
func New(name, game, loader, loaderVersion string) *Instance {
	mem := 4096
	if loader == "neoforge" || loader == "forge" {
		mem = 6144
	}
	return &Instance{
		ID: slug(name), Name: name, Game: game, Loader: loader, LoaderVersion: loaderVersion,
		MemoryMB: mem, GC: "zgc", Width: 1708, Height: 960, FPSOnAC: 120, FPSOnBattery: 60,
		Env: map[string]string{}, Created: time.Now(),
	}
}

func slug(name string) string {
	s := strings.ToLower(regexp.MustCompile(`[^A-Za-z0-9]+`).ReplaceAllString(name, "-"))
	s = strings.Trim(s, "-")
	if s == "" {
		s = "instance"
	}
	base := s
	for n := 2; ; n++ {
		if _, err := os.Stat(filepath.Join(paths.Instances(), s)); os.IsNotExist(err) {
			return s
		}
		s = fmt.Sprintf("%s-%d", base, n)
	}
}

func (i *Instance) Save() error {
	if err := os.MkdirAll(i.GameDir(), 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(i, "", "  ")
	tmp := filepath.Join(i.Dir(), ".instance.json.tmp")
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(i.Dir(), "instance.json"))
}

func Load(id string) (*Instance, error) {
	b, err := os.ReadFile(filepath.Join(paths.Instances(), id, "instance.json"))
	if err != nil {
		return nil, err
	}
	var i Instance
	if err := json.Unmarshal(b, &i); err != nil {
		return nil, err
	}
	i.ID = id
	if i.Env == nil {
		i.Env = map[string]string{}
	}
	return &i, nil
}

// List returns the instances, most recently played first.
func List() []*Instance {
	entries, _ := os.ReadDir(paths.Instances())
	var out []*Instance
	for _, e := range entries {
		if i, err := Load(e.Name()); err == nil {
			out = append(out, i)
		}
	}
	sort.Slice(out, func(a, b int) bool {
		if !out[a].LastPlayed.Equal(out[b].LastPlayed) {
			return out[a].LastPlayed.After(out[b].LastPlayed)
		}
		return out[a].Created.After(out[b].Created)
	})
	return out
}

func (i *Instance) Delete() error {
	if i.ID == "" || strings.Contains(i.ID, "/") {
		return errors.New("bad instance id")
	}
	return os.RemoveAll(i.Dir())
}

// ---- options.txt -----------------------------------------------------------

// SetMaxFPS writes maxFps into options.txt (creating it if the game hasn't yet).
func (i *Instance) SetMaxFPS(fps int) error {
	if fps <= 0 {
		return nil
	}
	p := filepath.Join(i.GameDir(), "options.txt")
	b, _ := os.ReadFile(p)
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		lines = nil
	}
	set := false
	for k, l := range lines {
		if strings.HasPrefix(l, "maxFps:") {
			lines[k] = fmt.Sprintf("maxFps:%d", fps)
			set = true
		}
	}
	if !set {
		lines = append(lines, fmt.Sprintf("maxFps:%d", fps))
	}
	os.MkdirAll(i.GameDir(), 0o755)
	return os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
}

// OnAC reports whether the laptop is plugged in.
func OnAC() bool {
	matches, _ := filepath.Glob("/sys/class/power_supply/A*/online")
	for _, m := range matches {
		if b, err := os.ReadFile(m); err == nil && strings.TrimSpace(string(b)) == "1" {
			return true
		}
	}
	return false
}

// ---- mods ------------------------------------------------------------------

type ModFile struct {
	File     string `json:"file"` // name in mods/, with .disabled when off
	Enabled  bool   `json:"enabled"`
	Size     int64  `json:"size"`
	SHA1     string `json:"sha1,omitempty"`
	Project  string `json:"project,omitempty"` // Modrinth project ID
	Version  string `json:"version,omitempty"` // Modrinth version ID
	Title    string `json:"title,omitempty"`
	Icon     string `json:"icon,omitempty"`
	VersionN string `json:"versionNumber,omitempty"`
}

// Mods lists mods/ (*.jar and *.jar.disabled).
func (i *Instance) Mods() []ModFile {
	entries, _ := os.ReadDir(i.ModsDir())
	var out []ModFile
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !(strings.HasSuffix(n, ".jar") || strings.HasSuffix(n, ".jar.disabled")) {
			continue
		}
		info, _ := e.Info()
		var size int64
		if info != nil {
			size = info.Size()
		}
		out = append(out, ModFile{File: n, Enabled: strings.HasSuffix(n, ".jar"), Size: size})
	}
	sort.Slice(out, func(a, b int) bool { return strings.ToLower(out[a].File) < strings.ToLower(out[b].File) })
	return out
}

// ToggleMod renames a jar to .jar.disabled and back; returns the new name.
func (i *Instance) ToggleMod(file string) (string, error) {
	if strings.ContainsAny(file, "/\\") {
		return "", errors.New("bad file name")
	}
	from := filepath.Join(i.ModsDir(), file)
	to := strings.TrimSuffix(file, ".disabled")
	if to == file {
		to = file + ".disabled"
	}
	return to, os.Rename(from, filepath.Join(i.ModsDir(), to))
}

func (i *Instance) RemoveMod(file string) error {
	if strings.ContainsAny(file, "/\\") {
		return errors.New("bad file name")
	}
	return os.Remove(filepath.Join(i.ModsDir(), file))
}

// ---- worlds ----------------------------------------------------------------

type World struct {
	Name     string    `json:"name"`
	Modified time.Time `json:"modified"`
	Size     int64     `json:"size"`
}

func (i *Instance) Worlds() []World {
	dir := filepath.Join(i.GameDir(), "saves")
	entries, _ := os.ReadDir(dir)
	var out []World
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		w := World{Name: e.Name()}
		filepath.Walk(filepath.Join(dir, e.Name()), func(p string, fi os.FileInfo, err error) error {
			if err == nil && !fi.IsDir() {
				w.Size += fi.Size()
				if fi.ModTime().After(w.Modified) {
					w.Modified = fi.ModTime()
				}
			}
			return nil
		})
		out = append(out, w)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Modified.After(out[b].Modified) })
	return out
}

// BackupWorld zips a world into the instance's backups/ and returns the path.
func (i *Instance) BackupWorld(name string) (string, error) {
	if strings.ContainsAny(name, "/\\") || name == ".." {
		return "", errors.New("bad world name")
	}
	src := filepath.Join(i.GameDir(), "saves", name)
	dst := filepath.Join(i.Dir(), "backups", fmt.Sprintf("%s-%s.zip", name, time.Now().Format("20060102-150405")))
	os.MkdirAll(filepath.Dir(dst), 0o755)
	f, err := os.Create(dst)
	if err != nil {
		return "", err
	}
	defer f.Close()
	z := zip.NewWriter(f)
	err = filepath.Walk(src, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(filepath.Dir(src), p)
		w, err := z.Create(filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		r, err := os.Open(p)
		if err != nil {
			return err
		}
		defer r.Close()
		_, err = io.Copy(w, r)
		return err
	})
	if cerr := z.Close(); err == nil {
		err = cerr
	}
	return dst, err
}

// RandomID for accounts and temporary names.
func RandomID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}
