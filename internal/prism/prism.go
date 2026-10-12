// Package prism imports Prism Launcher (and MultiMC) instances: instance.cfg
// for the name and Java settings, mmc-pack.json for the version and loader,
// and the game folder copied over.
package prism

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"mclaunch/internal/instance"
)

// DefaultDir is where Prism keeps its instances.
func DefaultDir() string {
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return filepath.Join(d, "PrismLauncher", "instances")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "PrismLauncher", "instances")
}

func readCfg(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	m := map[string]string{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '[' || line[0] == '#' {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			m[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"`)
		}
	}
	return m, sc.Err()
}

var components = map[string]string{
	"net.fabricmc.fabric-loader": "fabric",
	"org.quiltmc.quilt-loader":   "quilt",
	"net.neoforged":              "neoforge",
	"net.minecraftforge":         "forge",
}

// Import one Prism instance directory.
func Import(dir string) (*instance.Instance, error) {
	cfg, err := readCfg(filepath.Join(dir, "instance.cfg"))
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(filepath.Join(dir, "mmc-pack.json"))
	if err != nil {
		return nil, err
	}
	var pack struct {
		Components []struct {
			UID     string `json:"uid"`
			Version string `json:"version"`
		} `json:"components"`
	}
	if err := json.Unmarshal(b, &pack); err != nil {
		return nil, fmt.Errorf("mmc-pack.json: %w", err)
	}
	game, loader, lv := "", "vanilla", ""
	for _, c := range pack.Components {
		if c.UID == "net.minecraft" {
			game = c.Version
		}
		if l, ok := components[c.UID]; ok {
			loader, lv = l, c.Version
		}
	}
	if game == "" {
		return nil, errors.New("no Minecraft version in mmc-pack.json")
	}
	name := cfg["name"]
	if name == "" {
		name = filepath.Base(dir)
	}
	inst := instance.New(name, game, loader, lv)
	inst.ImportedFrom = dir
	if cfg["OverrideMemory"] == "true" {
		if mx, err := strconv.Atoi(cfg["MaxMemAlloc"]); err == nil && mx > 0 {
			inst.MemoryMB = mx
		}
	}
	if cfg["OverrideJavaArgs"] == "true" {
		inst.JVMArgs = cfg["JvmArgs"]
	}
	if cfg["OverrideJavaLocation"] == "true" {
		inst.Java = cfg["JavaPath"]
	}
	if w, err := strconv.Atoi(cfg["MinecraftWinWidth"]); err == nil && w > 0 && cfg["OverrideWindow"] == "true" {
		inst.Width = w
		if h, err := strconv.Atoi(cfg["MinecraftWinHeight"]); err == nil && h > 0 {
			inst.Height = h
		}
	}
	src := filepath.Join(dir, ".minecraft")
	if _, err := os.Stat(src); err != nil {
		src = filepath.Join(dir, "minecraft")
	}
	if err := inst.Save(); err != nil {
		return nil, err
	}
	if _, err := os.Stat(src); err == nil {
		if err := copyTree(src, inst.GameDir()); err != nil {
			return inst, fmt.Errorf("copying the game folder: %w", err)
		}
	}
	return inst, nil
}

// ImportAll imports every instance in a Prism instances folder.
func ImportAll(root string, log func(string)) ([]*instance.Instance, error) {
	if root == "" {
		root = DefaultDir()
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("no Prism instances at %s", root)
	}
	var out []*instance.Instance
	var errs []error
	for _, e := range entries {
		dir := filepath.Join(root, e.Name())
		if _, err := os.Stat(filepath.Join(dir, "instance.cfg")); err != nil {
			continue
		}
		inst, err := Import(dir)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", e.Name(), err))
			continue
		}
		if log != nil {
			log("imported " + inst.Name)
		}
		out = append(out, inst)
	}
	return out, errors.Join(errs...)
}

// copyTree copies files (worlds are written in place, so no hard links).
func copyTree(src, dst string) error {
	return filepath.Walk(src, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		switch {
		case fi.IsDir():
			return os.MkdirAll(target, 0o755)
		case fi.Mode()&os.ModeSymlink != 0:
			link, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		case fi.Mode().IsRegular():
			r, err := os.Open(p)
			if err != nil {
				return err
			}
			defer r.Close()
			w, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, fi.Mode().Perm())
			if err != nil {
				return err
			}
			if _, err := io.Copy(w, r); err != nil {
				w.Close()
				return err
			}
			return w.Close()
		}
		return nil
	})
}
