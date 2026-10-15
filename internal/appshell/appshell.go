// Package appshell opens a standalone window for a web UI: CEF renders the
// page off-screen into an X11/EGL window. It is media-player's appshell
// (github.com/r3clusionn/media-player, internal/appshell) without the libmpv
// player (player_stub.c). See appshell.h for how the pieces fit together.
//
// Build requirements: the CEF headers and libcef.so under third_party/ (a
// symlink to ~/src/third_party, set up by build.sh), plus the system's X11,
// EGL, GLES and yyjson.
package appshell

/*
#cgo CFLAGS: -std=gnu11 -O2 -march=native -I${SRCDIR}/../../third_party/cef -I/usr/include/libdrm -Wall -Wno-unused-parameter -Wno-format-truncation
#cgo LDFLAGS: -L${SRCDIR}/../../third_party/runtime -lcef -lX11 -lXss -lXi -lXext -lEGL -lGLESv2 -lm
#include <stdlib.h>
#include "appshell.h"
*/
import "C"

import "unsafe"

// Options for Run. Paths are absolute.
type Options struct {
	URL          string
	Title        string
	Class        string // X11 window class, for dwm rules
	CacheDir     string // CEF profile and cache
	DataDir      string // settings, screenshots, logs
	HelperPath   string // appshell-helper
	ResourcesDir string // CEF .pak files and icudtl.dat
	LocalesDir   string
	Anime4KDir   string
	ServerPort   int
	Width        int
	Height       int
	Scale        float64 // 0: from Xft.dpi
}

// Run opens the window and returns when it is closed. It must be called on
// the main OS thread (runtime.LockOSThread in the main package's init).
// Quit closes the window; Run then returns. Safe from any goroutine.
func Quit() { C.appshell_request_quit() }

func Run(o Options) int {
	var frees []unsafe.Pointer
	cs := func(s string) *C.char {
		p := C.CString(s)
		frees = append(frees, unsafe.Pointer(p))
		return p
	}
	defer func() {
		for _, p := range frees {
			C.free(p)
		}
	}()
	copts := C.appshell_opts{
		url:           cs(o.URL),
		title:         cs(o.Title),
		wm_class:      cs(o.Class),
		cache_dir:     cs(o.CacheDir),
		data_dir:      cs(o.DataDir),
		helper_path:   cs(o.HelperPath),
		resources_dir: cs(o.ResourcesDir),
		locales_dir:   cs(o.LocalesDir),
		anime4k_dir:   cs(o.Anime4KDir),
		server_port:   C.int(o.ServerPort),
		width:         C.int(o.Width),
		height:        C.int(o.Height),
		scale:         C.double(o.Scale),
	}
	return int(C.appshell_run(&copts))
}
