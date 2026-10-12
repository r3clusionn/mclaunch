package modrinth

import (
	"archive/zip"
	"context"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"mclaunch/internal/dl"
	"mclaunch/internal/instance"
)

// The Modrinth modpack format (modrinth.index.json in a zip, plus overrides/).
type packIndex struct {
	FormatVersion int    `json:"formatVersion"`
	Game          string `json:"game"`
	VersionID     string `json:"versionId"`
	Name          string `json:"name"`
	Summary       string `json:"summary,omitempty"`
	Files         []struct {
		Path      string            `json:"path"`
		Hashes    map[string]string `json:"hashes"`
		Env       map[string]string `json:"env,omitempty"`
		Downloads []string          `json:"downloads"`
		FileSize  int64             `json:"fileSize"`
	} `json:"files"`
	Dependencies map[string]string `json:"dependencies"`
}

var loaderKeys = map[string]string{
	"fabric-loader": "fabric", "quilt-loader": "quilt", "neoforge": "neoforge", "forge": "forge",
}

func safeJoin(dir, rel string) (string, error) {
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if !strings.HasPrefix(p, filepath.Clean(dir)+string(os.PathSeparator)) {
		return "", fmt.Errorf("unsafe path in pack: %s", rel)
	}
	return p, nil
}

// ImportPack makes an instance from a .mrpack file.
func ImportPack(ctx context.Context, file, name string, log func(string)) (*instance.Instance, error) {
	z, err := zip.OpenReader(file)
	if err != nil {
		return nil, err
	}
	defer z.Close()
	var idx packIndex
	for _, f := range z.File {
		if f.Name == "modrinth.index.json" {
			r, err := f.Open()
			if err != nil {
				return nil, err
			}
			err = json.NewDecoder(r).Decode(&idx)
			r.Close()
			if err != nil {
				return nil, err
			}
		}
	}
	if idx.Dependencies["minecraft"] == "" {
		return nil, errors.New("not a Modrinth modpack (no modrinth.index.json)")
	}
	loader, lv := "vanilla", ""
	for k, l := range loaderKeys {
		if v := idx.Dependencies[k]; v != "" {
			loader, lv = l, v
		}
	}
	if name == "" {
		name = idx.Name
	}
	inst := instance.New(name, idx.Dependencies["minecraft"], loader, lv)
	if len(idx.Files) > 60 {
		inst.MemoryMB = 6144 // a big pack
	}
	if err := inst.Save(); err != nil {
		return nil, err
	}
	var tasks []dl.Task
	for _, f := range idx.Files {
		if f.Env["client"] == "unsupported" || len(f.Downloads) == 0 {
			continue
		}
		p, err := safeJoin(inst.GameDir(), f.Path)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, dl.Task{URL: f.Downloads[0], Path: p, SHA1: f.Hashes["sha1"], Size: f.FileSize})
	}
	if log != nil {
		log(fmt.Sprintf("%s %s: %d files for %s %s %s", idx.Name, idx.VersionID, len(tasks), loader, lv, inst.Game))
	}
	if err := dl.All(ctx, tasks, nil); err != nil {
		return inst, err
	}
	for _, f := range z.File {
		var rel string
		switch {
		case strings.HasPrefix(f.Name, "overrides/"):
			rel = strings.TrimPrefix(f.Name, "overrides/")
		case strings.HasPrefix(f.Name, "client-overrides/"):
			rel = strings.TrimPrefix(f.Name, "client-overrides/")
		default:
			continue
		}
		if rel == "" || f.FileInfo().IsDir() {
			continue
		}
		dst, err := safeJoin(inst.GameDir(), rel)
		if err != nil {
			return inst, err
		}
		os.MkdirAll(filepath.Dir(dst), 0o755)
		r, err := f.Open()
		if err != nil {
			return inst, err
		}
		w, err := os.Create(dst)
		if err == nil {
			_, err = io.Copy(w, r)
			w.Close()
		}
		r.Close()
		if err != nil {
			return inst, err
		}
	}
	return inst, nil
}

// ExportPack writes the instance as a .mrpack: Modrinth mods as downloads,
// everything else in mods/ and config/ as overrides.
func ExportPack(ctx context.Context, inst *instance.Instance, out string) error {
	mods, err := Identify(ctx, inst)
	if err != nil {
		return err
	}
	idx := map[string]any{"formatVersion": 1, "game": "minecraft", "versionId": "1.0", "name": inst.Name}
	deps := map[string]string{"minecraft": inst.Game}
	for k, l := range loaderKeys {
		if l == inst.Loader && inst.LoaderVersion != "" {
			deps[k] = inst.LoaderVersion
		}
	}
	idx["dependencies"] = deps
	modIndex := loadIndex(inst)
	var files []map[string]any
	var overrides []string
	for _, m := range mods {
		e := modIndex[baseName(m.File)]
		if m.Enabled && e.URL != "" && e.SHA1 != "" {
			// The format wants sha512 too: hash the file.
			sum512, _ := fileSHA512(filepath.Join(inst.ModsDir(), m.File))
			files = append(files, map[string]any{"path": "mods/" + m.File,
				"hashes":    map[string]string{"sha1": e.SHA1, "sha512": sum512},
				"downloads": []string{e.URL}, "fileSize": m.Size, "env": map[string]string{"client": "required", "server": "required"}})
		} else if m.Enabled {
			overrides = append(overrides, filepath.Join("mods", m.File))
		}
	}
	idx["files"] = files
	filepath.Walk(filepath.Join(inst.GameDir(), "config"), func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			rel, _ := filepath.Rel(inst.GameDir(), p)
			overrides = append(overrides, rel)
		}
		return nil
	})
	f, err := os.Create(out)
	if err != nil {
		return err
	}
	defer f.Close()
	z := zip.NewWriter(f)
	w, _ := z.Create("modrinth.index.json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(idx)
	for _, rel := range overrides {
		r, err := os.Open(filepath.Join(inst.GameDir(), rel))
		if err != nil {
			continue
		}
		w, _ := z.Create("overrides/" + filepath.ToSlash(rel))
		io.Copy(w, r)
		r.Close()
	}
	return z.Close()
}

func fileSHA512(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha512.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
