// mclaunch: a Minecraft launcher. With no arguments it opens its window
// (appshell: CEF in an X11/EGL window); with a command it runs from the
// terminal (mclaunch help).
package main

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"syscall"

	"mclaunch/internal/appshell"
	"mclaunch/internal/cli"
	"mclaunch/internal/server"
)

//go:embed all:web/dist
var webFiles embed.FS

func init() {
	// CEF and X11 run on the main OS thread.
	runtime.LockOSThread()
}

func main() {
	args := os.Args[1:]
	page := ""
	if len(args) >= 2 && args[0] == "--show" {
		page, args = args[1], args[2:]
	}
	if ok, err := cli.Run(args); ok {
		if err != nil {
			fmt.Fprintln(os.Stderr, "mclaunch:", err)
			os.Exit(1)
		}
		return
	}
	os.Exit(window(page))
}

func window(page string) int {
	web, _ := fs.Sub(webFiles, "web/dist")
	srv := server.New(web)
	var mu sync.Mutex
	var game *server.Launched
	srv.OnLaunch = func(l server.Launched) {
		mu.Lock()
		game = &l
		mu.Unlock()
		// The interface and all its processes go; a supervisor waits for the game.
		appshell.Quit()
	}
	addr, err := srv.Listen()
	if err != nil {
		fmt.Fprintln(os.Stderr, "mclaunch:", err)
		return 1
	}
	exe, _ := os.Executable()
	dir := filepath.Dir(exe)
	home, _ := os.UserHomeDir()
	url := "http://" + addr + "/"
	if page != "" {
		url += "#" + page
	}
	code := appshell.Run(appshell.Options{
		URL: url, Title: "mclaunch", Class: "mclaunch",
		CacheDir:     filepath.Join(home, ".cache", "mclaunch", "cef"),
		DataDir:      filepath.Join(home, ".local", "share", "mclaunch", "window"),
		HelperPath:   filepath.Join(dir, "appshell-helper"),
		ResourcesDir: dir, LocalesDir: filepath.Join(dir, "locales"),
		Width: 1400, Height: 900,
	})
	mu.Lock()
	g := game
	mu.Unlock()
	if g != nil {
		// Replace this process (and the CEF libraries' memory) with the small
		// supervisor; the game stays our child across exec.
		err := syscall.Exec(filepath.Join(dir, "mclaunch-supervise"), []string{"mclaunch-supervise",
			strconv.Itoa(g.PID), g.Instance, strconv.FormatInt(g.Started.Unix(), 10)}, os.Environ())
		fmt.Fprintln(os.Stderr, "mclaunch: supervisor:", err)
	}
	return code
}
