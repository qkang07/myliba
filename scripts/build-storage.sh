#!/usr/bin/env sh
set -eu

target_os=${1:-$(go env GOOS)}
target_arch=${2:-$(go env GOARCH)}
case "$target_os" in windows|darwin|linux) ;; *) echo 'unsupported OS' >&2; exit 2;; esac
case "$target_arch" in amd64|arm64) ;; *) echo 'unsupported architecture' >&2; exit 2;; esac

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
output_dir="$root/.runtime/storage-build/$target_os-$target_arch"
mkdir -p "$output_dir" "$root/.runtime/gocache"
name=myliba-storage
if [ "$target_os" = windows ]; then name=myliba-storage.exe; fi
space_file=cloud_space_unix.go
if [ "$target_os" = windows ]; then space_file=cloud_space_windows.go; fi
cd "$root"
GOOS="$target_os" GOARCH="$target_arch" CGO_ENABLED=0 GOCACHE="$root/.runtime/gocache" \
    go build -trimpath -buildvcs=false -o "$output_dir/$name" \
    cmd/myliba/cloud_protocol.go cmd/myliba/cloud_store.go cmd/myliba/cloud_stats.go "cmd/myliba/$space_file" \
    cmd/myliba/cloud_hub.go cmd/myliba/cloud_node.go \
    cmd/myliba/cloud_repair.go cmd/myliba/cloud_backend.go \
    cmd/myliba/cloud_s3.go cmd/myliba/cloud_storage_entry.go
printf '%s\n' "$output_dir/$name"
