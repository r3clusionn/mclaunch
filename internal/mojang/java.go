package mojang

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"mclaunch/internal/dl"
	"mclaunch/internal/paths"
)

// Mojang's Java runtimes, the ones the official launcher uses: no system Java.
const javaAllURL = "https://launchermeta.mojang.com/v1/products/java-runtime/2ec0cc96c44e5a76b9c8b7c39df7210883d12871/all.json"

type javaAll map[string]map[string][]struct {
	Manifest struct {
		SHA1 string `json:"sha1"`
		Size int64  `json:"size"`
		URL  string `json:"url"`
	} `json:"manifest"`
	Version struct {
		Name string `json:"name"`
	} `json:"version"`
}

type javaManifest struct {
	Files map[string]struct {
		Type       string `json:"type"` // file, directory, link
		Executable bool   `json:"executable"`
		Target     string `json:"target"`
		Downloads  struct {
			Raw struct {
				SHA1 string `json:"sha1"`
				Size int64  `json:"size"`
				URL  string `json:"url"`
			} `json:"raw"`
		} `json:"downloads"`
	} `json:"files"`
}

// JavaComponent is the runtime a version asks for (Java 8 for old versions).
func (v *Version) JavaComponent() string {
	if v.JavaVersion.Component != "" {
		return v.JavaVersion.Component
	}
	return "jre-legacy"
}

// JavaBinary is where a component's java ends up.
func JavaBinary(component string) string {
	return filepath.Join(paths.Java(), component, "bin", "java")
}

// EnsureJava downloads a Java runtime component and returns its java binary.
func EnsureJava(ctx context.Context, component string, progress dl.Progress) (string, error) {
	dir := filepath.Join(paths.Java(), component)
	bin := JavaBinary(component)
	marker := filepath.Join(dir, ".complete")
	if _, err := os.Stat(marker); err == nil {
		return bin, nil
	}
	var all javaAll
	if err := dl.CachedJSON(ctx, javaAllURL, filepath.Join(paths.Cache(), "java-all.json"), 24*time.Hour, &all); err != nil {
		return "", err
	}
	entries := all["linux"][component]
	if len(entries) == 0 {
		return "", fmt.Errorf("no %s Java runtime for Linux", component)
	}
	e := entries[0]
	var m javaManifest
	mpath := filepath.Join(paths.Cache(), "java-"+component+".json")
	if err := dl.All(ctx, []dl.Task{{URL: e.Manifest.URL, Path: mpath, SHA1: e.Manifest.SHA1, Size: e.Manifest.Size}}, nil); err != nil {
		return "", err
	}
	if err := readJSON(mpath, &m); err != nil {
		return "", err
	}
	var tasks []dl.Task
	var links [][2]string
	for name, f := range m.Files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		switch f.Type {
		case "directory":
			os.MkdirAll(p, 0o755)
		case "link":
			links = append(links, [2]string{f.Target, p})
		case "file":
			mode := os.FileMode(0o644)
			if f.Executable {
				mode = 0o755
			}
			r := f.Downloads.Raw
			tasks = append(tasks, dl.Task{URL: r.URL, Path: p, SHA1: r.SHA1, Size: r.Size, Mode: mode})
		}
	}
	if err := dl.All(ctx, tasks, progress); err != nil {
		return "", err
	}
	for _, l := range links {
		os.MkdirAll(filepath.Dir(l[1]), 0o755)
		os.Remove(l[1])
		os.Symlink(l[0], l[1])
	}
	os.WriteFile(marker, []byte(e.Version.Name+"\n"), 0o644)
	return bin, nil
}
