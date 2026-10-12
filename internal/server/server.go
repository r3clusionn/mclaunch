// Package server: the JSON API and event stream the interface uses, and the
// interface's files.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"mclaunch/internal/auth"
	"mclaunch/internal/instance"
	"mclaunch/internal/launch"
	"mclaunch/internal/loaders"
	"mclaunch/internal/modrinth"
	"mclaunch/internal/mojang"
	"mclaunch/internal/paths"
	"mclaunch/internal/prism"
)

// Settings the interface changes.
type Settings struct {
	MSAClientID   string `json:"msaClientId"`
	CloseOnLaunch bool   `json:"closeOnLaunch"`
}

func settingsPath() string { return filepath.Join(paths.Root, "settings.json") }

func LoadSettings() Settings {
	s := Settings{CloseOnLaunch: true}
	if b, err := os.ReadFile(settingsPath()); err == nil {
		json.Unmarshal(b, &s)
	}
	return s
}

func (s Settings) save() error {
	os.MkdirAll(paths.Root, 0o755)
	b, _ := json.MarshalIndent(s, "", "  ")
	return os.WriteFile(settingsPath(), b, 0o644)
}

// Launched is what the window's owner needs when a game started: the
// interface closes and a supervisor waits for the game.
type Launched struct {
	PID      int
	Instance string
	Started  time.Time
}

type Server struct {
	web      fs.FS
	OnLaunch func(Launched) // called after the game started (close the window)

	mu      sync.Mutex
	clients map[chan []byte]bool
	busy    map[string]string // instance -> current task
	running map[string]Launched
	msa     *msaFlow
}

type msaFlow struct {
	Code   *auth.DeviceCode `json:"code"`
	Status string           `json:"status"` // waiting, done, error
	Error  string           `json:"error,omitempty"`
	Name   string           `json:"name,omitempty"`
	cancel context.CancelFunc
}

func New(web fs.FS) *Server {
	return &Server{web: web, clients: map[chan []byte]bool{}, busy: map[string]string{}, running: map[string]Launched{}}
}

// Listen serves on a free localhost port and returns its address.
func (s *Server) Listen() (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	go http.Serve(ln, s.routes())
	return ln.Addr().String(), nil
}

// ---- events (server-sent) ----------------------------------------------------

func (s *Server) emit(kind string, data map[string]any) {
	if data == nil {
		data = map[string]any{}
	}
	data["type"] = kind
	b, _ := json.Marshal(data)
	s.mu.Lock()
	for c := range s.clients {
		select {
		case c <- b:
		default: // a slow client misses a progress update, not the app
		}
	}
	s.mu.Unlock()
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "no streaming", 500)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	c := make(chan []byte, 64)
	s.mu.Lock()
	s.clients[c] = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.clients, c)
		s.mu.Unlock()
	}()
	fmt.Fprint(w, ": hi\n\n")
	fl.Flush()
	for {
		select {
		case <-r.Context().Done():
			return
		case b := <-c:
			fmt.Fprintf(w, "data: %s\n\n", b)
			fl.Flush()
		}
	}
}

// reporter turns launch progress into events for one instance.
type reporter struct {
	s    *Server
	id   string
	last time.Time
}

func (r *reporter) Step(msg string) {
	r.s.mu.Lock()
	r.s.busy[r.id] = msg
	r.s.mu.Unlock()
	r.s.emit("progress", map[string]any{"instance": r.id, "step": msg})
}

func (r *reporter) Bytes(done, total int64, files, totalFiles int) {
	if time.Since(r.last) < 200*time.Millisecond && files < totalFiles {
		return
	}
	r.last = time.Now()
	r.s.emit("progress", map[string]any{"instance": r.id, "done": done, "total": total, "files": files, "totalFiles": totalFiles})
}

// ---- helpers -------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, err error, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

func body(r *http.Request, v any) error {
	return json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(v)
}

