// Package loaders installs mod loaders as versions that inherit from vanilla:
// Fabric and Quilt from their meta servers, NeoForge and Forge by running
// their official installers headless with Mojang's Java.
package loaders

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"mclaunch/internal/dl"
	"mclaunch/internal/mojang"
	"mclaunch/internal/paths"
)

const (
	Vanilla  = "vanilla"
	Fabric   = "fabric"
	Quilt    = "quilt"
	NeoForge = "neoforge"
	Forge    = "forge"
)

var All = []string{Vanilla, Fabric, Quilt, NeoForge, Forge}

type LoaderVersion struct {
	Version string `json:"version"`
	Stable  bool   `json:"stable"`
}

// Versions lists a loader's versions for a game version, newest first.
func Versions(ctx context.Context, loader, game string) ([]LoaderVersion, error) {
	switch loader {
	case Fabric, Quilt:
		base := "https://meta.fabricmc.net/v2"
		if loader == Quilt {
			base = "https://meta.quiltmc.org/v3"
		}
		var raw []struct {
			Loader struct {
				Version string `json:"version"`
				Stable  *bool  `json:"stable"`
			} `json:"loader"`
		}
		if err := dl.JSON(ctx, base+"/versions/loader/"+game, &raw); err != nil {
			return nil, err
		}
		var out []LoaderVersion
		for _, r := range raw {
			stable := r.Loader.Stable == nil || *r.Loader.Stable
			if loader == Quilt {
				stable = !strings.Contains(r.Loader.Version, "beta")
			}
			out = append(out, LoaderVersion{r.Loader.Version, stable})
		}
		if len(out) == 0 {
			return nil, fmt.Errorf("%s has no loader for %s", loader, game)
		}
		return out, nil
	case NeoForge:
		var raw struct {
			Versions []string `json:"versions"`
		}
		if err := dl.CachedJSON(ctx, "https://maven.neoforged.net/api/maven/versions/releases/net/neoforged/neoforge",
			filepath.Join(paths.Cache(), "neoforge-versions.json"), time.Hour, &raw); err != nil {
			return nil, err
		}
		prefix := neoForgePrefix(game)
		var out []LoaderVersion
		for i := len(raw.Versions) - 1; i >= 0; i-- {
			v := raw.Versions[i]
			if strings.HasPrefix(v, prefix) {
				out = append(out, LoaderVersion{v, !strings.Contains(v, "beta")})
			}
		}
		if len(out) == 0 {
			return nil, fmt.Errorf("NeoForge has no version for %s", game)
		}
		return out, nil
	case Forge:
		var promos struct {
			Promos map[string]string `json:"promos"`
		}
		if err := dl.CachedJSON(ctx, "https://files.minecraftforge.net/net/minecraftforge/forge/promotions_slim.json",
			filepath.Join(paths.Cache(), "forge-promotions.json"), time.Hour, &promos); err != nil {
			return nil, err
		}
		var out []LoaderVersion
		if v, ok := promos.Promos[game+"-recommended"]; ok {
			out = append(out, LoaderVersion{v, true})
		}
		if v, ok := promos.Promos[game+"-latest"]; ok && (len(out) == 0 || out[0].Version != v) {
			out = append(out, LoaderVersion{v, len(out) == 0})
		}
		if len(out) == 0 {
			return nil, fmt.Errorf("Forge has no version for %s", game)
		}
		return out, nil
	}
	return nil, fmt.Errorf("unknown loader %q", loader)
}

// NeoForge numbers follow the game: 1.21.1 -> 21.1.*, 1.21 -> 21.0.*,
// 26.1.2 -> 26.1.2.*, 26.3 -> 26.3.0.*.
func neoForgePrefix(game string) string {
	p := strings.Split(game, ".")
	if p[0] == "1" && len(p) >= 2 {
		minor := "0"
		if len(p) >= 3 {
			minor = p[2]
		}
		return p[1] + "." + minor + "."
	}
	if len(p) == 2 {
		p = append(p, "0")
	}
	return strings.Join(p, ".") + "."
}

// Recommended picks the first stable version (else the newest).
func Recommended(vs []LoaderVersion) string {
	for _, v := range vs {
		if v.Stable {
			return v.Version
		}
	}
	return vs[0].Version
}

