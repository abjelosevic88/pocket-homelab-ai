package server

import (
	"os"
	"testing"
)

// Agents started by these tests run as plain children: a held one would
// outlive the test in the real home's ~/.pocket-ai (held.go has its own).
func TestMain(m *testing.M) {
	os.Setenv("POCKET_HOLD", "off")
	os.Exit(m.Run())
}
