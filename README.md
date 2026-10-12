# mclaunch

A Minecraft launcher for Linux in Go: instances, Fabric, Quilt, NeoForge and Forge, Modrinth mods and modpacks, imports of Prism Launcher instances, offline and Microsoft accounts. It downloads Mojang's own Java runtimes, so nothing has to be installed system-wide. It is made for one laptop (Ryzen AI 9 HX 475, Radeon 890M, 2880x1800 at 120 Hz, Gentoo with dwm). Its interface uses Seanime's design, in the same standalone window as [media-player](https://github.com/r3clusionn/media-player), and closes completely when the game starts. It sets Java, Mesa, the frame cap and the power profile for the hardware and the power source.

**Status:** working, version 1.0. Vanilla, Fabric, Quilt, NeoForge and Forge instances, a 244-mod NeoForge modpack and an imported Prism instance were all launched on the target laptop (see Verification). Microsoft sign-in uses the official Minecraft launcher's client ID (see Limits). The code is issued and the sign-in page works; finishing a sign-in hasn't been tested yet.

![The mclaunch window at 2880x1800 in Seanime's dark design: an icon sidebar on the left; a hero banner showing a Minecraft screenshot behind "Continue playing: My Fabric World" with badges for Fabric 0.19.5, Minecraft 26.3 and 2 mods and a violet Play button; below it the Instances grid with cards for each instance, each showing a screenshot from that instance's game, its loader badge, version, mod count and play time](docs/images/library.png)

## Features

- **Instances:** any release or snapshot, with Fabric, Quilt, NeoForge or Forge. Each has its own memory, garbage collector, JVM arguments, Java, window size and frame caps. Covers come from the instance's own screenshots or world icons.
- **Shared files:** libraries, assets and Java are downloaded once for all instances, 16 at a time, checked by SHA1.
- **Java without packages:** the runtime each version asks for comes from Mojang's Java manifest, as in the official launcher. A custom Java can be set per instance.
- **Mods from Modrinth:**
  - search filtered by the instance's version and loader;
  - installs with required dependencies;
  - update all, enable and disable, remove;
  - existing jars identified by hash.
- **Modpacks:** Modrinth modpacks installed from Browse or from a `.mrpack` file, and any instance exported as a `.mrpack`.
- **One click for performance** on Fabric: Sodium, Lithium, FerriteCore, ModernFix, ImmediatelyFast and Entity Culling, skipping any without a build for the version.
- **Prism Launcher import:** its instances come over with their version, loader, memory, JVM arguments, mods and worlds.
- **Worlds:** listed with size and last play, zip backups.
- **Logs and crashes:** a live log with search. After a crash the window reopens on the log, with the crash report and the mod most likely behind it.
- **Out of the way while you play:** the window and its browser processes exit when the game starts. A 4.5 MB supervisor waits for the game, records the play time, restores the power profile, and brings the window back only after a crash.
- **A command line** for everything above (`mclaunch help`).

## How to install

Requires Go 1.27, Node.js 26 with npm, gcc, yyjson, and the X11 libraries with EGL/GLES. Linux with X11 only. The window is media-player's appshell (vendored in `internal/appshell`, without the video player), so the build needs the same `~/src/third_party` (or `$THIRD_PARTY`): `cef/` (CEF 154.0.34 minimal) and `runtime/` (NSS, NSPR, xkbcommon).

```sh
git clone https://github.com/r3clusionn/mclaunch
cd mclaunch
./build.sh --install
```

This builds the interface, the helper, the supervisor and the program into `build/dist`, then copies them to `~/.local/opt/mclaunch` (377 MB, CEF stripped of its debug info) and links `~/.local/bin/mclaunch`.

**Power plan while playing (optional, root):** with the `powerprofile` script from [scripts](https://github.com/r3clusionn/scripts) installed, mclaunch asks for the gaming plan at launch and restores the previous one after. A plan picked by hand stays as it is. Without the script it skips this silently.

## How to use

```sh
mclaunch                 # the window
```

1. **Accounts:** sign in with Microsoft (a code to enter at microsoft.com/link on any device), or add an offline account (a name) for singleplayer and LAN.
2. **New instance:** pick the version and loader. The recommended loader version is preselected, and Fabric offers the performance mods.
3. **Play:** the first launch downloads Java, the libraries and the assets (about 700 MB for 26.3, 45 s here), then the window closes and the game starts.
4. **Mods:** Browse > Mods for the selected instance, or the instance's Mods tab. **Modpacks:** Browse > Modpacks makes a new instance.
5. **Import:** Settings > Prism Launcher, or a `.mrpack` file.

![An instance page: a Minecraft screenshot as the banner behind "Test Fabric" with Fabric 0.19.5, Minecraft 26.3 and "4 GB · ZGC" badges and a Play button; the Mods tab lists Entity Culling, Fabric API, FerriteCore, ImmediatelyFast, Lithium, Mod Menu, Text Placeholder API and Sodium with their icons, versions, file names, on/off switches and delete buttons, under Add mods, Update all and Performance mods buttons](docs/images/instance.png)

The command line does the same without the window:

```sh
mclaunch account microsoft               # prints a code for microsoft.com/link
mclaunch account offline Steve
mclaunch create "Survival" -game 26.3 -loader fabric -perf
mclaunch launch survival                  # waits for the game; -detach to return
mclaunch mods search iris -instance survival
mclaunch mods install survival iris
mclaunch import-prism                     # from ~/.local/share/PrismLauncher/instances
mclaunch import-mrpack pack.mrpack
```

## How it works

- **Versions:** Mojang's version manifest gives each version's JSON. A loader's JSON inherits from it, and mclaunch merges them: a loader's library replaces the vanilla one with the same group and artifact. Library and argument rules are evaluated for Linux on x86-64.
- **Loaders:**
  - Fabric and Quilt profiles come from their meta servers.
  - NeoForge and Forge run their official installers headless (`--installClient`) with Mojang's Java, into the shared folder, which they treat as `.minecraft`.
  - NeoForge versions follow the game's (1.21.1 → 21.1.x, 26.1.2 → 26.1.2.x).
- **Launching:**
  - The game jar is linked under the launched version's name, as the official launcher does: Forge and NeoForge ignore `<version>.jar` by name.
  - Natives of older versions are extracted per launch. LWJGL's own natives unpack into the instance, not `/tmp`, which is RAM here.
  - The game starts in its own session, with its output in `logs/launch.log`.
- **Java flags:** a fixed heap (`-Xms` = `-Xmx`: 4 GB, 6 GB for NeoForge, Forge and big packs), `AlwaysPreTouch` and transparent huge pages. The collector is generational ZGC by default; G1 is an option (see Measurements).
- **Mesa:** `mesa_glthread=true` and a 4 GB shader cache.
- **Frame cap by power source:** `maxFps` in the instance's `options.txt` before each launch, 120 plugged in (the panel's rate) and 60 on battery.
- **The window** is CEF rendering the React interface off-screen into an X11/EGL window (appshell). The Go program serves the interface and a JSON API on a random localhost port, with progress as server-sent events.
- **When the game starts** the window closes, and the process replaces itself (`exec`) with `mclaunch-supervise`, a static Go binary without CEF. The game stays its child, so it waits with `wait4`.
- **Accounts:** offline accounts use the game's own offline UUID (MD5 of `OfflinePlayer:<name>`). Microsoft accounts use a device code with the official launcher's client ID (`00000000402b5328`) at login.live.com, or an own Azure app ID at Azure AD. Then come Xbox Live, XSTS and Minecraft services. Tokens refresh before launching and are stored in `accounts.json` with mode 0600.

## Measurements

On the target laptop.

| | |
|---|---|
| First download for Minecraft 26.3 (Java, libraries, assets, client: ~700 MB) | 45 s |
| NeoForge 21.1.259 install (official installer, headless) | 54 s |
| Forge 47.4.10 install | 26 s |
| Create+ (NeoForge, 155 files) from its `.mrpack` | 6.4 s; the game starts in 27 s with 244 mods |
| Window: mapped after launch / memory, 5 processes | 73 ms / 371 MB PSS |
| While the game runs | window processes 0; supervisor 4.5 MB PSS, 5 threads, 4.8 MB binary |

**Garbage collectors:** Fabric 26.3 with Sodium, launched straight into the same generated world with quick play and the JVM's GC log, 90 s each:

| | Pauses | Longest | Total |
|---|---|---|---|
| G1 (`MaxGCPauseMillis=37`) | 32 | 23 ms | 234 ms |
| Generational ZGC | 30 | 2.35 ms | 2.8 ms |

At 120 fps a frame lasts 8.3 ms, so G1's longer pauses show as stutters and ZGC's never do. Most pauses fell in loading, because nobody moved in the world. ZGC is the default.

## Verification

- **Launched to the title screen or into a world** on the target laptop, with an offline account:
  - vanilla 26.3;
  - Fabric 0.19.5 on 26.3 with the performance set (56 mods loaded);
  - NeoForge 21.1.259 on 1.21.1;
  - the Create+ modpack (NeoForge 21.1.233, 244 mods);
  - Forge 47.4.10 on 1.20.1;
  - Quilt 0.24.0 on 1.21.1;
  - an imported Prism instance, straight into its world, with its memory and JVM arguments.
- **In the window,** driven through the DevTools protocol:
  - a Quilt instance created with the dialog;
  - Mod Menu installed from Browse with its dependency;
  - an offline account; Microsoft sign-in shows a code from login.live.com for microsoft.com/link (window and command line); an unknown own app ID reaches Microsoft and its error is shown.
- **Play from the window:**
  - the window and all its CEF processes exited;
  - the supervisor waited (4.5 MB) and recorded 94 s of play;
  - after a forced failure (a bad JVM flag) the window reopened on the log showing Java's error.
- **Not verified:** a finished Microsoft sign-in and an online launch with it (needs the owner's account); power draw while playing (the laptop was plugged in, and then the battery reports the charge rate).

## Limits

- **Microsoft sign-in passes as the official launcher.** Minecraft services refuse app IDs Mojang hasn't approved, so mclaunch uses the official Minecraft launcher's client ID, as many small launchers do. This isn't sanctioned and Microsoft can block it at any time. An own app ID approved by Mojang can be set in Settings instead.
- **Quilt:** Quilt Loader 0.24 can't load Minecraft 26.x (it can't read Java 25 classes). It also presents an older Fabric Loader than current Fabric mods require, so many Fabric mods refuse to load on it. The performance set is offered for Fabric only.
- The window takes 371 MB (CEF), more than the plan's 250 MB target; it is closed while you play.
- CurseForge, skins, servers and Technic or ATLauncher imports aren't there.
- Linux with X11 only; built for one machine (`GOAMD64=v4` needs AVX-512).

## License

GPL-3.0 (see `LICENSE`).