// Install makes the loader's version and returns its ID for launching.
// java runs the Forge/NeoForge installers.
func Install(ctx context.Context, loader, game, version, java string, log func(string)) (string, error) {
	switch loader {
	case Vanilla, "":
		return game, nil
	case Fabric, Quilt:
		base := "https://meta.fabricmc.net/v2"
		if loader == Quilt {
			base = "https://meta.quiltmc.org/v3"
		}
		var raw json.RawMessage
		if err := dl.JSON(ctx, fmt.Sprintf("%s/versions/loader/%s/%s/profile/json", base, game, version), &raw); err != nil {
			return "", err
		}
		var head struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(raw, &head); err != nil {
			return "", err
		}
		p := paths.VersionJSON(head.ID)
		os.MkdirAll(filepath.Dir(p), 0o755)
		return head.ID, os.WriteFile(p, raw, 0o644)
	case NeoForge, Forge:
		return installForge(ctx, loader, game, version, java, log)
	}
	return "", fmt.Errorf("unknown loader %q", loader)
}

func installForge(ctx context.Context, loader, game, version, java string, log func(string)) (string, error) {
	var url, id string
	if loader == NeoForge {
		url = fmt.Sprintf("https://maven.neoforged.net/releases/net/neoforged/neoforge/%s/neoforge-%s-installer.jar", version, version)
		id = "neoforge-" + version
	} else {
		full := game + "-" + version
		url = fmt.Sprintf("https://maven.minecraftforge.net/net/minecraftforge/forge/%s/forge-%s-installer.jar", full, full)
		id = game + "-forge-" + version
	}
	if _, err := os.Stat(paths.VersionJSON(id)); err == nil {
		return id, nil
	}
	// The installers want the vanilla version and a launcher profile file in
	// what they think is .minecraft; that is our shared root.
	v, err := mojang.LoadVersion(ctx, game)
	if err != nil {
		return "", err
	}
	if err := dl.All(ctx, []dl.Task{v.ClientTask()}, nil); err != nil {
		return "", err
	}
	profiles := filepath.Join(paths.Root, "launcher_profiles.json")
	if _, err := os.Stat(profiles); err != nil {
		os.WriteFile(profiles, []byte(`{"profiles":{},"selectedProfile":"","clientToken":"mclaunch"}`), 0o644)
	}
	installer := filepath.Join(paths.Cache(), filepath.Base(url))
	if err := dl.All(ctx, []dl.Task{{URL: url, Path: installer}}, nil); err != nil {
		return "", err
	}
	if log != nil {
		log("Running the " + loader + " installer…")
	}
	before := versionDirs()
	cmd := exec.CommandContext(ctx, java, "-Djava.awt.headless=true", "-jar", installer, "--installClient", paths.Root)
	cmd.Dir = paths.Cache()
	out, err := cmd.CombinedOutput()
	if log != nil {
		for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			log(l)
		}
	}
	if err != nil {
		return "", fmt.Errorf("%s installer failed: %v", loader, err)
	}
	if _, err := os.Stat(paths.VersionJSON(id)); err == nil {
		return id, nil
	}
	// The installer chose its own name: the new version directory.
	for d := range versionDirs() {
		if !before[d] && strings.Contains(strings.ToLower(d), loader) {
			return d, nil
		}
	}
	return "", fmt.Errorf("%s installer finished but made no version (%s)", loader, id)
}

func versionDirs() map[string]bool {
	m := map[string]bool{}
	entries, _ := os.ReadDir(paths.Versions())
	for _, e := range entries {
		m[e.Name()] = true
	}
	return m
}

// ---- version lists for the interface ---------------------------------------

// CompareGame orders game versions (1.21.11 < 26.1 < 26.1.2), for sorting.
func CompareGame(a, b string) int {
	num := regexp.MustCompile(`\d+`)
	pa, pb := num.FindAllString(a, -1), num.FindAllString(b, -1)
	for i := 0; i < len(pa) && i < len(pb); i++ {
		x, _ := strconv.Atoi(pa[i])
		y, _ := strconv.Atoi(pb[i])
		if x != y {
			return x - y
		}
	}
	return len(pa) - len(pb)
}

func SortGame(vs []string) {
	sort.Slice(vs, func(i, j int) bool { return CompareGame(vs[i], vs[j]) > 0 })
}
