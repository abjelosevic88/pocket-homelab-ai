package hold

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
)

//go:generate sh build-bins.sh

//go:embed bin
var bins embed.FS

// Binary is pocket-hold for goos/goarch as this server was built with it,
// and a name for it that changes with its content. ok is false when the
// build did not include it.
func Binary(goos, goarch string) (data []byte, name string, ok bool) {
	data, err := bins.ReadFile("bin/pocket-hold-" + goos + "-" + goarch)
	if err != nil {
		return nil, "", false
	}
	sum := sha256.Sum256(data)
	return data, "pocket-hold-" + hex.EncodeToString(sum[:6]), true
}
