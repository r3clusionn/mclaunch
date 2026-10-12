// Package mojang reads Mojang's version manifest and version files and
// puts a version's client, libraries, assets and Java runtime on disk.
package mojang

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"mclaunch/internal/dl"
	"mclaunch/internal/paths"
)

const ManifestURL = "https://piston-meta.mojang.com/mc/game/version_manifest_v2.json"

type ManifestVersion struct {
	ID          string    `json:"id"`
	Type        string    `json:"type"` // release, snapshot, old_beta, old_alpha
	URL         string    `json:"url"`
	SHA1        string    `json:"sha1"`
	ReleaseTime time.Time `json:"releaseTime"`
}

type Manifest struct {
	Latest struct {
		Release  string `json:"release"`
		Snapshot string `json:"snapshot"`
	} `json:"latest"`
	Versions []ManifestVersion `json:"versions"`
}

// GetManifest returns the version list, refreshed every hour.
func GetManifest(ctx context.Context) (*Manifest, error) {
	var m Manifest
	err := dl.CachedJSON(ctx, ManifestURL, filepath.Join(paths.Cache(), "version_manifest_v2.json"), time.Hour, &m)
	return &m, err
}

func (m *Manifest) Find(id string) *ManifestVersion {
	for i := range m.Versions {
		if m.Versions[i].ID == id {
			return &m.Versions[i]
		}
	}
	return nil
}

// ---- version files -------------------------------------------------------

type Artifact struct {
	Path string `json:"path"`
	SHA1 string `json:"sha1"`
	Size int64  `json:"size"`
	URL  string `json:"url"`
}

type Rule struct {
	Action string `json:"action"`
	OS     struct {
		Name string `json:"name"`
		Arch string `json:"arch"`
	} `json:"os"`
	Features map[string]bool `json:"features"`
}

type Library struct {
	Name      string `json:"name"`
	Downloads struct {
		Artifact    *Artifact           `json:"artifact"`
		Classifiers map[string]Artifact `json:"classifiers"`
	} `json:"downloads"`
	Natives map[string]string `json:"natives"`
	Extract struct {
		Exclude []string `json:"exclude"`
	} `json:"extract"`
	Rules []Rule `json:"rules"`
	// Fabric and Quilt profiles: a Maven repository instead of downloads.
	URL  string `json:"url"`
	SHA1 string `json:"sha1"`
	Size int64  `json:"size"`
}

// Argument is a plain string or a ruled value (a string or a list).
type Argument struct {
	Rules []Rule
	Value []string
}

func (a *Argument) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		a.Value = []string{s}
		return nil
	}
	var r struct {
		Rules []Rule          `json:"rules"`
		Value json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return err
	}
	a.Rules = r.Rules
	if json.Unmarshal(r.Value, &s) == nil {
		a.Value = []string{s}
		return nil
	}
	return json.Unmarshal(r.Value, &a.Value)
}

type Version struct {
	ID                 string `json:"id"`
	InheritsFrom       string `json:"inheritsFrom,omitempty"`
	Type               string `json:"type"`
	MainClass          string `json:"mainClass"`
	MinecraftArguments string `json:"minecraftArguments,omitempty"`
	Arguments          struct {
		Game []Argument `json:"game"`
		JVM  []Argument `json:"jvm"`
	} `json:"arguments"`
	AssetIndex struct {
		ID        string `json:"id"`
		SHA1      string `json:"sha1"`
		Size      int64  `json:"size"`
		TotalSize int64  `json:"totalSize"`
		URL       string `json:"url"`
	} `json:"assetIndex"`
	Assets    string `json:"assets"`
	Downloads struct {
		Client Artifact `json:"client"`
	} `json:"downloads"`
	Libraries   []Library `json:"libraries"`
	JavaVersion struct {
		Component    string `json:"component"`
		MajorVersion int    `json:"majorVersion"`
	} `json:"javaVersion"`

	// The vanilla version whose jar is the game (the root of inheritsFrom).
	JarID string `json:"-"`
}

// LoadVersion reads versions/<id>/<id>.json, downloading vanilla versions
// from the manifest, and merges what it inherits from.
func LoadVersion(ctx context.Context, id string) (*Version, error) {
	path := paths.VersionJSON(id)
	b, err := os.ReadFile(path)
	if err != nil {
		m, merr := GetManifest(ctx)
		if merr != nil {
			return nil, merr
		}
		mv := m.Find(id)
		if mv == nil {
			return nil, fmt.Errorf("version %s not found", id)
		}
		if err := dl.All(ctx, []dl.Task{{URL: mv.URL, Path: path, SHA1: mv.SHA1}}, nil); err != nil {
			return nil, err
		}
		if b, err = os.ReadFile(path); err != nil {
			return nil, err
		}
	}
	var v Version
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	v.JarID = v.ID
	if v.InheritsFrom == "" {
		return &v, nil
	}
	parent, err := LoadVersion(ctx, v.InheritsFrom)
	if err != nil {
		return nil, err
	}
	return merge(parent, &v), nil
}

