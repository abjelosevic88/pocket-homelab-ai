package server

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

// Logins signed in to the same account share one set of limits: they show
// once, under the named account, with the freshest numbers.
func TestLimitsSameAccountShowOnce(t *testing.T) {
	s, h, root := testServer(t)
	s.cfg.PeopleDir = "" // the shared-login model: everyone may use the server login (people.go is off)
	_, admin := person(t, s, h, "admin@example.test", "admin", true)
	_, bob := person(t, s, h, "bob@example.test", "member", true)
	team := &store.Account{Provider: "claude", Label: "Team", Home: filepath.Join(root, ".claude-team")}
	personal := &store.Account{Provider: "claude", Label: "Personal", Home: filepath.Join(root, ".claude-personal")}
	bobs := &store.Account{Provider: "claude", Label: "Bob's", Home: filepath.Join(root, ".claude-bob"), UserID: &bob.ID}
	for _, a := range []*store.Account{team, personal, bobs} {
		if err := s.store.CreateAccount(t.Context(), a); err != nil {
			t.Fatal(err)
		}
	}
	teamID := loginIdent("claude", "Me@Example.test", "org-team")
	win := func(used float64) []agent.Window {
		return []agent.Window{{ID: "seven_day", Name: "7 days", Used: used}}
	}
	rt := s.rt
	rt.mu.Lock()
	rt.limits["claude"] = &provLimits{Windows: win(98), UpdatedAt: "2026-10-25T17:36:00Z", Email: "me@example.test", Ident: teamID}
	rt.limits[limitsKey("claude", team.ID)] = &provLimits{Windows: win(97), UpdatedAt: "2026-10-25T17:35:00Z", Email: "me@example.test", Ident: teamID}
	rt.limits[limitsKey("claude", personal.ID)] = &provLimits{Windows: win(9), UpdatedAt: "2026-10-25T17:35:00Z", Email: "me@gmail.test", Ident: loginIdent("claude", "me@gmail.test", "org-me")}
	rt.limits[limitsKey("claude", bobs.ID)] = &provLimits{Windows: win(50), UpdatedAt: "2026-10-25T17:30:00Z", Email: "me@example.test", Ident: teamID}
	rt.limits["codex"] = &provLimits{Windows: win(10), UpdatedAt: "2026-10-25T17:35:00Z"} // who it is: unknown
	rt.mu.Unlock()

	snap := rt.limitsSnapshot(admin.ID)
	keys := func(m map[string]any) []string {
		var out []string
		for k := range m {
			out = append(out, k)
		}
		slices.Sort(out)
		return out
	}
	teamKey, personalKey, bobsKey := limitsKey("claude", team.ID), limitsKey("claude", personal.ID), limitsKey("claude", bobs.ID)
	want := []string{teamKey, personalKey, "codex"}
	slices.Sort(want)
	if got := keys(snap); !slices.Equal(got, want) {
		t.Fatalf("admin sees %v, want %v (server login folded into Team)", got, want)
	}
	tl := snap[teamKey].(provLimits)
	if !tl.ServerLogin || !slices.Equal(tl.Aliases, []string{"claude"}) || tl.Label != "Team" {
		t.Fatalf("Team should carry the server login: %+v", tl)
	}
	if tl.Windows[0].Used != 98 || tl.UpdatedAt != "2026-10-25T17:36:00Z" {
		t.Fatalf("Team should show the freshest numbers of its logins: %+v", tl)
	}
	if tl.Ident != "" || tl.Email != "me@example.test" {
		t.Fatalf("ident leaked or email missing for the admin: %+v", tl)
	}
	if pl := snap[personalKey].(provLimits); pl.ServerLogin || len(pl.Aliases) > 0 {
		t.Fatalf("Personal is another account: %+v", pl)
	}
	if cl := snap["codex"].(provLimits); !cl.ServerLogin {
		t.Fatalf("an unmatched server login is still marked as the server's: %+v", cl)
	}

	// Bob may use the server login, the shared ones and his own (which is
	// the same team account): all three fold into Team.
	snap = rt.limitsSnapshot(bob.ID)
	if _, ok := snap[bobsKey]; ok {
		t.Fatalf("Bob's own login should fold into Team: %v", keys(snap))
	}
	tl = snap[teamKey].(provLimits)
	if !slices.Equal(tl.Aliases, []string{"claude", bobsKey}) {
		t.Fatalf("Team aliases for Bob: %v", tl.Aliases)
	}
	if snap[personalKey].(provLimits).Email != "" {
		t.Fatal("a member saw the email of a login that is not theirs")
	}
}
