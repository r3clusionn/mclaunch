// Package cli: mclaunch's command line, for scripts and testing without the window.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"mclaunch/internal/auth"
	"mclaunch/internal/instance"
	"mclaunch/internal/launch"
	"mclaunch/internal/loaders"
	"mclaunch/internal/modrinth"
	"mclaunch/internal/mojang"
	"mclaunch/internal/prism"
)

const Usage = `usage: mclaunch [command]
  (no command)                     open the window
  versions [-snapshots]            list Minecraft versions
  list                             list instances
  create NAME -game V [-loader L] [-loader-version V] [-perf]
                                   L: vanilla fabric quilt neoforge forge; -perf adds the
                                   performance mods (Fabric)
  prepare NAME                     download and install everything for NAME
  launch NAME [-account A] [-quickplay WORLD] [-gc-log FILE] [-detach]
  account offline NAME | account list | account remove ID | account default ID
  mods search QUERY -instance NAME | mods install NAME PROJECT... | mods list NAME | mods update NAME
  import-prism [DIR]               import Prism Launcher instances (default: its data dir)
  import-mrpack FILE [-name NAME]  import a Modrinth modpack
  export-mrpack NAME FILE          export an instance as a Modrinth modpack
`

type term struct{ last time.Time }

func (t *term) Step(msg string) { fmt.Println("==", msg) }

func (t *term) Bytes(done, total int64, files, totalFiles int) {
	if time.Since(t.last) < 500*time.Millisecond && files < totalFiles {
		return
	}
	t.last = time.Now()
	if total > 0 {
		fmt.Printf("   %d/%d files, %.1f/%.1f MB\n", files, totalFiles, float64(done)/1e6, float64(total)/1e6)
	} else {
		fmt.Printf("   %d/%d files\n", files, totalFiles)
	}
}

func find(name string) (*instance.Instance, error) {
	for _, i := range instance.List() {
		if i.ID == name || strings.EqualFold(i.Name, name) {
			return i, nil
		}
	}
	return nil, fmt.Errorf("no instance %q (mclaunch list)", name)
}

