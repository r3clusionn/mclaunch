#!/bin/sh
# Builds mclaunch as a standalone program into build/dist: mclaunch (with the
# interface embedded), appshell-helper, CEF (libcef.so, .pak, locales), the
# bundled NSS/NSPR/xkbcommon and the libcups stand-in.
#
# Usage: ./build.sh [--skip-web] [--install]
#   --skip-web  reuse web/dist from the last build
#   --install   copy build/dist to ~/.local/opt/mclaunch (CEF stripped of its
#               debug info) and link ~/.local/bin/mclaunch
#
# Needs ~/src/third_party (or THIRD_PARTY): cef/ (the CEF minimal build) and
# runtime/ (Thorium's bundled libraries), as for media-player.
set -eu
cd "$(dirname "$0")"
ROOT=$PWD
TP=${THIRD_PARTY:-$HOME/src/third_party}
DIST=$ROOT/build/dist
SKIP_WEB=0 INSTALL=0
for a in "$@"; do
	case $a in
	--skip-web) SKIP_WEB=1 ;;
	--install) INSTALL=1 ;;
	*) echo "unknown option $a" >&2; exit 2 ;;
	esac
done

[ -f "$TP/cef/include/cef_version.h" ] || { echo "no CEF in $TP/cef" >&2; exit 1; }
[ -e third_party ] || ln -s "$TP" third_party
mkdir -p "$DIST"

echo "== libcups stand-in"
gcc -O2 -march=native -shared -fPIC -Wl,-soname,libcups.so.2 -o "$DIST/libcups.so.2" appshell/cups-stub/cups_stub.c
sh appshell/cups-stub/check-symbols.sh "$TP/cef/Release/libcef.so" "$DIST/libcups.so.2"

echo "== CEF runtime"
for f in "$TP"/cef/Release/* "$TP"/cef/Resources/*; do
	case $(basename "$f") in chrome-sandbox) continue ;; esac
	if [ -d "$f" ]; then rm -rf "$DIST/$(basename "$f")"; cp -al "$f" "$DIST/"
	else ln -f "$f" "$DIST/"; fi
done
for f in "$TP"/runtime/lib*.so* "$TP"/runtime/BUNDLED_VERSIONS; do
	case $(basename "$f") in libcef.so|libcups.so.2|libvk_swiftshader.so|libvulkan.so.1) continue ;; esac
	ln -f "$f" "$DIST/"
done

echo "== appshell-helper"
gcc -std=gnu11 -O2 -march=native -Wall -Wextra -Wno-unused-parameter \
	-DSHIM_JS_PATH="\"$ROOT/appshell/helper/shim.js\"" -I "$TP/cef" \
	appshell/helper/helper.c -o "$DIST/appshell-helper" \
	-L "$DIST" -lcef -Wl,--disable-new-dtags,-rpath,'$ORIGIN'

if [ $SKIP_WEB = 0 ]; then
	echo "== interface"
	(cd web && { [ -d node_modules ] || npm ci --no-audit --no-fund --loglevel=error; } && npx vite build --logLevel warn)
fi

echo "== mclaunch"
GOAMD64=v4 CGO_ENABLED=1 go build -trimpath \
	-ldflags="-s -w -extldflags '-Wl,--disable-new-dtags,-rpath,\$ORIGIN'" \
	-o "$DIST/mclaunch" .

echo "== mclaunch-supervise (static, no CEF)"
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o "$DIST/mclaunch-supervise" ./cmd/mclaunch-supervise

echo "built: $(du -sh "$DIST" | cut -f1) in $DIST"

if [ $INSTALL = 1 ]; then
	OPT=$HOME/.local/opt/mclaunch
	mkdir -p "$OPT" "$HOME/.local/bin"
	cp -a --remove-destination "$DIST"/. "$OPT"/
	strip --strip-unneeded "$OPT/libcef.so"
	ln -sfn "$OPT/mclaunch" "$HOME/.local/bin/mclaunch"
	echo "installed: $OPT, linked from ~/.local/bin/mclaunch"
fi
