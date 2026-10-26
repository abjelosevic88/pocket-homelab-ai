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
}
