package server

import (
	"strings"
	"testing"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

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

func TestRetitleConversation(t *testing.T) {
	if retitleConversation(nil) != "" {
		t.Fatal("empty conversation quoted")
	}
	items := []*store.Item{
		{Kind: "user", Body: "Check my Pocket apps"},
		{Kind: "assistant", Body: "Looked at them."},
		{Kind: "user", Body: "<cross-session-message from-name=\"x\">hi</cross-session-message>"},
		{Kind: "user", Body: "Now add rename to the palette"},
		{Kind: "assistant", Body: "Palette actions deployed."},
	}
	got := retitleConversation(items)
	for _, want := range []string{"Check my Pocket apps", "- Now add rename to the palette", "Palette actions deployed.", "as a whole"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "cross-session") || strings.Contains(got, "Looked at them") {
		t.Fatalf("quoted what it should skip:\n%s", got)
	}
}
