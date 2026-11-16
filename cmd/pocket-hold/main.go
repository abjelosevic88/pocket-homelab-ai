// pocket-hold keeps a coding CLI running across dropped connections and
// Pocket AI restarts (internal/hold). Pocket AI installs it on each machine.
package main

import (
	"os"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/hold"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/worktreefiles"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "worktree-files" {
		os.Exit(worktreefiles.Main(os.Args[2:]))
	}
	os.Exit(hold.Main(os.Args[1:]))
}
