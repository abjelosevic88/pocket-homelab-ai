package server

import "testing"

func TestIsAutoTitle(t *testing.T) {
	first := "So check my other pocket apps and tell me, can we build something like this? Something that will replace SearXNG for me"
	if !isAutoTitle(titleFrom(first), first) {
		t.Fatal("new-thread title not seen as automatic")
	}
	if !isAutoTitle("Fix this …", "Fix this\nplease") {
		t.Fatal("imported-session title not seen as automatic")
	}
	for _, kept := range []string{"Pocket Search build", "Morning server check · Nov 1 08:00", "Fork of " + titleFrom(first)} {
		if isAutoTitle(kept, first) {
			t.Fatalf("%q would be overwritten", kept)
		}
	}
}

func TestCleanTitle(t *testing.T) {
	for in, want := range map[string]string{
		`"Pocket Search: SearXNG replacement".`: "Pocket Search: SearXNG replacement",
		"Title: Nextcloud 502 after update\n":   "Nextcloud 502 after update",
		"**Jellyfin subtitle sync**":            "Jellyfin subtitle sync",
	} {
		if got, err := cleanTitle(in); err != nil || got != want {
			t.Fatalf("cleanTitle(%q) = %q, %v", in, got, err)
		}
	}
	if _, err := cleanTitle("   "); err == nil {
		t.Fatal("empty title accepted")
	}
}
