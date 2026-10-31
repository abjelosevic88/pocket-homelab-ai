package server

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

// An app linked with threads:read (or threads:write) sees which files the
// person's conversations that made a pull request edited, in order, with
// what the agent said before — and nothing of anyone else's.
func TestLinkEdits(t *testing.T) {
	s, h, _ := testServer(t)
	_, me := person(t, s, h, "link@example.test", "member", true)
	_, other := person(t, s, h, "other@example.test", "member", true)
	linkCore(t, s, map[string]string{"pcx_valid": "link@example.test"})
	pr := "https://github.com/acme/shop/pull/7"
	add := func(uid int64, id string, items ...string) {
		th := &store.Thread{ID: id, UserID: uid, Provider: "claude", Cwd: "/work/shop"}
		if err := s.store.CreateThread(t.Context(), th); err != nil {
			t.Fatal(err)
		}
		for i, d := range items {
			var probe struct {
				Kind string `json:"kind"`
			}
			_ = json.Unmarshal([]byte(d), &probe)
			if _, err := s.store.UpsertItem(t.Context(), id, fmt.Sprintf("k%d", i), probe.Kind, []byte(d), ""); err != nil {
				t.Fatal(err)
			}
		}
	}
	add(me.ID, "t-mine",
		`{"kind":"assistant","text":"Start with the table."}`,
		`{"kind":"tool","tool":{"name":"Write","kind":"write","input":{"file_path":"/work/shop/db/7.sql","content":"x"}}}`,
		`{"kind":"assistant","text":"Now the route."}`,
		`{"kind":"tool","tool":{"name":"Edit","kind":"edit","input":{"file_path":"/work/shop/api/coupons.go"}}}`,
		`{"kind":"tool","tool":{"name":"Bash","kind":"bash","input":{"command":"gh pr create --fill"}},"output":"`+pr+`\n"}`)
	add(other.ID, "t-other",
		`{"kind":"tool","tool":{"name":"Write","kind":"write","input":{"file_path":"/work/shop/secret.go"}}}`,
		`{"kind":"tool","tool":{"name":"Bash","kind":"bash","input":{"command":"gh pr view"}},"output":"`+pr+`"}`)

	c := &client{t: t, h: h, token: "pcx_valid"}
	code, b := c.do("GET", "/api/v1/link/edits?pr="+pr, "", nil)
	if code != 200 {
		t.Fatalf("edits: %d %s", code, b)
	}
	var out struct {
		Edits []linkEdit `json:"edits"`
	}
	_ = json.Unmarshal(b, &out)
	if len(out.Edits) != 2 || out.Edits[0].Path != "db/7.sql" || out.Edits[0].Note != "Start with the table." || out.Edits[1].Path != "api/coupons.go" || out.Edits[1].Note != "Now the route." {
		t.Fatalf("edits: %+v", out.Edits)
	}
	for _, tok := range []string{"pcx_wrong_scope", "pcx_wrong_audience", "pcx_revoked", ""} {
		c := &client{t: t, h: h, token: tok}
		if code, _ := c.do("GET", "/api/v1/link/edits?pr="+pr, "", nil); code != 401 && code != 403 {
			t.Fatalf("%q read edits: %d", tok, code)
		}
	}
}

func TestEditedPaths(t *testing.T) {
	cases := map[string][]string{
		`{"file_path":"/a/b.go"}`:           {"/a/b.go"},
		`{"changes":{"x.go":{},"a.go":{}}}`: {"a.go", "x.go"},
		`{"patch":"*** Begin Patch\n*** Update File: src/a.ts\n@@\n*** Add File: src/b.ts\n*** End Patch"}`: {"src/a.ts", "src/b.ts"},
	}
	for in, want := range cases {
		got := editedPaths(json.RawMessage(in))
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("%s: %v, want %v", in, got, want)
		}
	}
	if relTo("/w", "/w/a/b.go") != "a/b.go" || relTo("/w", "/x/b.go") != "/x/b.go" {
		t.Fatal("relTo")
	}
}
