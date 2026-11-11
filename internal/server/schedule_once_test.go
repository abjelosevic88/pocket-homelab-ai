package server

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

func TestOnceSpecs(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Sarajevo")
	now := time.Date(2026, 11, 11, 9, 38, 20, 0, loc)
	cases := map[string]string{
		"in 2h":                 "once 2026-11-11 11:39",
		"in 30 minutes":         "once 2026-11-11 10:09",
		"in 1h30m":              "once 2026-11-11 11:09",
		"in 3d":                 "once 2026-11-14 09:39",
		"today 17:00":           "once 2026-11-11 17:00",
		"Tomorrow 09:00":        "once 2026-11-12 09:00",
		"once 2026-12-01T08:15": "once 2026-12-01 08:15",
		"once 2026-12-01 08:15": "once 2026-12-01 08:15",
		"daily 08:00":           "daily 08:00",
	}
	for in, want := range cases {
		got, err := NormalizeSpec(in, now, loc)
		if err != nil || got != want {
			t.Errorf("%q: got %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"in soon", "in 10s", "tomorrow 9am", "today"} {
		if _, err := NormalizeSpec(bad, now, loc); err == nil {
			t.Errorf("%q normalized", bad)
		}
	}
	sp, err := ParseSpec("once 2026-11-12 09:00")
	if err != nil || !sp.Once() {
		t.Fatal(sp, err)
	}
	if n := sp.Next(now, loc); n.In(loc).Format("2006-01-02 15:04") != "2026-11-12 09:00" {
		t.Fatal(n)
	}
	if n := sp.Next(time.Date(2026, 11, 12, 9, 0, 0, 0, loc), loc); !n.IsZero() {
		t.Fatal("ran twice", n)
	}
	for _, bad := range []string{"once 2026-13-01 09:00", "once tomorrow", "in 2h"} {
		if _, err := ParseSpec(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}

// A one-time schedule from the page: relative times become a date, the past is
// refused, and after its run it is off and does not run again.
func TestOnceScheduleAPI(t *testing.T) {
	s, h, root := testServer(t)
	c, u := person(t, s, h, "once@example.test", "admin", true)
	ctx := context.Background()
	c.want("POST", "/api/v1/schedules", map[string]any{"name": "Past", "prompt": "x", "spec": "once 2001-01-01 09:00", "cwd": root}, 400, nil)
	var sc store.Schedule
	c.want("POST", "/api/v1/schedules", map[string]any{"name": "Later", "prompt": "check it", "spec": "in 2h", "cwd": root}, 201, &sc)
	if !strings.HasPrefix(sc.Spec, "once ") || sc.NextRunAt == "" || !sc.Enabled {
		t.Fatalf("%+v", sc)
	}
	if d := time.Until(store.ParseTime(sc.NextRunAt)); d < 119*time.Minute || d > 122*time.Minute {
		t.Fatalf("next in %v", d)
	}
	var pv map[string]any
	c.want("GET", "/api/v1/schedules/preview?spec=tomorrow+09:00", nil, 200, &pv)
	if pv["ok"] != true || pv["once"] != true || len(pv["next"].([]any)) != 1 {
		t.Fatalf("preview %v", pv)
	}
	got, _ := s.store.GetSchedule(ctx, u.ID, sc.ID)
	_, _ = s.runSchedule(ctx, got)
	got, _ = s.store.GetSchedule(ctx, u.ID, sc.ID)
	if got.Enabled || got.NextRunAt != "" || got.LastRunAt == "" {
		t.Fatalf("after its run: %+v", got)
	}
	if due, _ := s.store.DueSchedules(ctx, store.FormatTime(time.Now().Add(24*time.Hour))); len(due) != 0 {
		t.Fatalf("due again: %d", len(due))
	}
	var list map[string]any
	c.want("GET", "/api/v1/heartbeats", nil, 200, &list)
}

// An agent schedules a one-time run from its chat: approved, on the
// Schedules page with where it came from, and its run reports back.
func TestAgentScheduleOnce(t *testing.T) {
	s, h, root := testServer(t)
	c, u := person(t, s, h, "later@example.test", "admin", true)
	fakeClaude(t, s, root)
	script := `#!/usr/bin/env python3
import sys, json
if sys.argv[1:2] == ['auth']:
    print('{}')
    sys.exit(0)
for line in sys.stdin:
    if json.loads(line).get('type') != 'user':
        continue
    print(json.dumps({'type':'system','subtype':'init','session_id':'later-sess'}), flush=True)
    print(json.dumps({'type':'assistant','message':{'id':'a1','content':[{'type':'text','text':'OK: the backup finished'}]}}), flush=True)
    print(json.dumps({'type':'result','subtype':'success','is_error':False,'usage':{'input_tokens':1,'output_tokens':1}}), flush=True)
`
	if err := os.WriteFile(s.cfg.ClaudeBin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	parent, r, sess, tools := agentParent(t, s, u, root, "default")
	ctx := context.Background()
	if out := tools.call("pocket_agent_schedule_create", map[string]any{"prompt": "x", "when": "once 2001-01-01 09:00"}, true); !strings.Contains(out["error"].(string), "passed") {
		t.Fatalf("past: %v", out)
	}
	res := make(chan map[string]any, 1)
	go func() {
		res <- tools.call("pocket_agent_schedule_create", map[string]any{"prompt": "See if the backup finished", "when": "tomorrow 09:00"})
	}()
	rid := pendingRequest(t, r)
	c.want("POST", "/api/v1/threads/"+parent.ID+"/requests/"+rid, map[string]string{"decision": "allow"}, 204, nil)
	out := <-res
	if out["once"] != true || out["reports_back"] != true {
		t.Fatalf("%v", out)
	}
	id := int64(out["schedule_id"].(float64))
	sc, err := s.store.GetSchedule(ctx, u.ID, id)
	if err != nil || sc.FromThreadID != parent.ID || !sc.Report || sc.Notify != "always" || sc.Cwd != parent.Cwd || sc.Mode != "default" {
		t.Fatalf("%+v %v", sc, err)
	}
	list := tools.call("pocket_agent_schedules", map[string]any{})
	if rows := list["schedules"].([]any); len(rows) != 1 || rows[0].(map[string]any)["from_this_conversation"] != true {
		t.Fatalf("%v", list)
	}
	_, _ = s.store.DB.Exec(`UPDATE threads SET status = 'idle' WHERE id = ?`, parent.ID)
	if run, err := s.runSchedule(ctx, sc); run == nil || err != nil {
		t.Fatal("no run", err)
	}
	select {
	case in := <-sess.sent:
		if !strings.Contains(in.Text, "<pocket-agent-report") || !strings.Contains(in.Text, "The scheduled run") || !strings.Contains(in.Text, "backup finished") {
			t.Fatalf("report %q", in.Text)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the run did not report back")
	}
	if got, _ := s.store.GetSchedule(ctx, u.ID, id); got.Enabled || got.LastStatus != "ok" {
		t.Fatalf("after the run: %+v", got)
	}
	// another conversation's schedule cannot be removed from here
	var other store.Schedule
	c.want("POST", "/api/v1/schedules", map[string]any{"name": "Mine", "prompt": "x", "spec": "daily 08:00", "cwd": root}, 201, &other)
	if out := tools.call("pocket_agent_schedule_delete", map[string]any{"id": other.ID}, true); !strings.Contains(out["error"].(string), "Schedules page") {
		t.Fatalf("%v", out)
	}
	if out := tools.call("pocket_agent_schedule_delete", map[string]any{"id": id}); out["deleted"] == nil {
		t.Fatalf("%v", out)
	}
}

// A one-time heartbeat runs once and is gone; while busy it waits a minute.
func TestHeartbeatOnce(t *testing.T) {
	s, h, root := testServer(t)
	c, u := person(t, s, h, "hbonce@example.test", "admin", true)
	parent, r, sess, tools := agentParent(t, s, u, root, "default")
	ctx := context.Background()
	res := make(chan map[string]any, 1)
	go func() {
		res <- tools.call("pocket_agent_heartbeat_create", map[string]any{"prompt": "Look again", "spec": "in 45m"})
	}()
	rid := pendingRequest(t, r)
	c.want("POST", "/api/v1/threads/"+parent.ID+"/requests/"+rid, map[string]string{"decision": "allow"}, 204, nil)
	hb := (<-res)["heartbeat"].(map[string]any)
	id := int64(hb["id"].(float64))
	if !strings.HasPrefix(hb["spec"].(string), "once ") || hb["ends_at"] != "" {
		t.Fatalf("%v", hb)
	}
	due := func() {
		_, _ = s.store.DB.Exec(`UPDATE heartbeats SET next_at = '2000-01-01T00:00:00Z' WHERE id = ?`, id)
	}
	_, _ = s.store.DB.Exec(`UPDATE threads SET status = 'running' WHERE id = ?`, parent.ID)
	s.rt.setStatus(parent.ID, "running", "")
	due()
	s.runHeartbeats(ctx)
	list, _ := s.store.ThreadHeartbeats(ctx, parent.ID)
	if len(list) != 1 || list[0].Runs != 0 || time.Until(store.ParseTime(list[0].NextAt)) > 2*time.Minute {
		t.Fatalf("busy: %+v", list)
	}
	s.rt.setStatus(parent.ID, "idle", "")
	_, _ = s.store.DB.Exec(`UPDATE threads SET status = 'idle' WHERE id = ?`, parent.ID)
	due()
	s.runHeartbeats(ctx)
	select {
	case in := <-sess.sent:
		if !strings.Contains(in.Text, "Look again") {
			t.Fatal(in.Text)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("not sent")
	}
	if list, _ := s.store.ThreadHeartbeats(ctx, parent.ID); len(list) != 0 {
		t.Fatalf("still there: %+v", list)
	}
}
