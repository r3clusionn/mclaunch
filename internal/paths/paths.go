// Package paths: where mclaunch keeps its files.
//
//	~/.local/share/mclaunch/
//	  versions/<id>/<id>.json, <id>.jar   game versions (shared)
//	  libraries/                          Maven layout (shared)
//	  assets/indexes, assets/objects      game assets (shared)
//	  java/<component>/                   Mojang's Java runtimes
//	  instances/<id>/                     instance.json and minecraft/ (the game directory)
//	  accounts.json                       0600
//	  settings.json
package paths

import (
	"os"
	"path/filepath"
)

var Root = func() string {
	if d := os.Getenv("MCLAUNCH_DATA"); d != "" {
		return d
	}
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return filepath.Join(d, "mclaunch")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "mclaunch")
}()

func Versions() string  { return filepath.Join(Root, "versions") }
func Libraries() string { return filepath.Join(Root, "libraries") }
func Assets() string    { return filepath.Join(Root, "assets") }
func Java() string      { return filepath.Join(Root, "java") }
func Instances() string { return filepath.Join(Root, "instances") }
func Cache() string     { return filepath.Join(Root, "cache") }

func VersionJSON(id string) string { return filepath.Join(Versions(), id, id+".json") }
func VersionJar(id string) string  { return filepath.Join(Versions(), id, id+".jar") }
