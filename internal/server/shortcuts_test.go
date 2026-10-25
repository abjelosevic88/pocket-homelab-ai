package server

import (
	"testing"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

// Changed keyboard shortcuts are kept per person; bad action ids are dropped.
func TestShortcutsSaveAndStayPersonal(t *testing.T) {
	s, h, _ := testServer(t)
	alice, _ := person(t, s, h, "alice@example.com", "admin", false)
	bob, _ := person(t, s, h, "bob@example.com", "admin", false)

	alice.want("PUT", "/api/v1/settings", map[string]any{"shortcuts": map[string]string{
		"new_tab": "Mod+Alt+T", "close_tab": "", "Bad Id": "mod+x",
	}}, 200, nil)
	var st store.Settings
	alice.want("GET", "/api/v1/settings", nil, 200, &st)
	if st.Shortcuts["new_tab"] != "mod+alt+t" || len(st.Shortcuts) != 2 {
		t.Fatalf("alice shortcuts: %#v", st.Shortcuts)
	}
	if v, ok := st.Shortcuts["close_tab"]; !ok || v != "" {
		t.Fatalf("turned-off shortcut lost: %#v", st.Shortcuts)
	}
	var other store.Settings
	bob.want("GET", "/api/v1/settings", nil, 200, &other)
	if len(other.Shortcuts) != 0 {
		t.Fatalf("bob sees alice's shortcuts: %#v", other.Shortcuts)
	}
}
