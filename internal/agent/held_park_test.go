package agent

import (
	"context"
	"testing"
	"time"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/hold"
)

// An idle held agent's connection is let go and taken back: by a message, or
// by Wake when it spoke on its own meanwhile; nothing it said is lost or
// read twice, nobody is told of a lost link, and closing it while parked
// still ends the CLI.
func TestHeldPark(t *testing.T) {
	bin, holdBin := heldSetup(t)
	const slot = "thread-park"
	o := Options{Bin: bin, Cwd: t.TempDir(), Mode: "default", Hold: &Hold{Bin: holdBin, Slot: slot, Leave: make(chan struct{})}}
	r := &heldRec{rec: newRec()}
	s, err := StartClaude(context.Background(), o, r)
	if err != nil {
		t.Fatal(err)
	}
	p := s.(Parkable)
	l := s.(*claudeSession).l
	resting := func() int64 {
		t.Helper()
		for i := 0; i < 250; i++ {
			if off, ok := p.Parked(); !ok {
				t.Fatal("not parked")
			} else if off >= 0 {
				return off
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("the connection did not end")
		return 0
	}
	status := func() hold.Header {
		t.Helper()
		hd, ok, err := hold.Status(slot)
		if err != nil || !ok || hd.Exited {
			t.Fatalf("the CLI is not held any more: %+v %v %v", hd, ok, err)
		}
		return hd
	}
	if _, ok := p.Parked(); ok {
		t.Fatal("parked from the start")
	}
	if err := s.Send(Input{Text: "one"}); err != nil {
		t.Fatal(err)
	}
	r.wait(t, func() bool { return len(r.turns) == 1 })

	// idle: the connection goes, the CLI stays, and the offsets agree
	if !p.Park() || p.Park() {
		t.Fatal("Park: true once, then false")
	}
	off := resting()
	l.wmu.Lock()
	gone := l.waited && l.down
	l.wmu.Unlock()
	if hd := status(); !gone || hd.Offset != off || hd.Busy {
		t.Fatalf("parked: connection gone %v, read %d, holder %+v", gone, off, hd)
	}

	// a message brings it back by itself
	if err := s.Send(Input{Text: "two"}); err != nil {
		t.Fatal(err)
	}
	r.wait(t, func() bool { return len(r.turns) == 2 && r.has("second 2") })
	if _, ok := p.Parked(); ok || p.Wake() {
		t.Fatal("still parked after a message")
	}

	// it speaks while parked (here: the rest of a turn): the holder's offset
	// passes what was read, and Wake delivers exactly the rest
	if err := s.Send(Input{Text: "three"}); err != nil {
		t.Fatal(err)
	}
	r.wait(t, func() bool { return r.has("first 3") })
	if !p.Park() {
		t.Fatal("Park mid-turn")
	}
	off = resting()
	for i := 0; ; i++ {
		if hd := status(); !hd.Busy && hd.Offset > off {
			break
		}
		if i > 200 {
			t.Fatalf("the turn did not end under the holder: %+v", status())
		}
		time.Sleep(20 * time.Millisecond)
	}
	r.mu.Lock()
	early := r.has("second 3") || len(r.turns) != 2
	r.mu.Unlock()
	if early {
		t.Fatal("output arrived without a connection")
	}
	if !p.Wake() || p.Wake() {
		t.Fatal("Wake: true once, then false")
	}
	r.wait(t, func() bool { return len(r.turns) == 3 && r.has("second 3") })
	r.mu.Lock()
	items, links := len(r.order), len(r.links)
	r.mu.Unlock()
	if items != 6 || links != 0 {
		t.Fatalf("%d items (want 6: nothing twice), link reports %d (want none)", items, links)
	}
	l.wmu.Lock()
	back := !l.down && !l.waited && !l.let
	l.wmu.Unlock()
	if hd := status(); !back || hd.Offset != l.off {
		t.Fatalf("after waking: connected %v, read %d, holder %+v", back, l.off, hd)
	}

	// closed while parked: the CLI is ended over a connection of its own
	if !p.Park() {
		t.Fatal("Park again")
	}
	resting()
	_ = s.Close()
	select {
	case err := <-r.exited:
		if err != nil {
			t.Fatalf("exit: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("did not end")
	}
	for i := 0; ; i++ {
		if _, ok, _ := hold.Status(slot); !ok {
			break
		}
		if i > 300 {
			t.Fatal("the CLI is still held after Close")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// A parked agent is let go of like any other when Pocket AI stops: no exit
// is reported, and it runs on for the next one.
func TestHeldParkLeave(t *testing.T) {
	bin, holdBin := heldSetup(t)
	const slot = "thread-park-leave"
	leave := make(chan struct{})
	o := Options{Bin: bin, Cwd: t.TempDir(), Mode: "default", Hold: &Hold{Bin: holdBin, Slot: slot, Leave: leave}}
	r := &heldRec{rec: newRec()}
	s, err := StartClaude(context.Background(), o, r)
	if err != nil {
		t.Fatal(err)
	}
	p := s.(Parkable)
	if !p.Park() {
		t.Fatal("Park")
	}
	for i := 0; ; i++ {
		if off, _ := p.Parked(); off >= 0 {
			break
		}
		if i > 250 {
			t.Fatal("the connection did not end")
		}
		time.Sleep(20 * time.Millisecond)
	}
	close(leave)
	select {
	case <-s.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("did not let go")
	}
	select {
	case err := <-r.exited:
		t.Fatalf("reported an exit on letting go: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	if hd, ok, _ := hold.Status(slot); !ok || hd.Exited {
		t.Fatalf("holder after letting go: %+v %v", hd, ok)
	}
	_ = hold.Stop(slot)
}
