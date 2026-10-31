#!/bin/sh
# Builds pocket-hold for the machines Pocket AI installs it on (bins.go).
set -e
for t in linux/amd64 linux/arm64 darwin/arm64 darwin/amd64; do
	GOOS=${t%/*} GOARCH=${t#*/} CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o "bin/pocket-hold-${t%/*}-${t#*/}" ../../cmd/pocket-hold
done