// libKey is a library's group:artifact(:classifier), to let a loader's
// library replace the parent's.
func libKey(name string) string {
	p := strings.Split(name, ":")
	if len(p) >= 4 {
		return p[0] + ":" + p[1] + ":" + p[3]
	}
	if len(p) >= 2 {
		return p[0] + ":" + p[1]
	}
	return name
}

func merge(parent, child *Version) *Version {
	v := *parent
	v.ID = child.ID
	v.InheritsFrom = child.InheritsFrom
	v.JarID = parent.JarID
	if child.MainClass != "" {
		v.MainClass = child.MainClass
	}
	if child.MinecraftArguments != "" {
		v.MinecraftArguments = child.MinecraftArguments
	}
	if child.Type != "" {
		v.Type = child.Type
	}
	if child.JavaVersion.Component != "" {
		v.JavaVersion = child.JavaVersion
	}
	v.Arguments.Game = append(append([]Argument{}, parent.Arguments.Game...), child.Arguments.Game...)
	v.Arguments.JVM = append(append([]Argument{}, parent.Arguments.JVM...), child.Arguments.JVM...)
	childKeys := map[string]bool{}
	for _, l := range child.Libraries {
		childKeys[libKey(l.Name)] = true
	}
	v.Libraries = append([]Library{}, child.Libraries...)
	for _, l := range parent.Libraries {
		if !childKeys[libKey(l.Name)] {
			v.Libraries = append(v.Libraries, l)
		}
	}
	return &v
}

// ---- rules -----------------------------------------------------------------

// Features are the launcher features rules can ask about.
type Features map[string]bool

func osArch() string {
	if runtime.GOARCH == "amd64" {
		return "x86_64"
	}
	return runtime.GOARCH
}

// Allowed evaluates rules for Linux on this machine: the last matching rule wins.
func Allowed(rules []Rule, f Features) bool {
	if len(rules) == 0 {
		return true
	}
	allow := false
	for _, r := range rules {
		match := true
		if r.OS.Name != "" && r.OS.Name != "linux" {
			match = false
		}
		if r.OS.Arch != "" && r.OS.Arch != osArch() {
			match = false
		}
		for k, want := range r.Features {
			if f[k] != want {
				match = false
			}
		}
		if match {
			allow = r.Action == "allow"
		}
	}
	return allow
}

// ---- libraries -------------------------------------------------------------

// MavenPath turns group:artifact:version[:classifier][@ext] into a path.
func MavenPath(name string) string {
	ext := "jar"
	if i := strings.LastIndexByte(name, '@'); i >= 0 {
		ext = name[i+1:]
		name = name[:i]
	}
	p := strings.Split(name, ":")
	if len(p) < 3 {
		return name
	}
	file := p[1] + "-" + p[2]
	if len(p) >= 4 {
		file += "-" + p[3]
	}
	return filepath.Join(strings.ReplaceAll(p[0], ".", "/"), p[1], p[2], file+"."+ext)
}

// LibraryFile is a library jar to have, and whether it goes on the class
// path or is a native archive to extract.
type LibraryFile struct {
	Task    dl.Task
	Native  bool
	Exclude []string
}

func nativeClassifier(l *Library) string {
	c := l.Natives["linux"]
	return strings.ReplaceAll(c, "${arch}", "64")
}

