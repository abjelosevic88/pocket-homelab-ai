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

// The logins on a person's own machines show with their limits, to them
// only, named after the machine (and its login folder).
func TestLimitsOwnMachines(t *testing.T) {
	s, h, _ := testServer(t)
	ctx := t.Context()
	_, owner := person(t, s, h, "owner@example.test", "admin", true)
	_, arno := person(t, s, h, "arno@example.test", "member", true)
	_, sani := person(t, s, h, "sani@example.test", "member", true)
	box := &store.Host{Name: "AB-box", Target: "arno@box.invalid", UserID: arno.ID}
	if err := s.store.CreateHost(ctx, box); err != nil {
		t.Fatal(err)
	}
	win := []agent.Window{{ID: "five_hour", Name: "5 hours", Used: 34}}
	def, max := hostLimitsKey("claude", box.ID, ""), hostLimitsKey("claude", box.ID, "/home/arno/.claude-max")
	if max != def+"+claude-max" {
		t.Fatalf("key of a login folder: %q", max)
	}
	rt := s.rt
	rt.mu.Lock()
	rt.limits[def] = &provLimits{Windows: win, UpdatedAt: "2026-10-30T14:00:00Z", Email: "arno@example.test", Ident: loginIdent("claude", "arno@example.test", "org-a")}
	rt.limits[max] = &provLimits{Windows: win, UpdatedAt: "2026-10-30T14:00:00Z", Email: "arno@max.test", Ident: loginIdent("claude", "arno@max.test", "org-m")}
	rt.mu.Unlock()

	snap := rt.limitsSnapshot(arno.ID)
	d, ok := snap[def].(provLimits)
	if !ok || d.Label != "AB-box" || d.HostID != box.ID || d.ServerLogin || d.Provider != "claude" || d.Email != "arno@example.test" {
		t.Fatalf("Arno's machine login: %+v (%v)", d, snap)
	}
	if m := snap[max].(provLimits); m.Label != "AB-box · claude-max" {
		t.Fatalf("Arno's other login folder: %+v", m)
	}
	for _, uid := range []int64{sani.ID, owner.ID} {
		snap := rt.limitsSnapshot(uid)
		if _, ok := snap[def]; ok {
			t.Fatalf("user %d sees Arno's machine: %v", uid, snap)
		}
	}
	if !limitsLess("claude", "claude:9") || !limitsLess("claude:9", def) || !limitsLess(def, max) {
		t.Fatal("order: server login, accounts, then other machines")
	}
}

// One Claude account signed in on several machines (and here) is one set
// of limits: it shows once, the other logins named on it.
func TestLimitsSameAccountOnManyMachines(t *testing.T) {
	s, h, root := testServer(t)
	ctx := t.Context()
	_, arno := person(t, s, h, "arno@example.test", "member", true)
	acct := &store.Account{Provider: "claude", Label: "Claude Main", Home: filepath.Join(root, "people", "x", ".claude"), UserID: &arno.ID}
	if err := s.store.CreateAccount(ctx, acct); err != nil {
		t.Fatal(err)
	}
	a := &store.Host{Name: "AB-box", Target: "arno@a.invalid", UserID: arno.ID}
	b := &store.Host{Name: "build-box", Target: "arno@b.invalid", UserID: arno.ID}
	for _, x := range []*store.Host{a, b} {
		if err := s.store.CreateHost(ctx, x); err != nil {
			t.Fatal(err)
		}
	}
	id := loginIdent("claude", "Arno@Example.test", "org-a")
	win := func(used float64) []agent.Window {
		return []agent.Window{{ID: "five_hour", Name: "5 hours", Used: used}}
	}
	main, ka, kb := limitsKey("claude", acct.ID), hostLimitsKey("claude", a.ID, ""), hostLimitsKey("claude", b.ID, "")
	rt := s.rt
	rt.mu.Lock()
	rt.limits[main] = &provLimits{Windows: win(10), UpdatedAt: "2026-10-30T14:00:00Z", Ident: id}
	rt.limits[ka] = &provLimits{Windows: win(30), UpdatedAt: "2026-10-30T14:05:00Z", Ident: id}
	rt.limits[kb] = &provLimits{Windows: win(20), UpdatedAt: "2026-10-30T14:01:00Z", Ident: id}
	rt.mu.Unlock()

	snap := rt.limitsSnapshot(arno.ID)
	if len(snap) != 1 {
		t.Fatalf("one account, one card: %v", snap)
	}
	l, ok := snap[main].(provLimits)
	if !ok || l.Label != "Claude Main" || !slices.Equal(l.Aliases, []string{ka, kb}) || !slices.Equal(l.Also, []string{"AB-box", "build-box"}) {
		t.Fatalf("folded under the named login: %+v", l)
	}
	if l.Windows[0].Used != 30 {
		t.Fatalf("the freshest numbers: %+v", l.Windows)
	}

	// without the named login: the first machine leads
	rt.mu.Lock()
	delete(rt.limits, main)
	rt.mu.Unlock()
	snap = rt.limitsSnapshot(arno.ID)
	if l, ok := snap[ka].(provLimits); len(snap) != 1 || !ok || !slices.Equal(l.Also, []string{"build-box"}) || l.ServerLogin {
		t.Fatalf("two machines, one account: %v", snap)
	}
}
