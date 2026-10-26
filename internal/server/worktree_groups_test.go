package server

import (
	"context"
	"fmt"
	"testing"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

func TestWorktreeGroups(t *testing.T) {
	s, h, _ := testServer(t)
	alex, au := person(t, s, h, "alex@example.test", "member", true)
	ben, bu := person(t, s, h, "ben@example.test", "member", true)
	ctx := context.Background()
	wt := func(uid int64, b string) int64 {
		w := &store.Worktree{UserID: uid, Repo: "/r", Path: "/r/.wt/" + b, Branch: b, Base: "main"}
		if err := s.store.CreateWorktree(ctx, w); err != nil {
			t.Fatal(err)
		}
		return w.ID
	}
	a1, a2, a3, b1 := wt(au.ID, "a1"), wt(au.ID, "a2"), wt(au.ID, "a3"), wt(bu.ID, "b1")

	var g1, g2 store.WorktreeGroup
	alex.want("POST", "/api/v1/worktree-groups", map[string]any{"name": "  Release  "}, 201, &g1)
	alex.want("POST", "/api/v1/worktree-groups", map[string]any{"name": "Spikes"}, 201, &g2)
	alex.want("POST", "/api/v1/worktree-groups", map[string]any{"name": " "}, 400, nil)
	if g1.Name != "Release" || g2.Position != g1.Position+1 {
		t.Fatalf("groups: %+v %+v", g1, g2)
	}
	// into a group, then a second one before it
	alex.want("PUT", fmt.Sprintf("/api/v1/worktrees/%d/group", a1), map[string]any{"group_id": g1.ID}, 204, nil)
	alex.want("PUT", fmt.Sprintf("/api/v1/worktrees/%d/group", a2), map[string]any{"group_id": g1.ID, "order": []int64{a2, a1}}, 204, nil)
	// someone else's worktree or group: not found, and it is not slipped in through order
	alex.want("PUT", fmt.Sprintf("/api/v1/worktrees/%d/group", b1), map[string]any{"group_id": g1.ID}, 404, nil)
	ben.want("PUT", fmt.Sprintf("/api/v1/worktrees/%d/group", b1), map[string]any{"group_id": g1.ID}, 404, nil)
	alex.want("PUT", fmt.Sprintf("/api/v1/worktrees/%d/group", a3), map[string]any{"group_id": g2.ID, "order": []int64{b1}}, 204, nil)
	var list []store.WorktreeGroup
	alex.want("GET", "/api/v1/worktree-groups", nil, 200, &list)
	if len(list) != 2 || fmt.Sprint(list[0].Items) != fmt.Sprint([]int64{a2, a1}) || fmt.Sprint(list[1].Items) != fmt.Sprint([]int64{a3}) {
		t.Fatalf("after placing: %+v", list)
	}
	// reorder, rename, fold, move out, delete
	alex.want("PUT", "/api/v1/worktree-groups/order", map[string]any{"ids": []int64{g2.ID, g1.ID}}, 204, nil)
	alex.want("PATCH", fmt.Sprintf("/api/v1/worktree-groups/%d", g1.ID), map[string]any{"name": "Ship", "collapsed": true}, 204, nil)
	ben.want("PATCH", fmt.Sprintf("/api/v1/worktree-groups/%d", g1.ID), map[string]any{"name": "Mine"}, 404, nil)
	alex.want("PUT", fmt.Sprintf("/api/v1/worktrees/%d/group", a1), map[string]any{"group_id": 0}, 204, nil)
	alex.want("GET", "/api/v1/worktree-groups", nil, 200, &list)
	if list[0].ID != g2.ID || list[1].Name != "Ship" || !list[1].Collapsed || fmt.Sprint(list[1].Items) != fmt.Sprint([]int64{a2}) {
		t.Fatalf("after changes: %+v", list)
	}
	ben.want("DELETE", fmt.Sprintf("/api/v1/worktree-groups/%d", g1.ID), nil, 404, nil)
	alex.want("DELETE", fmt.Sprintf("/api/v1/worktree-groups/%d", g1.ID), nil, 204, nil)
	alex.want("GET", "/api/v1/worktree-groups", nil, 200, &list)
	if len(list) != 1 {
		t.Fatalf("after delete: %+v", list)
	}
	var bl []store.WorktreeGroup
	ben.want("GET", "/api/v1/worktree-groups", nil, 200, &bl)
	if len(bl) != 0 {
		t.Fatalf("ben sees: %+v", bl)
	}

	// groups inside groups
	var top, mid, leaf store.WorktreeGroup
	alex.want("POST", "/api/v1/worktree-groups", map[string]any{"name": "Top"}, 201, &top)
	alex.want("POST", "/api/v1/worktree-groups", map[string]any{"name": "Mid", "parent_id": top.ID}, 201, &mid)
	alex.want("POST", "/api/v1/worktree-groups", map[string]any{"name": "Leaf", "parent_id": mid.ID}, 201, &leaf)
	ben.want("POST", "/api/v1/worktree-groups", map[string]any{"name": "Sneaky", "parent_id": top.ID}, 404, nil)
	if mid.ParentID == nil || *mid.ParentID != top.ID || leaf.Position != 0 {
		t.Fatalf("nested: %+v %+v", mid, leaf)
	}
	// never inside itself or its own groups
	alex.want("PUT", fmt.Sprintf("/api/v1/worktree-groups/%d/move", top.ID), map[string]any{"parent_id": leaf.ID}, 409, nil)
	alex.want("PUT", fmt.Sprintf("/api/v1/worktree-groups/%d/move", top.ID), map[string]any{"parent_id": top.ID}, 409, nil)
	// a group moves in and out
	alex.want("PUT", fmt.Sprintf("/api/v1/worktree-groups/%d/move", g2.ID), map[string]any{"parent_id": top.ID, "order": []int64{g2.ID, mid.ID}}, 204, nil)
	alex.want("PUT", fmt.Sprintf("/api/v1/worktrees/%d/group", a1), map[string]any{"group_id": mid.ID}, 204, nil)
	alex.want("GET", "/api/v1/worktree-groups", nil, 200, &list)
	find := func(id int64) store.WorktreeGroup {
		for _, g := range list {
			if g.ID == id {
				return g
			}
		}
		t.Fatalf("group %d missing", id)
		return store.WorktreeGroup{}
	}
	if g := find(g2.ID); g.ParentID == nil || *g.ParentID != top.ID || g.Position != 0 || find(mid.ID).Position != 1 {
		t.Fatalf("after move: %+v", list)
	}
	// deleting Mid: Leaf and its worktree move up into Top
	alex.want("DELETE", fmt.Sprintf("/api/v1/worktree-groups/%d", mid.ID), nil, 204, nil)
	alex.want("GET", "/api/v1/worktree-groups", nil, 200, &list)
	if l := find(leaf.ID); l.ParentID == nil || *l.ParentID != top.ID || fmt.Sprint(find(top.ID).Items) != fmt.Sprint([]int64{a1}) {
		t.Fatalf("after deleting mid: %+v", list)
	}
	// deleting Top: its groups go to the top, its worktrees back to their repository
	alex.want("DELETE", fmt.Sprintf("/api/v1/worktree-groups/%d", top.ID), nil, 204, nil)
	alex.want("GET", "/api/v1/worktree-groups", nil, 200, &list)
	if l := find(leaf.ID); l.ParentID != nil || find(g2.ID).ParentID != nil {
		t.Fatalf("after deleting top: %+v", list)
	}
	for _, g := range list {
		for _, i := range g.Items {
			if i == a1 {
				t.Fatalf("a1 still grouped: %+v", list)
			}
		}
	}
}