// Run executes a command; it returns false if args isn't a command (open the window).
func Run(args []string) (bool, error) {
	if len(args) == 0 {
		return false, nil
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "help", "-h", "--help":
		fmt.Print(Usage)
	case "versions":
		fs := flag.NewFlagSet(cmd, flag.ExitOnError)
		snaps := fs.Bool("snapshots", false, "include snapshots")
		fs.Parse(rest)
		m, err := mojang.GetManifest(ctx)
		if err != nil {
			return true, err
		}
		for _, v := range m.Versions {
			if v.Type == "release" || (*snaps && v.Type == "snapshot") {
				fmt.Printf("%-24s %s %s\n", v.ID, v.Type, v.ReleaseTime.Format("2006-01-02"))
			}
		}
	case "list":
		for _, i := range instance.List() {
			l := i.Loader
			if i.LoaderVersion != "" {
				l += " " + i.LoaderVersion
			}
			fmt.Printf("%-24s %-10s %-24s %d MB  played %s\n", i.ID, i.Game, l, i.MemoryMB,
				time.Duration(i.PlaySeconds)*time.Second)
		}
	case "create":
		if len(rest) < 1 {
			return true, errors.New("create NAME -game VERSION")
		}
		fs := flag.NewFlagSet(cmd, flag.ExitOnError)
		game := fs.String("game", "", "Minecraft version (default: latest release)")
		loader := fs.String("loader", "vanilla", "vanilla, fabric, quilt, neoforge, forge")
		lv := fs.String("loader-version", "", "loader version (default: recommended)")
		perf := fs.Bool("perf", false, "add the performance mods")
		fs.Parse(rest[1:])
		if *game == "" {
			m, err := mojang.GetManifest(ctx)
			if err != nil {
				return true, err
			}
			*game = m.Latest.Release
		}
		inst := instance.New(rest[0], *game, *loader, *lv)
		if err := inst.Save(); err != nil {
			return true, err
		}
		fmt.Println("created", inst.ID)
		if *perf {
			if err := modrinth.InstallPerformanceMods(ctx, inst, func(s string) { fmt.Println("  ", s) }); err != nil {
				return true, err
			}
		}
	case "prepare":
		if len(rest) < 1 {
			return true, errors.New("prepare NAME")
		}
		inst, err := find(rest[0])
		if err != nil {
			return true, err
		}
		_, java, err := launch.Prepare(ctx, inst, &term{})
		if err == nil {
			fmt.Println("ready:", inst.VersionID, "with", java)
		}
		return true, err
	case "launch":
		if len(rest) < 1 {
			return true, errors.New("launch NAME")
		}
		fs := flag.NewFlagSet(cmd, flag.ExitOnError)
		accName := fs.String("account", "", "account name or ID (default account)")
		quick := fs.String("quickplay", "", "load straight into this singleplayer world")
		gclog := fs.String("gc-log", "", "write the JVM's GC log here")
		detach := fs.Bool("detach", false, "don't wait for the game")
		fs.Parse(rest[1:])
		inst, err := find(rest[0])
		if err != nil {
			return true, err
		}
		acc, err := auth.Get(*accName)
		if err != nil {
			return true, err
		}
		if acc, err = auth.Refresh(ctx, acc); err != nil {
			return true, err
		}
		v, java, err := launch.Prepare(ctx, inst, &term{})
		if err != nil {
			return true, err
		}
		started := time.Now()
		c, err := launch.Start(inst, v, java, &launch.Options{Account: acc, QuickPlay: *quick, GCLog: *gclog})
		if err != nil {
			return true, err
		}
		fmt.Printf("started %s (pid %d), log: %s\n", inst.Name, c.Process.Pid, launch.LogPath(inst))
		if *detach {
			return true, nil
		}
		err = c.Wait()
		code := c.ProcessState.ExitCode()
		res := launch.Finish(inst, started, code)
		fmt.Printf("exited %d after %ds\n", code, res.Seconds)
		if res.Crash != "" {
			fmt.Println("crash report:", res.Crash)
		}
	case "account":
		if len(rest) < 1 {
			return true, errors.New("account offline NAME | list | remove ID | default ID")
		}
		switch rest[0] {
		case "offline":
			if len(rest) < 2 {
				return true, errors.New("account offline NAME")
			}
			a, err := auth.Offline(rest[1])
			if err != nil {
				return true, err
			}
			fmt.Println("added offline account", a.Name, a.ID)
			return true, auth.Upsert(a)
		case "list":
			for _, a := range auth.List() {
				d := ""
				if a.Default {
					d = " (default)"
				}
				fmt.Printf("%s  %-16s %s%s\n", a.ID, a.Name, a.Type, d)
			}
		case "remove":
			return true, auth.Remove(rest[1])
		case "default":
			return true, auth.SetDefault(rest[1])
		}
	case "mods":
		return true, modsCommand(ctx, rest)
	case "import-prism":
		dir := ""
		if len(rest) > 0 {
			dir = rest[0]
		}
		insts, err := prism.ImportAll(dir, func(s string) { fmt.Println("  ", s) })
		for _, i := range insts {
			fmt.Println("imported", i.ID, i.Game, i.Loader, i.LoaderVersion)
		}
		return true, err
	case "import-mrpack":
		if len(rest) < 1 {
			return true, errors.New("import-mrpack FILE")
		}
		fs := flag.NewFlagSet(cmd, flag.ExitOnError)
		name := fs.String("name", "", "instance name (default: the pack's)")
		fs.Parse(rest[1:])
		inst, err := modrinth.ImportPack(ctx, rest[0], *name, func(s string) { fmt.Println("  ", s) })
		if err == nil {
			fmt.Println("imported", inst.ID)
		}
		return true, err
	case "export-mrpack":
		if len(rest) < 2 {
			return true, errors.New("export-mrpack NAME FILE")
		}
		inst, err := find(rest[0])
		if err != nil {
			return true, err
		}
		return true, modrinth.ExportPack(ctx, inst, rest[1])
	case "loaders":
		if len(rest) < 2 {
			return true, errors.New("loaders LOADER GAME")
		}
		vs, err := loaders.Versions(ctx, rest[0], rest[1])
		for _, v := range vs {
			fmt.Println(v.Version, map[bool]string{true: "stable", false: ""}[v.Stable])
		}
		return true, err
	default:
		return false, nil
	}
	return true, nil
}

func modsCommand(ctx context.Context, args []string) error {
	if len(args) < 2 {
		return errors.New("mods search QUERY -instance NAME | install NAME PROJECT... | list NAME | update NAME")
	}
	switch args[0] {
	case "search":
		fs := flag.NewFlagSet("search", flag.ExitOnError)
		in := fs.String("instance", "", "instance (filters by its version and loader)")
		fs.Parse(args[2:])
		var game, loader string
		if *in != "" {
			inst, err := find(*in)
			if err != nil {
				return err
			}
			game, loader = inst.Game, inst.Loader
		}
		res, err := modrinth.Search(ctx, args[1], "mod", game, loader, 0)
		if err != nil {
			return err
		}
		for _, h := range res.Hits {
			fmt.Printf("%-28s %-10s %9d downloads  %s\n", h.Slug, h.ProjectID, h.Downloads, h.Title)
		}
	case "install":
		inst, err := find(args[1])
		if err != nil {
			return err
		}
		for _, p := range args[2:] {
			if err := modrinth.Install(ctx, inst, p, func(s string) { fmt.Println("  ", s) }); err != nil {
				return err
			}
		}
	case "list":
		inst, err := find(args[1])
		if err != nil {
			return err
		}
		mods, err := modrinth.Identify(ctx, inst)
		if err != nil {
			return err
		}
		for _, m := range mods {
			state := "on "
			if !m.Enabled {
				state = "off"
			}
			fmt.Printf("%s %-40s %s %s\n", state, m.File, m.Title, m.VersionN)
		}
	case "update":
		inst, err := find(args[1])
		if err != nil {
			return err
		}
		n, err := modrinth.UpdateAll(ctx, inst, func(s string) { fmt.Println("  ", s) })
		fmt.Println(n, "updated")
		return err
	}
	return nil
}