// Files lists the libraries this machine needs.
func (v *Version) Files(f Features) []LibraryFile {
	var out []LibraryFile
	for i := range v.Libraries {
		l := &v.Libraries[i]
		if !Allowed(l.Rules, f) {
			continue
		}
		if a := l.Downloads.Artifact; a != nil {
			p := a.Path
			if p == "" {
				p = MavenPath(l.Name)
			}
			if a.URL != "" || fileExists(filepath.Join(paths.Libraries(), p)) {
				out = append(out, LibraryFile{Task: dl.Task{URL: a.URL, Path: filepath.Join(paths.Libraries(), p),
					SHA1: a.SHA1, Size: a.Size}})
			}
		} else if len(l.Natives) == 0 {
			// Maven repository (Fabric, Quilt), or a file an installer put there.
			p := MavenPath(l.Name)
			base := l.URL
			if base == "" {
				base = "https://libraries.minecraft.net/"
			}
			if !strings.HasSuffix(base, "/") {
				base += "/"
			}
			out = append(out, LibraryFile{Task: dl.Task{URL: base + filepath.ToSlash(p),
				Path: filepath.Join(paths.Libraries(), p), SHA1: l.SHA1, Size: l.Size}})
		}
		if c := nativeClassifier(l); c != "" {
			if a, ok := l.Downloads.Classifiers[c]; ok {
				out = append(out, LibraryFile{Task: dl.Task{URL: a.URL, Path: filepath.Join(paths.Libraries(), a.Path),
					SHA1: a.SHA1, Size: a.Size}, Native: true, Exclude: l.Extract.Exclude})
			}
		}
	}
	return out
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// ClientTask is the game jar.
func (v *Version) ClientTask() dl.Task {
	c := v.Downloads.Client
	return dl.Task{URL: c.URL, Path: paths.VersionJar(v.JarID), SHA1: c.SHA1, Size: c.Size}
}

// ---- assets ----------------------------------------------------------------

type AssetIndex struct {
	Objects map[string]struct {
		Hash string `json:"hash"`
		Size int64  `json:"size"`
	} `json:"objects"`
	Virtual        bool `json:"virtual"`
	MapToResources bool `json:"map_to_resources"`
}

// AssetTasks reads (downloading if needed) the asset index and lists the objects.
func (v *Version) AssetTasks(ctx context.Context) ([]dl.Task, *AssetIndex, error) {
	ai := v.AssetIndex
	idxPath := filepath.Join(paths.Assets(), "indexes", ai.ID+".json")
	if err := dl.All(ctx, []dl.Task{{URL: ai.URL, Path: idxPath, SHA1: ai.SHA1, Size: ai.Size}}, nil); err != nil {
		return nil, nil, err
	}
	b, err := os.ReadFile(idxPath)
	if err != nil {
		return nil, nil, err
	}
	var idx AssetIndex
	if err := json.Unmarshal(b, &idx); err != nil {
		return nil, nil, err
	}
	var tasks []dl.Task
	for _, o := range idx.Objects {
		sub := o.Hash[:2] + "/" + o.Hash
		tasks = append(tasks, dl.Task{URL: "https://resources.download.minecraft.net/" + sub,
			Path: filepath.Join(paths.Assets(), "objects", sub), SHA1: o.Hash, Size: o.Size})
	}
	return tasks, &idx, nil
}

// VirtualAssets copies objects to their names for the old versions that
// read assets that way (assets/virtual/<index> or the game's resources/).
func (v *Version) VirtualAssets(idx *AssetIndex, gameDir string) (string, error) {
	if !idx.Virtual && !idx.MapToResources {
		return paths.Assets(), nil
	}
	dir := filepath.Join(paths.Assets(), "virtual", v.AssetIndex.ID)
	if idx.MapToResources {
		dir = filepath.Join(gameDir, "resources")
	}
	for name, o := range idx.Objects {
		dst := filepath.Join(dir, filepath.FromSlash(name))
		if fileExists(dst) {
			continue
		}
		os.MkdirAll(filepath.Dir(dst), 0o755)
		src := filepath.Join(paths.Assets(), "objects", o.Hash[:2], o.Hash)
		if err := os.Link(src, dst); err != nil {
			b, rerr := os.ReadFile(src)
			if rerr != nil {
				return "", rerr
			}
			if err := os.WriteFile(dst, b, 0o644); err != nil {
				return "", err
			}
		}
	}
	return dir, nil
}

// ---- arguments -------------------------------------------------------------

// Expand resolves the version's JVM and game arguments with the values.
func (v *Version) Expand(f Features, vals map[string]string) (jvm, game []string) {
	sub := func(s string) string {
		for k, val := range vals {
			s = strings.ReplaceAll(s, "${"+k+"}", val)
		}
		return s
	}
	for _, a := range v.Arguments.JVM {
		if Allowed(a.Rules, f) {
			for _, s := range a.Value {
				jvm = append(jvm, sub(s))
			}
		}
	}
	if v.MinecraftArguments != "" {
		for _, s := range strings.Fields(v.MinecraftArguments) {
			game = append(game, sub(s))
		}
	}
	for _, a := range v.Arguments.Game {
		if Allowed(a.Rules, f) {
			for _, s := range a.Value {
				game = append(game, sub(s))
			}
		}
	}
	if len(v.Arguments.JVM) == 0 {
		// Before 1.13 the JVM arguments weren't in the file.
		jvm = append(jvm, "-Djava.library.path="+vals["natives_directory"], "-cp", vals["classpath"])
	}
	return jvm, game
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}
