#!/bin/sh
# Build reproducible, static Linux executables; only amd64 is vendored.
set -eu
cd "$(dirname "$0")/.."
GO=${GO:-/workspace/tools/go/bin/go}
if [ ! -x "$GO" ]; then GO=go; fi
export CGO_ENABLED=0 GOOS=linux GOTOOLCHAIN=local GOPROXY=off
export GOCACHE=${GOCACHE:-/tmp/forge-go-cache}
export GOPATH=${GOPATH:-/tmp/forge-go-path}
mkdir -p bin
for arch in amd64 arm64; do
  (cd native && GOARCH=$arch "$GO" build -buildvcs=false -trimpath -ldflags='-s -w -buildid=' -o "../bin/forge-linux-$arch" ./cmd/forge)
  size=$(wc -c < "bin/forge-linux-$arch")
  if [ "$size" -gt 8388608 ]; then
    echo "bin/forge-linux-$arch exceeds the 8 MiB file limit" >&2
    exit 1
  fi
done
file bin/forge-linux-amd64 bin/forge-linux-arm64
