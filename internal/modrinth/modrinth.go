// Package modrinth: mods from Modrinth (API v2, no key): search, install with
// dependencies, identify jars by hash, update, and .mrpack modpacks.
package modrinth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"mclaunch/internal/dl"
	"mclaunch/internal/instance"
)

const api = "https://api.modrinth.com/v2"

type Hit struct {
	ProjectID   string   `json:"project_id"`
	Slug        string   `json:"slug"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Author      string   `json:"author"`
	Downloads   int      `json:"downloads"`
	IconURL     string   `json:"icon_url"`
	Categories  []string `json:"categories"`
	ProjectType string   `json:"project_type"`
}

type SearchResult struct {
	Hits      []Hit `json:"hits"`
	TotalHits int   `json:"total_hits"`
}

type File struct {
	Hashes   map[string]string `json:"hashes"`
	URL      string            `json:"url"`
	Filename string            `json:"filename"`
	Primary  bool              `json:"primary"`
	Size     int64             `json:"size"`
}

type Dependency struct {
	ProjectID string `json:"project_id"`
	VersionID string `json:"version_id"`
	Type      string `json:"dependency_type"` // required, optional, incompatible, embedded
}

type Version struct {
	ID            string       `json:"id"`
	ProjectID     string       `json:"project_id"`
	Name          string       `json:"name"`
	VersionNumber string       `json:"version_number"`
	Files         []File       `json:"files"`
	Dependencies  []Dependency `json:"dependencies"`
	GameVersions  []string     `json:"game_versions"`
	Loaders       []string     `json:"loaders"`
}

func (v *Version) PrimaryFile() *File {
	for i := range v.Files {
		if v.Files[i].Primary {
			return &v.Files[i]
		}
	}
	if len(v.Files) > 0 {
		return &v.Files[0]
	}
	return nil
}

type Project struct {
	ID      string `json:"id"`
	Slug    string `json:"slug"`
	Title   string `json:"title"`
	IconURL string `json:"icon_url"`
}

func get(ctx context.Context, path string, q url.Values, out any) error {
	u := api + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return dl.JSON(ctx, u, out)
}

func post(ctx context.Context, path string, body, out any) error {
	b, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, api+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", dl.UserAgent)
	resp, err := dl.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return fmt.Errorf("modrinth %s: %s %s", path, resp.Status, msg)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// loaderList: Quilt runs Fabric mods too.
func loaderList(loader string) []string {
	switch loader {
	case "quilt":
		return []string{"quilt", "fabric"}
	case "", "vanilla":
		return nil
	}
	return []string{loader}
}

func jsonList(v []string) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// Search finds projects of a type (mod, modpack, resourcepack, shader) for a game version and loader.
func Search(ctx context.Context, query, ptype, game, loader string, offset int) (*SearchResult, error) {
	facets := [][]string{{"project_type:" + ptype}}
	if game != "" {
		facets = append(facets, []string{"versions:" + game})
	}
	if ls := loaderList(loader); ptype == "mod" && len(ls) > 0 {
		var f []string
		for _, l := range ls {
			f = append(f, "categories:"+l)
		}
		facets = append(facets, f)
	}
	fb, _ := json.Marshal(facets)
	var r SearchResult
	err := get(ctx, "/search", url.Values{"query": {query}, "facets": {string(fb)}, "limit": {"30"},
		"offset": {fmt.Sprint(offset)}, "index": {"relevance"}}, &r)
	return &r, err
}

// Versions of a project usable by the instance, newest first.
func Versions(ctx context.Context, project, game, loader string) ([]Version, error) {
	q := url.Values{"game_versions": {jsonList([]string{game})}}
	if ls := loaderList(loader); len(ls) > 0 {
		q.Set("loaders", jsonList(ls))
	}
	var vs []Version
	err := get(ctx, "/project/"+url.PathEscape(project)+"/version", q, &vs)
	return vs, err
}

// ---- the instance's mod index (mods.json) -------------------------------------

type indexEntry struct {
	Project string `json:"project"`
	Version string `json:"version"`
	Title   string `json:"title"`
	Icon    string `json:"icon"`
	Number  string `json:"versionNumber"`
	SHA1    string `json:"sha1"`
	URL     string `json:"url"`
	Size    int64  `json:"size"`
}

var indexMu sync.Mutex

func indexPath(inst *instance.Instance) string { return filepath.Join(inst.Dir(), "mods.json") }

func loadIndex(inst *instance.Instance) map[string]indexEntry {
	m := map[string]indexEntry{}
	if b, err := os.ReadFile(indexPath(inst)); err == nil {
		json.Unmarshal(b, &m)
	}
	return m
}

func saveIndex(inst *instance.Instance, m map[string]indexEntry) {
	b, _ := json.MarshalIndent(m, "", "  ")
	os.WriteFile(indexPath(inst), b, 0o644)
}

func baseName(file string) string { return strings.TrimSuffix(file, ".disabled") }

// Install puts a project's newest compatible version and its required
// dependencies into the instance's mods folder.
func Install(ctx context.Context, inst *instance.Instance, project string, log func(string)) error {
	seen := map[string]bool{}
	installed := map[string]bool{}
	for _, e := range loadIndex(inst) {
		installed[e.Project] = true
	}
	return install(ctx, inst, project, "", seen, installed, log)
}

func install(ctx context.Context, inst *instance.Instance, project, versionID string, seen, installed map[string]bool, log func(string)) error {
	if seen[project] {
		return nil
	}
	seen[project] = true
	var v *Version
	if versionID != "" {
		var one Version
		if err := get(ctx, "/version/"+versionID, nil, &one); err != nil {
			return err
		}
		v = &one
	} else {
		vs, err := Versions(ctx, project, inst.Game, inst.Loader)
		if err != nil {
			return err
		}
		if len(vs) == 0 {
			return fmt.Errorf("%s has no version for %s %s", project, inst.Loader, inst.Game)
		}
		v = &vs[0]
	}
	var proj Project
	get(ctx, "/project/"+v.ProjectID, nil, &proj)
	f := v.PrimaryFile()
	if f == nil {
		return fmt.Errorf("%s %s has no file", project, v.VersionNumber)
	}
	{
		if log != nil {
			log(fmt.Sprintf("%s %s", nonEmpty(proj.Title, project), v.VersionNumber))
		}
		// An older file of the same project goes away.
		indexMu.Lock()
		idx := loadIndex(inst)
		for file, e := range idx {
			if e.Project == v.ProjectID && file != f.Filename {
				os.Remove(filepath.Join(inst.ModsDir(), file))
				os.Remove(filepath.Join(inst.ModsDir(), file+".disabled"))
				delete(idx, file)
			}
		}
		indexMu.Unlock()
		if err := dl.All(ctx, []dl.Task{{URL: f.URL, Path: filepath.Join(inst.ModsDir(), f.Filename),
			SHA1: f.Hashes["sha1"], Size: f.Size}}, nil); err != nil {
			return err
		}
		indexMu.Lock()
		idx[f.Filename] = indexEntry{Project: v.ProjectID, Version: v.ID, Title: proj.Title, Icon: proj.IconURL,
			Number: v.VersionNumber, SHA1: f.Hashes["sha1"], URL: f.URL, Size: f.Size}
		saveIndex(inst, idx)
		indexMu.Unlock()
		installed[v.ProjectID] = true
	}
	for _, d := range v.Dependencies {
		if d.Type != "required" || d.ProjectID == "" || installed[d.ProjectID] {
			continue
		}
		if err := install(ctx, inst, d.ProjectID, d.VersionID, seen, installed, log); err != nil {
			if log != nil {
				log("dependency: " + err.Error())
			}
		}
	}
	return nil
}

func nonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// Identify lists the mods folder with what Modrinth knows about each jar
// (looked up by SHA1 once, then kept in mods.json).
func Identify(ctx context.Context, inst *instance.Instance) ([]instance.ModFile, error) {
	mods := inst.Mods()
	indexMu.Lock()
	idx := loadIndex(inst)
	indexMu.Unlock()
	var unknown []string
	byHash := map[string]string{}
	for i := range mods {
		name := baseName(mods[i].File)
		if e, ok := idx[name]; ok && e.Size == mods[i].Size {
			continue
		}
		sum, err := dl.FileSHA1(filepath.Join(inst.ModsDir(), mods[i].File))
		if err != nil {
			continue
		}
		unknown = append(unknown, sum)
		byHash[sum] = name
	}
	if len(unknown) > 0 {
		var found map[string]Version
		if err := post(ctx, "/version_files", map[string]any{"hashes": unknown, "algorithm": "sha1"}, &found); err == nil {
			var ids []string
			for _, v := range found {
				ids = append(ids, v.ProjectID)
			}
			projects := map[string]Project{}
			if len(ids) > 0 {
				var ps []Project
				get(ctx, "/projects", url.Values{"ids": {jsonList(ids)}}, &ps)
				for _, p := range ps {
					projects[p.ID] = p
				}
			}
			for sum, name := range byHash {
				e := indexEntry{SHA1: sum}
				if v, ok := found[sum]; ok {
					p := projects[v.ProjectID]
					e = indexEntry{Project: v.ProjectID, Version: v.ID, Title: p.Title, Icon: p.IconURL,
						Number: v.VersionNumber, SHA1: sum}
					if f := v.PrimaryFile(); f != nil {
						e.URL, e.Size = f.URL, f.Size
					}
				}
				if st, err := os.Stat(filepath.Join(inst.ModsDir(), name)); err == nil {
					e.Size = st.Size()
				} else if st, err := os.Stat(filepath.Join(inst.ModsDir(), name+".disabled")); err == nil {
					e.Size = st.Size()
				}
				idx[name] = e
			}
			indexMu.Lock()
			saveIndex(inst, idx)
			indexMu.Unlock()
		}
	}
	for i := range mods {
		if e, ok := idx[baseName(mods[i].File)]; ok {
			mods[i].SHA1, mods[i].Project, mods[i].Version = e.SHA1, e.Project, e.Version
			mods[i].Title, mods[i].Icon, mods[i].VersionN = e.Title, e.Icon, e.Number
		}
	}
	return mods, nil
}

// UpdateAll replaces each Modrinth mod with its newest compatible version.
func UpdateAll(ctx context.Context, inst *instance.Instance, log func(string)) (int, error) {
	mods, err := Identify(ctx, inst)
	if err != nil {
		return 0, err
	}
	var hashes []string
	files := map[string]instance.ModFile{}
	for _, m := range mods {
		if m.Project != "" && m.SHA1 != "" {
			hashes = append(hashes, m.SHA1)
			files[m.SHA1] = m
		}
	}
	if len(hashes) == 0 {
		return 0, nil
	}
	body := map[string]any{"hashes": hashes, "algorithm": "sha1", "game_versions": []string{inst.Game}}
	if ls := loaderList(inst.Loader); len(ls) > 0 {
		body["loaders"] = ls
	}
	var latest map[string]Version
	if err := post(ctx, "/version_files/update", body, &latest); err != nil {
		return 0, err
	}
	n := 0
	for sum, v := range latest {
		m := files[sum]
		if v.ID == m.Version {
			continue
		}
		f := v.PrimaryFile()
		if f == nil {
			continue
		}
		if log != nil {
			log(fmt.Sprintf("%s %s -> %s", nonEmpty(m.Title, m.File), m.VersionN, v.VersionNumber))
		}
		dst := filepath.Join(inst.ModsDir(), f.Filename)
		if !m.Enabled {
			dst += ".disabled"
		}
		if err := dl.All(ctx, []dl.Task{{URL: f.URL, Path: dst, SHA1: f.Hashes["sha1"], Size: f.Size}}, nil); err != nil {
			return n, err
		}
		if filepath.Base(dst) != m.File {
			os.Remove(filepath.Join(inst.ModsDir(), m.File))
		}
		indexMu.Lock()
		idx := loadIndex(inst)
		delete(idx, baseName(m.File))
		idx[f.Filename] = indexEntry{Project: v.ProjectID, Version: v.ID, Title: m.Title, Icon: m.Icon,
			Number: v.VersionNumber, SHA1: f.Hashes["sha1"], URL: f.URL, Size: f.Size}
		saveIndex(inst, idx)
		indexMu.Unlock()
		n++
	}
	return n, nil
}

// The one-click performance set for Fabric. (Not Quilt: Quilt Loader 0.24
// presents an older Fabric Loader than these mods require.)
var PerformanceMods = []string{"sodium", "lithium", "ferrite-core", "modernfix", "immediatelyfast", "entityculling"}

func InstallPerformanceMods(ctx context.Context, inst *instance.Instance, log func(string)) error {
	if inst.Loader != "fabric" {
		return fmt.Errorf("the performance mods are for Fabric")
	}
	for _, slug := range PerformanceMods {
		if err := Install(ctx, inst, slug, log); err != nil && log != nil {
			log("skipped " + slug + ": " + err.Error())
		}
	}
	return nil
}
