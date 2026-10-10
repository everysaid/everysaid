#!/usr/bin/env bash
# Builds Everysaid with Signal in it: first Signal's library, libsignal_ffi (Rust), at the version
# signalmeow's libsignalgo pins (third_party/mautrix-signal/pkg/libsignalgo/signalversion), then
# the binary with the build tag `signal` (cgo), linked against it. docs/go.md tells the rest.
#
#   scripts/build-signal.sh [OUTPUT]        the binary, ./everysaid by default
#
# LIBSIGNAL_DIR: where libsignal is cloned and built (~/.cache/everysaid-build/libsignal by
# default); a clone there at another version is moved to the pinned one. Needs git, Rust (cargo:
# rustup takes the toolchain libsignal names), a C and C++ compiler, protoc, cmake and libclang
# (on Arch: protobuf, cmake, clang; on Debian and Ubuntu: protobuf-compiler, cmake, libclang-dev).
# Linux and macOS; Windows has no Signal.
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
out=${1:-$root/everysaid}
version=$(sed -n 's/^const Version = "\(v[0-9.]*\)"$/\1/p' "$root/third_party/mautrix-signal/pkg/libsignalgo/signalversion/version.go")
if [[ -z "$version" ]]; then
	echo "libsignal's version not found in libsignalgo" >&2
	exit 1
fi
dir=${LIBSIGNAL_DIR:-${XDG_CACHE_HOME:-$HOME/.cache}/everysaid-build/libsignal}

if [[ ! -d "$dir/.git" ]]; then
	mkdir -p "$(dirname "$dir")"
	git clone --depth 1 --branch "$version" https://github.com/signalapp/libsignal.git "$dir"
elif [[ "$(git -C "$dir" describe --tags --exact-match 2>/dev/null || true)" != "$version" ]]; then
	git -C "$dir" fetch --depth 1 origin tag "$version"
	git -C "$dir" checkout --quiet "$version"
fi

echo "libsignal $version in $dir" >&2
(cd "$dir" && cargo build -p libsignal-ffi --profile=release)

cd "$root"
CGO_ENABLED=1 CGO_LDFLAGS="-L$dir/target/release" go build -tags signal -trimpath -ldflags="-s -w" -o "$out" ./cmd/everysaid
echo "built $out (with Signal)" >&2
