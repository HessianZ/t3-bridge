#!/bin/sh
set -eu
export LC_ALL=C TZ=UTC0

fail() {
    printf '%s\n' "$*" >&2
    exit 1
}

if [ "$#" -lt 2 ] || [ "$#" -gt 3 ]; then
    fail 'Usage: sh deploy/build-release.sh VERSION OUTPUT_ZIP [core|desktop]'
fi

version=$1
output=$2
flavor=${3-core}
case "$version" in
    ''|*[!a-zA-Z0-9._+-]*) fail 'VERSION must contain only letters, digits, dots, underscores, plus signs or hyphens.' ;;
esac
case "$output" in
    *.zip) ;;
    *) fail 'OUTPUT_ZIP must end in .zip.' ;;
esac
case "$flavor" in
    core|desktop) ;;
    *) fail "Unknown build flavor '$flavor'. Choose core or desktop." ;;
esac

case "$output" in
    /*) output_path=$output ;;
    *) output_path=$PWD/$output ;;
esac
if [ -e "$output_path" ] || [ -L "$output_path" ]; then
    fail "Output already exists: $output"
fi
command -v zip >/dev/null 2>&1 || fail 'Release builds require zip.'

repo=$(CDPATH= cd -P "$(dirname "$0")/.." && pwd)
cd "$repo"
target_os=$(go env GOOS)
target_arch=$(go env GOARCH)
binary=t3-bridge
case "$target_os" in
    darwin|linux) ;;
    windows) binary=t3-bridge.exe ;;
    *) fail "Unsupported GOOS '$target_os'. Choose darwin, linux or windows." ;;
esac

cgo=0
tags=
if [ "$flavor" = desktop ]; then
    tags=desktop
    case "$target_os" in
        darwin|linux)
            [ "$(go env GOHOSTOS)" = "$target_os" ] || fail "The $target_os desktop build requires a $target_os host."
            cgo=1
            ;;
    esac
    case "$target_os" in
        darwin)
            export MACOSX_DEPLOYMENT_TARGET=13.0
            deployment_flag=-mmacosx-version-min=$MACOSX_DEPLOYMENT_TARGET
            # Record the macOS deployment target in Go's cgo cache key.
            CGO_CPPFLAGS="$(go env CGO_CPPFLAGS) $deployment_flag"
            CGO_LDFLAGS="$(go env CGO_LDFLAGS) $deployment_flag"
            export CGO_CPPFLAGS CGO_LDFLAGS
            ;;
        linux) pkg-config --exists gtk+-3.0 webkit2gtk-4.1 || fail 'Linux desktop builds require pkg-config, GTK 3 and WebKitGTK 4.1 development files.' ;;
    esac
fi

parent=$(dirname "$output_path")
mkdir -p "$parent"
stage=$(mktemp -d "$parent/.t3-bridge-release.XXXXXXXX")
trap 'rm -rf "$stage"' EXIT
trap 'exit 1' HUP INT TERM
archive=$stage/${output_path##*/}
bundle=$stage/bundle
mkdir -p "$bundle/bin" "$bundle/config" "$bundle/deploy" "$bundle/docs/screenshots"

# Paths here are the complete archive allowlist, in ZIP entry order.
set -- README.md README.zh-CN.md LICENSE THIRD_PARTY_NOTICES.md
set -- "$@" config/generic.example.json config/homeops.example.json config/multi-host.example.json
set -- "$@" deploy/lark-telemetry-relay.mjs deploy/lark-telemetry-events.mjs
if [ "$target_os" = linux ]; then
    set -- "$@" deploy/t3-bridge.service
    if [ "$flavor" = desktop ]; then
        set -- "$@" deploy/t3-bridge.desktop
    fi
fi
if [ "$target_os" = windows ] && [ "$flavor" = desktop ]; then
    mkdir -p "$bundle/third_party/go-webview2/webviewloader"
    set -- "$@" third_party/go-webview2/LICENSE third_party/go-webview2/webviewloader/LICENSE
fi
set -- "$@" docs/t3-bridge-logo.png
set -- "$@" docs/screenshots/web-workspace.png docs/screenshots/macos-workspace.png \
    docs/screenshots/terminal-workspace.png docs/screenshots/feishu-help.png
for guide in ARCHITECTURE CONFIGURATION FEISHU-COMMANDS HOMEOPS-INTEGRATION RUNBOOK THREAD-LIFECYCLE; do
    set -- "$@" "docs/$guide.md" "docs/$guide.zh-CN.md"
done
for file do
    [ -f "$file" ] && [ ! -L "$file" ] || fail "Expected a regular release input: $file"
    cp "$file" "$bundle/$file"
    chmod 644 "$bundle/$file"
done
launcher=
case "$target_os:$flavor" in
    linux:desktop) launcher='T3 Bridge Config.sh'; launcher_mode=755 ;;
    windows:desktop) launcher='T3 Bridge Config.cmd'; launcher_mode=644 ;;
esac
if [ -n "$launcher" ]; then
    cp "deploy/$launcher" "$bundle/$launcher"
    chmod "$launcher_mode" "$bundle/$launcher"
    set -- "$@" "$launcher"
fi

GOOS=$target_os GOARCH=$target_arch CGO_ENABLED=$cgo go build -mod=readonly -buildvcs=false -trimpath -tags "$tags" \
    -ldflags "-X main.buildVersion=$version" -o "$bundle/bin/$binary" ./cmd/t3-bridge
chmod 755 "$bundle/bin/$binary"
set -- "bin/$binary" "$@"
(
    cd "$bundle"
    touch -t 200001010000 "$@"
    ZIPOPT= zip -q -X "$archive" "$@"
)

if command -v sha256sum >/dev/null 2>&1; then
    checksum=$(sha256sum < "$archive")
else
    checksum=$(shasum -a 256 < "$archive")
fi
# Publish the complete ZIP in one step. A concurrent build cannot replace it.
ln "$archive" "$parent/"
printf '%s  %s\n' "${checksum%% *}" "$output"
