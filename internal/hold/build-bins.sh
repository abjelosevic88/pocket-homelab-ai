#!/bin/sh
# Builds pocket-hold for the machines Pocket AI installs it on (bins.go).
# VERSION (as the Makefile passes it; else git describe) is stamped into the
# relay for people to read. Pocket AI does not compare it: it knows a relay
# is the one it uploaded by the binary's content hash (relay.Hello.Sum).
set -e
v=${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}
for t in linux/amd64 linux/arm64 darwin/arm64 darwin/amd64; do
	GOOS=${t%/*} GOARCH=${t#*/} CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X github.com/abjelosevic88/pocket-homelab-ai/internal/relay.Version=$v" -o "bin/pocket-hold-${t%/*}-${t#*/}" ../../cmd/pocket-hold
done