func (s *Server) inst(w http.ResponseWriter, r *http.Request) *instance.Instance {
	i, err := instance.Load(r.PathValue("id"))
	if err != nil {
		fail(w, fmt.Errorf("no instance %s", r.PathValue("id")), 404)
		return nil
	}
	return i
}

// ---- routes ----------------------------------------------------------------------

type instanceView struct {
	*instance.Instance
	Busy    string `json:"busy,omitempty"`
	Running bool   `json:"running"`
	Mods    int    `json:"mods"`
}

func (s *Server) view(i *instance.Instance) instanceView {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, run := s.running[i.ID]
	return instanceView{Instance: i, Busy: s.busy[i.ID], Running: run, Mods: len(i.Mods())}
}

func (s *Server) routes() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /api/events", s.events)

	m.HandleFunc("GET /api/instances", func(w http.ResponseWriter, r *http.Request) {
		out := []instanceView{}
		for _, i := range instance.List() {
			out = append(out, s.view(i))
		}
		writeJSON(w, out)
	})
	m.HandleFunc("GET /api/instances/{id}", func(w http.ResponseWriter, r *http.Request) {
		if i := s.inst(w, r); i != nil {
			writeJSON(w, s.view(i))
		}
	})
	m.HandleFunc("POST /api/instances", func(w http.ResponseWriter, r *http.Request) {
		var b struct {
			Name, Game, Loader, LoaderVersion string
			PerfMods                          bool
		}
		if err := body(r, &b); err != nil || b.Name == "" || b.Game == "" {
			fail(w, errors.New("name and version are required"), 400)
			return
		}
		i := instance.New(b.Name, b.Game, b.Loader, b.LoaderVersion)
		if err := i.Save(); err != nil {
			fail(w, err, 500)
			return
		}
		if b.PerfMods && b.Loader == "fabric" {
			go s.task(i.ID, "Adding performance mods", func(rep *reporter) error {
				return modrinth.InstallPerformanceMods(context.Background(), i, rep.Step)
			})
		}
		writeJSON(w, s.view(i))
	})
	m.HandleFunc("PATCH /api/instances/{id}", func(w http.ResponseWriter, r *http.Request) {
		i := s.inst(w, r)
		if i == nil {
			return
		}
		// Only settings change; identity and history stay.
		var b map[string]json.RawMessage
		if err := body(r, &b); err != nil {
			fail(w, err, 400)
			return
		}
		for _, k := range []string{"id", "versionId", "created", "lastPlayed", "playSeconds", "importedFrom"} {
			delete(b, k)
		}
		if _, ok := b["game"]; ok {
			i.VersionID = "" // reinstall for the new version
		}
		if _, ok := b["loaderVersion"]; ok {
			i.VersionID = ""
		}
		raw, _ := json.Marshal(b)
		if err := json.Unmarshal(raw, i); err != nil {
			fail(w, err, 400)
			return
		}
		if err := i.Save(); err != nil {
			fail(w, err, 500)
			return
		}
		writeJSON(w, s.view(i))
	})
	m.HandleFunc("DELETE /api/instances/{id}", func(w http.ResponseWriter, r *http.Request) {
		if i := s.inst(w, r); i != nil {
			if err := i.Delete(); err != nil {
				fail(w, err, 500)
				return
			}
			writeJSON(w, true)
		}
	})
	m.HandleFunc("POST /api/instances/{id}/launch", s.launchHandler)
	m.HandleFunc("GET /api/instances/{id}/cover", func(w http.ResponseWriter, r *http.Request) {
		// The newest screenshot, else the newest world's icon.
		i := s.inst(w, r)
		if i == nil {
			return
		}
		best, when := "", time.Time{}
		pick := func(glob string) {
			ms, _ := filepath.Glob(filepath.Join(i.GameDir(), glob))
			for _, p := range ms {
				if st, err := os.Stat(p); err == nil && st.ModTime().After(when) {
					best, when = p, st.ModTime()
				}
			}
		}
		pick("screenshots/*.png")
		if best == "" {
			pick("saves/*/icon.png")
		}
		if best == "" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFile(w, r, best)
	})
	m.HandleFunc("GET /api/instances/{id}/log", func(w http.ResponseWriter, r *http.Request) {
		i := s.inst(w, r)
		if i == nil {
			return
		}
		b, _ := os.ReadFile(launch.LogPath(i))
		if len(b) > 512<<10 {
			b = b[len(b)-512<<10:]
		}
		crash := ""
		reports, _ := filepath.Glob(filepath.Join(i.GameDir(), "crash-reports", "*.txt"))
		var newest time.Time
		for _, p := range reports {
			if st, err := os.Stat(p); err == nil && st.ModTime().After(newest) && st.ModTime().After(i.LastPlayed) {
				newest, crash = st.ModTime(), p
			}
		}
		out := map[string]any{"log": string(b)}
		if crash != "" {
			cb, _ := os.ReadFile(crash)
			out["crash"] = string(cb)
			out["crashFile"] = filepath.Base(crash)
			out["suspect"] = suspectMod(string(cb), i)
		}
		writeJSON(w, out)
	})
	m.HandleFunc("GET /api/instances/{id}/mods", func(w http.ResponseWriter, r *http.Request) {
		i := s.inst(w, r)
		if i == nil {
			return
		}
		mods, err := modrinth.Identify(r.Context(), i)
		if err != nil {
			fail(w, err, 500)
			return
		}
		if mods == nil {
			mods = []instance.ModFile{}
		}
		writeJSON(w, mods)
	})
	m.HandleFunc("POST /api/instances/{id}/mods", func(w http.ResponseWriter, r *http.Request) {
		i := s.inst(w, r)
		if i == nil {
			return
		}
		var b struct{ Project string }
		if err := body(r, &b); err != nil || b.Project == "" {
			fail(w, errors.New("project required"), 400)
			return
		}
		go s.task(i.ID, "Installing mods", func(rep *reporter) error {
			return modrinth.Install(context.Background(), i, b.Project, rep.Step)
		})
		writeJSON(w, true)
	})
	m.HandleFunc("POST /api/instances/{id}/mods/toggle", func(w http.ResponseWriter, r *http.Request) {
		i := s.inst(w, r)
		if i == nil {
			return
		}
		var b struct{ File string }
		body(r, &b)
		name, err := i.ToggleMod(b.File)
		if err != nil {
			fail(w, err, 400)
			return
		}
		writeJSON(w, name)
	})
	m.HandleFunc("POST /api/instances/{id}/mods/remove", func(w http.ResponseWriter, r *http.Request) {
		i := s.inst(w, r)
		if i == nil {
			return
		}
		var b struct{ File string }
		body(r, &b)
		if err := i.RemoveMod(b.File); err != nil {
			fail(w, err, 400)
			return
		}
		writeJSON(w, true)
	})
	m.HandleFunc("POST /api/instances/{id}/mods/update", func(w http.ResponseWriter, r *http.Request) {
		i := s.inst(w, r)
		if i == nil {
			return
		}
		go s.task(i.ID, "Updating mods", func(rep *reporter) error {
			n, err := modrinth.UpdateAll(context.Background(), i, rep.Step)
			rep.Step(fmt.Sprintf("%d updated", n))
			return err
		})
		writeJSON(w, true)
	})
	m.HandleFunc("POST /api/instances/{id}/perf", func(w http.ResponseWriter, r *http.Request) {
		i := s.inst(w, r)
		if i == nil {
			return
		}
		go s.task(i.ID, "Adding performance mods", func(rep *reporter) error {
			return modrinth.InstallPerformanceMods(context.Background(), i, rep.Step)
		})
		writeJSON(w, true)
	})
	m.HandleFunc("GET /api/instances/{id}/worlds", func(w http.ResponseWriter, r *http.Request) {
		if i := s.inst(w, r); i != nil {
			ws := i.Worlds()
			if ws == nil {
				ws = []instance.World{}
			}
			writeJSON(w, ws)
		}
	})
	m.HandleFunc("POST /api/instances/{id}/worlds/backup", func(w http.ResponseWriter, r *http.Request) {
		i := s.inst(w, r)
		if i == nil {
			return
		}
		var b struct{ Name string }
		body(r, &b)
		p, err := i.BackupWorld(b.Name)
		if err != nil {
			fail(w, err, 400)
			return
		}
		writeJSON(w, p)
	})
	m.HandleFunc("POST /api/instances/{id}/export", func(w http.ResponseWriter, r *http.Request) {
		i := s.inst(w, r)
		if i == nil {
			return
		}
		home, _ := os.UserHomeDir()
		out := filepath.Join(home, i.ID+".mrpack")
		if err := modrinth.ExportPack(r.Context(), i, out); err != nil {
			fail(w, err, 500)
			return
		}
		writeJSON(w, out)
	})

	m.HandleFunc("GET /api/versions", func(w http.ResponseWriter, r *http.Request) {
		man, err := mojang.GetManifest(r.Context())
		if err != nil {
			fail(w, err, 502)
			return
		}
		type v struct {
			ID, Type string
			Date     string `json:"date"`
		}
		out := []v{}
		for _, x := range man.Versions {
			if x.Type == "release" || (r.URL.Query().Get("snapshots") == "1" && x.Type == "snapshot") {
				out = append(out, v{x.ID, x.Type, x.ReleaseTime.Format("2006-01-02")})
			}
		}
		writeJSON(w, out)
	})
	m.HandleFunc("GET /api/loaders/{loader}/{game}", func(w http.ResponseWriter, r *http.Request) {
		vs, err := loaders.Versions(r.Context(), r.PathValue("loader"), r.PathValue("game"))
		if err != nil {
			fail(w, err, 404)
			return
		}
		if len(vs) > 40 {
			vs = vs[:40]
		}
		writeJSON(w, vs)
	})
	m.HandleFunc("GET /api/modrinth/search", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		var game, loader string
		if id := q.Get("instance"); id != "" {
			if i, err := instance.Load(id); err == nil {
				game, loader = i.Game, i.Loader
			}
		}
		ptype := q.Get("type")
		if ptype == "" {
			ptype = "mod"
		}
		var off int
		fmt.Sscan(q.Get("offset"), &off)
		res, err := modrinth.Search(r.Context(), q.Get("q"), ptype, game, loader, off)
		if err != nil {
			fail(w, err, 502)
			return
		}
		writeJSON(w, res)
	})
	m.HandleFunc("POST /api/modrinth/modpack", func(w http.ResponseWriter, r *http.Request) {
		// Install a modpack from Modrinth: its newest version's .mrpack.
		var b struct{ Project, Title, Icon string }
		if err := body(r, &b); err != nil || b.Project == "" {
			fail(w, errors.New("project required"), 400)
			return
		}
		go s.task("", "Installing "+b.Title, func(rep *reporter) error {
			var vs []modrinth.Version
			if err := dl("https://api.modrinth.com/v2/project/"+b.Project+"/version", &vs); err != nil || len(vs) == 0 {
				return fmt.Errorf("no versions for %s", b.Title)
			}
			f := vs[0].PrimaryFile()
			tmp := filepath.Join(paths.Cache(), b.Project+".mrpack")
			if err := download(f.URL, tmp); err != nil {
				return err
			}
			i, err := modrinth.ImportPack(context.Background(), tmp, b.Title, rep.Step)
			if i != nil && b.Icon != "" {
				i.Icon = b.Icon
				i.Save()
			}
			return err
		})
		writeJSON(w, true)
	})
	m.HandleFunc("POST /api/import/mrpack", func(w http.ResponseWriter, r *http.Request) {
		tmp, err := os.CreateTemp(paths.Cache(), "upload-*.mrpack")
		if err != nil {
			os.MkdirAll(paths.Cache(), 0o755)
			if tmp, err = os.CreateTemp(paths.Cache(), "upload-*.mrpack"); err != nil {
				fail(w, err, 500)
				return
			}
		}
		io.Copy(tmp, io.LimitReader(r.Body, 512<<20))
		tmp.Close()
		go s.task("", "Importing modpack", func(rep *reporter) error {
			defer os.Remove(tmp.Name())
			_, err := modrinth.ImportPack(context.Background(), tmp.Name(), r.URL.Query().Get("name"), rep.Step)
			return err
		})
		writeJSON(w, true)
	})
	m.HandleFunc("GET /api/import/prism", func(w http.ResponseWriter, r *http.Request) {
		dir := prism.DefaultDir()
		entries, _ := os.ReadDir(dir)
		names := []string{}
		for _, e := range entries {
			if _, err := os.Stat(filepath.Join(dir, e.Name(), "instance.cfg")); err == nil {
				names = append(names, e.Name())
			}
		}
		writeJSON(w, map[string]any{"dir": dir, "instances": names})
	})
	m.HandleFunc("POST /api/import/prism", func(w http.ResponseWriter, r *http.Request) {
		var b struct{ Dir string }
		body(r, &b)
		go s.task("", "Importing from Prism", func(rep *reporter) error {
			_, err := prism.ImportAll(b.Dir, rep.Step)
			return err
		})
		writeJSON(w, true)
	})

	m.HandleFunc("GET /api/accounts", func(w http.ResponseWriter, r *http.Request) {
		out := []map[string]any{}
		for _, a := range auth.List() {
			out = append(out, map[string]any{"id": a.ID, "name": a.Name, "type": a.Type, "default": a.Default, "skin": a.Skin})
		}
		writeJSON(w, out)
	})
	m.HandleFunc("POST /api/accounts/offline", func(w http.ResponseWriter, r *http.Request) {
		var b struct{ Name string }
		body(r, &b)
		a, err := auth.Offline(b.Name)
		if err != nil {
			fail(w, err, 400)
			return
		}
		auth.Upsert(a)
		writeJSON(w, true)
	})
	m.HandleFunc("POST /api/accounts/{id}/default", func(w http.ResponseWriter, r *http.Request) {
		auth.SetDefault(r.PathValue("id"))
		writeJSON(w, true)
	})
	m.HandleFunc("DELETE /api/accounts/{id}", func(w http.ResponseWriter, r *http.Request) {
		auth.Remove(r.PathValue("id"))
		writeJSON(w, true)
	})
	m.HandleFunc("POST /api/accounts/microsoft", func(w http.ResponseWriter, r *http.Request) {
		set := LoadSettings()
		dc, err := auth.StartDeviceCode(r.Context(), set.MSAClientID)
		if err != nil {
			fail(w, err, 400)
			return
		}
		ctx, cancel := context.WithCancel(context.Background())
		flow := &msaFlow{Code: dc, Status: "waiting", cancel: cancel}
		s.mu.Lock()
		if s.msa != nil && s.msa.cancel != nil {
			s.msa.cancel()
		}
		s.msa = flow
		s.mu.Unlock()
		go func() {
			a, err := auth.PollDeviceCode(ctx, set.MSAClientID, dc)
			s.mu.Lock()
			if err != nil {
				flow.Status, flow.Error = "error", err.Error()
			} else {
				auth.Upsert(a)
				flow.Status, flow.Name = "done", a.Name
			}
			s.mu.Unlock()
			s.emit("accounts", nil)
		}()
		writeJSON(w, flow)
	})
	m.HandleFunc("GET /api/accounts/microsoft", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		writeJSON(w, s.msa)
	})

	m.HandleFunc("GET /api/settings", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"settings": LoadSettings(), "onAC": instance.OnAC(), "dataDir": paths.Root})
	})
	m.HandleFunc("PUT /api/settings", func(w http.ResponseWriter, r *http.Request) {
		var st Settings
		if err := body(r, &st); err != nil {
			fail(w, err, 400)
			return
		}
		if err := st.save(); err != nil {
			fail(w, err, 500)
			return
		}
		writeJSON(w, st)
	})

	m.Handle("/", s.static())
	return m
}

