package server

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

// parkSession is a held agent on another machine, as far as parking goes.
type parkSession struct {
	agent.Session
	mu     sync.Mutex
	parked bool
	off    int64
	parks  int
	wakes  int
}

func (s *parkSession) Park() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.parked {
		return false
	}
	s.parked = true
	s.parks++
	return true
}

func (s *parkSession) Wake() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.parked {
		return false
	}
	s.parked = false
	s.wakes++
	return true
}

func (s *parkSession) Parked() (int64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.off, s.parked
}

func (s *parkSession) state() (parked bool, parks, wakes int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.parked, s.parks, s.wakes
}

func TestParkStir(t *testing.T) {
	for status, want := range map[string]bool{
		`{"type":"pocket_hold","pid":7,"offset":100,"in":5}`:                 false,
		`{"type":"pocket_hold","pid":7,"offset":101,"in":5}`:                 true, // it said something
		`{"type":"pocket_hold","pid":7,"busy":true,"offset":100,"in":9}`:     true, // a turn began
		`{"type":"pocket_hold","pid":7,"offset":100,"in":5,"exited":true}`:   true,
		`{"type":"pocket_hold","pid":7,"offset":100,"in":5,"stopping":true}`: true,
		`{"gone":true,"offset":0,"in":0}`:                                    true,
		``:                                                                   true, // pocket-hold could not be run
		`{"type":"pocket_hold","pid":7,"offset":100,"in":5`:                  true,
	} {
		if got := parkStir(status, 100); got != want {
			t.Errorf("parkStir(%q) = %v, want %v", status, got, want)
		}
	}
}

