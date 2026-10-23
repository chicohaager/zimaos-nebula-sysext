#!/bin/sh
# Build the nebula sysext for ZimaOS.
#   sh build.sh [amd64|arm64]      (default: amd64)
# Output in dist/: nebula-<arch>.raw (+ .sha256), for amd64 also nebula.raw.
set -eu
cd "$(dirname "$0")"
ARCH="${1:-amd64}"
MODKIT="${LINTUX_MODKIT:-../lintux-modkit}"
[ -f "$MODKIT/sysext/unit-watchdog.sh" ] || { echo "lintux-modkit with sysext/unit-watchdog.sh not found at $MODKIT (set LINTUX_MODKIT)" >&2; exit 1; }
VERSION=$(cat VERSION)

# Upstream release, pinned by version AND sha256 (from SHASUM256.txt of the release, 2026-10-23).
NEBULA_VERSION=v1.11.2
case "$ARCH" in
  amd64) SUM=6140d33f2ec21ce7f6b655b5bc820e93a684d97e51d0ddcf907324b5b28aac1e ;;
  arm64) SUM=85d10e7bc2d121193c1392a1a919172ded7c413f46e602138281cfa9fa1b0231 ;;
  *) echo "unsupported arch: $ARCH" >&2; exit 2 ;;
esac
TGZ="nebula-linux-$ARCH.tar.gz"

rm -rf build
mkdir -p build/root build/dl
curl -fsSL -o "build/dl/$TGZ" "https://github.com/slackhq/nebula/releases/download/$NEBULA_VERSION/$TGZ"
echo "$SUM  build/dl/$TGZ" | sha256sum -c -
tar -xzf "build/dl/$TGZ" -C build/dl nebula nebula-cert
# The backend tests run the real nebula-cert and nebula (amd64 build host).
go vet ./...
if [ "$ARCH" = amd64 ]; then NEBULA_BIN="$PWD/build/dl" go test ./...; else go test ./...; fi

cp -a files/. build/root/
install -m 755 build/dl/nebula build/dl/nebula-cert build/root/usr/bin/
mkdir -p build/root/usr/libexec/nebula build/root/usr/share/doc/nebula build/root/usr/share/casaos/modules
cp "$MODKIT/sysext/unit-watchdog.sh" build/root/usr/libexec/nebula/unit-watchdog.sh
curl -fsSL -o build/root/usr/share/doc/nebula/LICENSE "https://raw.githubusercontent.com/slackhq/nebula/$NEBULA_VERSION/LICENSE"
echo "nebula $NEBULA_VERSION ($TGZ sha256 $SUM)" > build/root/usr/share/doc/nebula/packages.txt
cp README.md build/root/usr/share/doc/nebula/README
# The tile: module JSON (its "services" make zpkg enable and start nebula-web; without the
# JSON zpkg answers "module not installed", rc=1 — measured 1.8.0), UI files and the backend.
sh "$MODKIT/sysext/add-web.sh" nebula build/root "$VERSION"
CGO_ENABLED=0 GOOS=linux GOARCH="$ARCH" go build -trimpath -ldflags="-s -w -X main.version=$VERSION" \
  -o build/root/usr/bin/nebula-web ./cmd/nebula-web
sh "$MODKIT/sysext/build-sysext.sh" nebula build/root "$ARCH" dist "$VERSION"
cat "dist/nebula-$ARCH.raw.sha256"
