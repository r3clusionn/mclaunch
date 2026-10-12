// Package dl downloads many files at once: 16 at a time, SHA1-checked,
// written to a temporary name and renamed, with retries.
package dl

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

var Client = &http.Client{Timeout: 10 * time.Minute}

const UserAgent = "mclaunch/1.0 (github.com/r3clusionn/mclaunch)"

// Task is one file to have on disk.
type Task struct {
	URL  string
	Path string
	SHA1 string // hex; empty: not checked
	Size int64  // 0: unknown
	Mode os.FileMode
}

// Progress is called from worker goroutines with bytes done/total and files done/total.
type Progress func(doneBytes, totalBytes int64, doneFiles, totalFiles int)

// Have reports whether the file is already there. With a known size the
// size is enough (hashing every asset on each launch would take seconds);
// without one the hash decides.
func Have(t Task) bool {
	st, err := os.Stat(t.Path)
	if err != nil || st.IsDir() {
		return false
	}
	if t.Size > 0 {
		return st.Size() == t.Size
	}
	if t.SHA1 != "" {
		sum, err := FileSHA1(t.Path)
		return err == nil && sum == t.SHA1
	}
	return true
}

func FileSHA1(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha1.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func fetch(ctx context.Context, t Task, add func(int64)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.URL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", UserAgent)
	resp, err := Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", t.URL, resp.Status)
	}
	if err := os.MkdirAll(filepath.Dir(t.Path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(t.Path), ".dl-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	h := sha1.New()
	buf := make([]byte, 256<<10)
	var n int64
	for {
		k, rerr := resp.Body.Read(buf)
		if k > 0 {
			if _, werr := tmp.Write(buf[:k]); werr != nil {
				tmp.Close()
				return werr
			}
			h.Write(buf[:k])
			n += int64(k)
			add(int64(k))
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			tmp.Close()
			add(-n)
			return rerr
		}
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if t.SHA1 != "" {
		if got := hex.EncodeToString(h.Sum(nil)); got != t.SHA1 {
			add(-n)
			return fmt.Errorf("%s: SHA1 %s, expected %s", t.URL, got, t.SHA1)
		}
	}
	mode := t.Mode
	if mode == 0 {
		mode = 0o644
	}
	os.Chmod(tmp.Name(), mode)
	return os.Rename(tmp.Name(), t.Path)
}

// All downloads the tasks that aren't already on disk.
func All(ctx context.Context, tasks []Task, progress Progress) error {
	var todo []Task
	var total int64
	seen := map[string]bool{}
	for _, t := range tasks {
		if seen[t.Path] || Have(t) {
			continue
		}
		seen[t.Path] = true
		todo = append(todo, t)
		total += t.Size
	}
	if len(todo) == 0 {
		return nil
	}
	var done int64
	var files int32
	report := func() {
		if progress != nil {
			progress(atomic.LoadInt64(&done), total, int(atomic.LoadInt32(&files)), len(todo))
		}
	}
	ch := make(chan Task)
	errs := make(chan error, len(todo))
	var wg sync.WaitGroup
	for w := 0; w < 16; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for t := range ch {
				var err error
				for attempt := 0; attempt < 3; attempt++ {
					if err = fetch(ctx, t, func(n int64) { atomic.AddInt64(&done, n); report() }); err == nil {
						break
					}
					if ctx.Err() != nil {
						break
					}
					time.Sleep(time.Duration(attempt+1) * 500 * time.Millisecond)
				}
				if err != nil {
					errs <- err
				}
				atomic.AddInt32(&files, 1)
				report()
			}
		}()
	}
	for _, t := range todo {
		select {
		case ch <- t:
		case <-ctx.Done():
		}
	}
	close(ch)
	wg.Wait()
	close(errs)
	var all []error
	for err := range errs {
		all = append(all, err)
	}
	return errors.Join(all...)
}

// JSON fetches url and decodes it into v.
func JSON(ctx context.Context, url string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", UserAgent)
	resp, err := Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("%s: %s %s", url, resp.Status, body)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

// CachedJSON is JSON with a copy on disk, used when the network is down or
// the copy is younger than maxAge.
func CachedJSON(ctx context.Context, url, path string, maxAge time.Duration, v any) error {
	if st, err := os.Stat(path); err == nil && time.Since(st.ModTime()) < maxAge {
		if b, err := os.ReadFile(path); err == nil && json.Unmarshal(b, v) == nil {
			return nil
		}
	}
	var raw json.RawMessage
	if err := JSON(ctx, url, &raw); err != nil {
		if b, rerr := os.ReadFile(path); rerr == nil && json.Unmarshal(b, v) == nil {
			return nil // offline: the old copy
		}
		return err
	}
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, raw, 0o644)
	return json.Unmarshal(raw, v)
}
