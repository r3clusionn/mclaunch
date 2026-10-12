// mclaunch-supervise: waits for a game started by mclaunch's window (which
// exits when the game starts), records the session, restores the power
// profile, and reopens the window on the log if the game crashed. A separate
// static binary without CEF, so waiting costs a few megabytes.
//
// usage: mclaunch-supervise PID INSTANCE STARTED_UNIX
package main

import (
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"mclaunch/internal/instance"
	"mclaunch/internal/launch"
)

func main() {
	if len(os.Args) != 4 {
		os.Stderr.WriteString("usage: mclaunch-supervise PID INSTANCE STARTED_UNIX\n")
		os.Exit(2)
	}
	pid, _ := strconv.Atoi(os.Args[1])
	id := os.Args[2]
	sec, _ := strconv.ParseInt(os.Args[3], 10, 64)
	started := time.Unix(sec, 0)
	var ws syscall.WaitStatus
	code := 0
	if _, err := syscall.Wait4(pid, &ws, 0, nil); err == nil {
		code = ws.ExitStatus()
		if ws.Signaled() {
			code = 128 + int(ws.Signal())
		}
	} else {
		// Not our child (started some other way): poll until it's gone.
		for syscall.Kill(pid, 0) == nil {
			time.Sleep(2 * time.Second)
		}
	}
	inst, err := instance.Load(id)
	if err != nil {
		return
	}
	res := launch.Finish(inst, started, code)
	if res.Crash != "" || (code != 0 && code != 130 && code != 143) {
		exe, _ := os.Executable()
		win := filepath.Join(filepath.Dir(exe), "mclaunch")
		syscall.Exec(win, []string{"mclaunch", "--show", "instance/" + id + "/log"}, os.Environ())
	}
}
