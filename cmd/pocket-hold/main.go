// pocket-hold keeps a coding CLI running across dropped connections and
// Pocket AI restarts (internal/hold), and runs the machine side of the link
// (internal/relay). Pocket AI installs it on each machine.
package main

import (
	"os"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/hold"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/relay"
)

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "relay" || os.Args[1] == "fence" || os.Args[1] == "probe") {
		os.Exit(relay.Main(os.Args[1:]))
	}
	os.Exit(hold.Main(os.Args[1:]))
}
