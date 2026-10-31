// pocket-hold keeps a coding CLI running across dropped connections and
// Pocket AI restarts (internal/hold). Pocket AI installs it on each machine.
package main

import (
	"os"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/hold"
)

func main() { os.Exit(hold.Main(os.Args[1:])) }
