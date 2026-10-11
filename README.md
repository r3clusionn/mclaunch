# mclaunch

A Minecraft launcher for Linux in Go: instances, Microsoft accounts, Fabric, Quilt, NeoForge and Forge, Modrinth mods and modpacks, and imports of existing Prism Launcher instances. It downloads Mojang's own Java runtimes, so nothing has to be installed system-wide. It is made for one laptop (Ryzen AI 9 HX 475, Radeon 890M, 2880x1800 at 120 Hz, Gentoo with dwm): its interface (Seanime's design, in the same standalone window as [media-player](https://github.com/r3clusionn/media-player)) closes completely when the game starts, and it sets Java, Mesa, the frame cap and the power profile for the hardware and the power source.

**Status:** planned, phase 6 of 7 of [the plan](https://github.com/r3clusionn/media-player/blob/main/docs/PLAN.md#6-minecraft-launcher-mclaunch). No code yet. Screenshots will be captures of the real program once it runs.

## Features

- **Accounts:** Microsoft sign-in with a device code (open the link on any device, no embedded login page), several accounts, tokens refreshed automatically.
- **Versions and loaders:** every vanilla release and snapshot, Fabric, Quilt, NeoForge and Forge. Libraries, assets and Java are shared between instances and checked by hash.
- **Java without packages:** the runtime each version asks for is downloaded from Mojang's Java manifest, as the official launcher does; a custom Java can be set per instance.
- **Instances:** memory, Java arguments, window size and environment per instance; mods, resource packs, shader packs, worlds and screenshots.
- **Mods from Modrinth:** search by version and loader, install with dependencies, update all, enable or disable, and identify existing jars by hash. `.mrpack` import and export.
- **Prism Launcher import:** existing instances (`instance.cfg`, `mmc-pack.json`) come over with their mods and worlds.
- **Logs and crashes:** a live log with search, and the crash report opened with the likely mod named.
- **Tuned for the laptop:** see How it works.

## How to install

Requires Go 1.27 and Node.js 26 with npm (to build the interface).

```sh
git clone https://github.com/r3clusionn/mclaunch
cd mclaunch
(cd web && npm ci && npm run build)
GOAMD64=v4 go build -trimpath -ldflags="-s -w" -o mclaunch .
```

## How to use

To be written with the program.

## How it works

- **The interface** runs in the shared standalone window from media-player (Chromium's engine embedded as a library, no browser window). When the game starts the window and its processes exit; a small supervisor waits for the game to finish, restores the power profile and offers the crash log.
- **Launching** builds the class path and arguments from the version's JSON (rules for the operating system and features), extracts the LWJGL natives, and starts the game on its own session.
- **Java:** a fixed heap (`-Xms` equal to `-Xmx`, 4 GB for vanilla or light mods, 6 to 8 GB for large packs); G1 with a short pause target and generational ZGC are both measured, and the smoother one is the default.
- **Mesa:** `mesa_glthread=true` and a 4 GB shader cache.
- **Frame cap by power source:** 120 fps on AC (the panel's rate, with VRR) and 60 on battery, written to the instance's `options.txt` before each launch.
- **Performance mods in one click** for new Fabric and Quilt instances: Sodium, Lithium, FerriteCore, ModernFix, ImmediatelyFast and EntityCulling.
- **Power profile:** a `game` mode (performance on AC) while the game runs, through a doas rule, and back to automatic afterwards.

## Measurements

None yet. Planned: frame times with each garbage collector, and watts on AC and battery with each frame cap.

## Verification

None yet. Planned: Microsoft sign-in, vanilla latest, Fabric with Sodium and a NeoForge pack launching, a Prism instance imported and launched, the interface's processes gone while the game runs.

## Limits

- Microsoft sign-in needs this launcher's own Azure app ID approved by Mojang; until then only offline singleplayer works.
- CurseForge needs an API key and comes later, as do skins and Technic or ATLauncher imports.
- Linux with X11 only; built for one machine (`GOAMD64=v4` needs AVX-512).

## License

GPL-3.0 (see `LICENSE`); the interface reuses components from Seanime.