// An agent on another machine with nothing going on gives up its connection
// after PARK_AFTER; one connection per machine watches the holders of those
// parked there, and the agent's connection is asked back when it stirs, when
// its thread is opened, and never let go while someone looks or it works.
func TestParkIdleAgents(t *testing.T) {
	oldPoll := parkPoll
	parkPoll = 100 * time.Millisecond
	defer func() { parkPoll = oldPoll }()
	s, h, root := testServer(t)
	ctx := context.Background()
	_, u := person(t, s, h, "park@example.test", "admin", true)
	s.cfg.ParkAfter = time.Minute
	t.Setenv("POCKET_HOLD", "") // the test server runs agents unheld
	box := &store.Host{Name: "vm", Target: "vm.invalid"}
	if err := s.store.CreateHost(ctx, box); err != nil {
		t.Fatal(err)
	}
	holders.mu.Lock()
	holders.at[box.ID] = holderPath{"/opt/pocket-hold", time.Now()}
	holders.mu.Unlock()
	defer forgetHolder(box.ID)

	// Fake ssh: the watch script's connection. It prints what the file says
	// of each slot, round after round, and counts its connections.
	dir := t.TempDir()
	script := `#!/usr/bin/env python3
import sys,base64,re,os,time,json
args=sys.argv[1:]
assert args[args.index('--')-1]=='vm.invalid'
cmd=base64.b64decode(re.search(r'echo ([A-Za-z0-9+/=]+)',args[-1]).group(1)).decode()
if 'status "$s"' not in cmd: sys.exit(3)
d=` + "'" + dir + "'" + `
open(os.path.join(d,'conns'),'a').write('x')
m=re.search(r"set -- /opt/pocket-hold (\S+) (.*)\n", cmd)
assert m and m.group(1)=='0.1', cmd
slots=m.group(2).split()
open(os.path.join(d,'asked'),'w').write(' '.join(sorted(slots)))
while True:
    try: st=json.load(open(os.path.join(d,'status')))
    except Exception: st={}
    for s in slots: print(s, st.get(s,'{"gone":true,"offset":0,"in":0}'), flush=True)
    print('@', flush=True)
    time.sleep(0.1)
`
	fake := filepath.Join(t.TempDir(), "ssh")
	if err := os.WriteFile(fake, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Dir(fake)+string(os.PathListSeparator)+os.Getenv("PATH"))
	setStatus := func(m map[string]string) {
		t.Helper()
		b := "{"
		for k, v := range m {
			if len(b) > 1 {
				b += ","
			}
			b += `"` + k + `":` + `"` + jsonEscape(v) + `"`
		}
		tmp := filepath.Join(dir, "status.tmp")
		if err := os.WriteFile(tmp, []byte(b+"}"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(tmp, filepath.Join(dir, "status")); err != nil {
			t.Fatal(err)
		}
	}
	read := func(name string) string { b, _ := os.ReadFile(filepath.Join(dir, name)); return string(b) }
	waitFor := func(what string, cond func() bool) {
		t.Helper()
		for i := 0; !cond(); i++ {
			if i > 250 {
				t.Fatalf("timed out waiting for %s (asked %q, connections %q)", what, read("asked"), read("conns"))
			}
			time.Sleep(20 * time.Millisecond)
		}
	}

	long := time.Now().Add(-time.Hour)
	add := func(held bool, host int64) (*store.Thread, *run, *parkSession) {
		th := thread(t, s, u.ID, root)
		sess := &parkSession{off: 100}
		r := &run{threadID: th.ID, userID: u.ID, sess: sess, host: host, held: held, lastUsed: long, pending: map[string]bool{}, live: map[string]*agent.Item{}, dirty: map[string]bool{}}
		s.rt.mu.Lock()
		s.rt.runs[th.ID] = r
		s.rt.mu.Unlock()
		t.Cleanup(func() { s.rt.mu.Lock(); delete(s.rt.runs, th.ID); s.rt.mu.Unlock() })
		return th, r, sess
	}
	quietTh, _, quietS := add(true, box.ID)
	_, busy, busyS := add(true, box.ID)
	busy.running = true
	_, asked, askedS := add(true, box.ID)
	asked.pending["req"] = true
	_, fresh, freshS := add(true, box.ID)
	fresh.lastUsed = time.Now()
	seenTh, _, seenS := add(true, box.ID)
	seen := s.rt.subscribeThread(seenTh.ID, u.ID)
	_, _, plainS := add(false, box.ID) // not under a holder: its CLI would die with the connection
	_, _, localS := add(true, 0)       // on this server: no ssh connection to save
	secondTh, second, secondS := add(true, box.ID)
	second.lastUsed = time.Now()

	idle := `{"type":"pocket_hold","pid":7,"offset":100,"in":5}`
	setStatus(map[string]string{quietTh.ID: idle, secondTh.ID: idle})
	s.rt.parkIdle()
	if p, n, _ := quietS.state(); !p || n != 1 {
		t.Fatalf("the quiet agent was not parked: %v %d", p, n)
	}
	for name, x := range map[string]*parkSession{"working": busyS, "asked something": askedS, "just used": freshS, "looked at": seenS, "not held": plainS, "local": localS, "second": secondS} {
		if p, _, _ := x.state(); p {
			t.Fatalf("parked an agent that is %s", name)
		}
	}
	waitFor("the watch", func() bool { return read("asked") == quietTh.ID })
	time.Sleep(400 * time.Millisecond) // rounds of "nothing new"
	if _, _, w := quietS.state(); w != 0 || read("conns") != "x" {
		t.Fatalf("woken for nothing (%d), connections %q", w, read("conns"))
	}

	// another one goes quiet: the same machine's watch asks about both, over
	// one connection at a time
	second.mu.Lock()
	second.lastUsed = long
	second.mu.Unlock()
	s.rt.parkIdle()
	want := quietTh.ID + " " + secondTh.ID
	if secondTh.ID < quietTh.ID {
		want = secondTh.ID + " " + quietTh.ID
	}
	waitFor("the watch of both", func() bool { return read("asked") == want })

	// one stirs (a message from the Claude app): only that one is asked back
	setStatus(map[string]string{quietTh.ID: `{"type":"pocket_hold","pid":7,"busy":true,"offset":180,"in":60}`, secondTh.ID: idle})
	waitFor("the stirring agent's connection", func() bool { p, _, w := quietS.state(); return !p && w == 1 })
	if p, _, w := secondS.state(); !p || w != 0 {
		t.Fatalf("the other one was woken too: %v %d", p, w)
	}
	// just woken: not parked again at once, though its last turn is old
	s.rt.parkIdle()
	if p, n, _ := quietS.state(); p || n != 1 {
		t.Fatalf("parked again right after waking: %v %d", p, n)
	}

	// opening the thread brings the connection back before a message does
	ch := s.rt.subscribeThread(secondTh.ID, u.ID)
	if p, _, w := secondS.state(); p || w != 1 {
		t.Fatalf("opening the thread did not wake it: %v %d", p, w)
	}
	s.rt.unsubscribeThread(secondTh.ID, ch)
	s.rt.unsubscribeThread(seenTh.ID, seen)
	// nobody parked there: the watch ends
	waitFor("the watch to end", func() bool {
		s.rt.parkMu.Lock()
		defer s.rt.parkMu.Unlock()
		return !s.rt.parkWatch[box.ID]
	})

	// parked for long: attached again, so its holder does not end it
	second.mu.Lock()
	second.lastUsed, second.wokeAt = long, long
	second.mu.Unlock()
	s.rt.parkIdle()
	if p, n, _ := secondS.state(); !p || n != 2 {
		t.Fatalf("not parked again: %v %d", p, n)
	}
	second.mu.Lock()
	second.parkedAt = time.Now().Add(-parkTouch - time.Minute)
	second.mu.Unlock()
	s.rt.parkIdle()
	if p, _, w := secondS.state(); p || w != 2 {
		t.Fatalf("a long-parked agent was not attached again: %v %d", p, w)
	}

	// PARK_AFTER=0: every agent keeps its connection
	s.cfg.ParkAfter = 0
	second.mu.Lock()
	second.lastUsed, second.wokeAt = long, long
	second.mu.Unlock()
	s.rt.parkIdle()
	if p, _, _ := secondS.state(); p {
		t.Fatal("parked with parking off")
	}
}

func jsonEscape(s string) string {
	out := ""
	for _, c := range s {
		if c == '"' || c == '\\' {
			out += `\`
		}
		out += string(c)
	}
	return out
}
