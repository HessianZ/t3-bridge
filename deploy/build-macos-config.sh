#!/bin/sh
set -eu

if [ "$(uname -s)" != Darwin ] || [ "$#" -gt 1 ]; then
    printf '%s\n' 'Usage on macOS: sh deploy/build-macos-config.sh [output.app]' >&2
    exit 2
fi

output=${1:-bin/T3 Bridge Config.app}
version=${T3_BRIDGE_BUILD_VERSION:-devel}
case "$version" in
    *[!0-9A-Za-z.+-]*) printf '%s\n' 'Invalid T3_BRIDGE_BUILD_VERSION.' >&2; exit 2 ;;
esac
case "$output" in
    *.app) ;;
    *) printf '%s\n' 'The output path must end in .app.' >&2; exit 2 ;;
esac
if [ -e "$output" ] || [ -L "$output" ]; then
    printf '%s\n' 'The output app already exists. Choose a new path.' >&2
    exit 1
fi

repo=$(CDPATH= cd -P "$(dirname "$0")/.." && pwd)
parent=$(dirname "$output")
mkdir -p "$parent"
parent=$(CDPATH= cd -P "$parent" && pwd)
stage=$(mktemp -d "$parent/.t3-bridge-app.XXXXXXXX")
trap 'rm -rf "$stage"' EXIT
app=$stage/Config.app
mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources"
cp "$repo/deploy/macos-config.Info.plist" "$app/Contents/Info.plist"
cp "$repo/deploy/t3-bridge.icns" "$app/Contents/Resources/"
cp "$repo/LICENSE" "$repo/THIRD_PARTY_NOTICES.md" "$app/Contents/Resources/"
(
    cd "$repo"
    export MACOSX_DEPLOYMENT_TARGET=13.0
    deployment_flag=-mmacosx-version-min=$MACOSX_DEPLOYMENT_TARGET
    # Record the macOS deployment target in Go's cgo cache key.
    CGO_CPPFLAGS="$(go env CGO_CPPFLAGS) $deployment_flag"
    CGO_LDFLAGS="$(go env CGO_LDFLAGS) $deployment_flag"
    export CGO_CPPFLAGS CGO_LDFLAGS
    CGO_ENABLED=1 go build -mod=readonly -buildvcs=false -trimpath \
        -ldflags "-X main.standaloneConfigApp=1 -X main.buildVersion=$version" \
        -o "$app/Contents/MacOS/t3-bridge-config" ./cmd/t3-bridge
)
codesign --force --sign - --timestamp=none "$app"
mkdir "$output"
mv "$app/Contents" "$output/Contents"
printf '%s\n' "$output"