// static serves the interface; unknown paths get index.html (client-side routes).
func (s *Server) static() http.Handler {
	files := http.FileServerFS(s.web)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p != "" {
			if _, err := fs.Stat(s.web, p); err == nil {
				if strings.HasPrefix(p, "assets/") {
					w.Header().Set("Cache-Control", "max-age=31536000, immutable")
				}
				files.ServeHTTP(w, r)
				return
			}
		}
		b, err := fs.ReadFile(s.web, "index.html")
		if err != nil {
			http.Error(w, "the interface isn't built (cd web && npm run build)", 500)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(b)
	})
}

// task runs work for an instance in the background, reporting progress.
func (s *Server) task(id, title string, fn func(*reporter) error) {
	rep := &reporter{s: s, id: id}
	rep.Step(title)
	err := fn(rep)
	s.mu.Lock()
	delete(s.busy, id)
	s.mu.Unlock()
	if err != nil {
		s.emit("error", map[string]any{"instance": id, "message": err.Error()})
	}
	s.emit("done", map[string]any{"instance": id, "title": title})
}

func (s *Server) launchHandler(w http.ResponseWriter, r *http.Request) {
	i := s.inst(w, r)
	if i == nil {
		return
	}
	var b struct{ Account string }
	body(r, &b)
	acc, err := auth.Get(b.Account)
	if err != nil {
		fail(w, err, 400)
		return
	}
	s.mu.Lock()
	if s.busy[i.ID] != "" {
		s.mu.Unlock()
		fail(w, errors.New("already busy"), 409)
		return
	}
	s.busy[i.ID] = "Starting"
	s.mu.Unlock()
	go s.task(i.ID, "Preparing "+i.Name, func(rep *reporter) error {
		ctx := context.Background()
		acc, err := auth.Refresh(ctx, acc)
		if err != nil {
			return err
		}
		v, java, err := launch.Prepare(ctx, i, rep)
		if err != nil {
			return err
		}
		rep.Step("Starting the game")
		cmd, err := launch.Start(i, v, java, &launch.Options{Account: acc})
		if err != nil {
			return err
		}
		l := Launched{PID: cmd.Process.Pid, Instance: i.ID, Started: time.Now()}
		s.mu.Lock()
		s.running[i.ID] = l
		s.mu.Unlock()
		s.emit("launched", map[string]any{"instance": i.ID, "pid": l.PID})
		if LoadSettings().CloseOnLaunch && s.OnLaunch != nil {
			s.OnLaunch(l)
			return nil
		}
		// Window stays open: wait here.
		go func() {
			cmd.Wait()
			res := launch.Finish(i, l.Started, cmd.ProcessState.ExitCode())
			s.mu.Lock()
			delete(s.running, i.ID)
			s.mu.Unlock()
			s.emit("exited", map[string]any{"instance": i.ID, "code": res.ExitCode, "crash": res.Crash != ""})
		}()
		return nil
	})
	writeJSON(w, true)
}

// suspectMod names the mod most likely behind a crash: the first mod ID or
// jar name of the instance that appears in the report's stack trace.
func suspectMod(report string, i *instance.Instance) string {
	lower := strings.ToLower(report)
	for _, m := range i.Mods() {
		name := strings.ToLower(strings.TrimSuffix(strings.TrimSuffix(m.File, ".disabled"), ".jar"))
		base := strings.FieldsFunc(name, func(r rune) bool { return r == '-' || r == '_' || r == '+' || r == ' ' })
		if len(base) > 0 && len(base[0]) > 3 && base[0] != "fabric" && strings.Contains(lower, base[0]) {
			return m.File
		}
	}
	return ""
}

func dl(url string, v any) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	req.Header.Set("User-Agent", "r3clusionn/mclaunch/1.0")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(v)
}

func download(url, path string) error {
	os.MkdirAll(filepath.Dir(path), 0o755)
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, resp.Body)
	f.Close()
	return err
}
