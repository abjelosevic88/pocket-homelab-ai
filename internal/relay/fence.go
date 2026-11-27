package relay

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"syscall"
	"time"
)

// The fence (docs/machine-link-impl.md, "Fence") decides on the machine which
// relay may run for an installation, and outlives every relay: a stale start,
// a delayed cleanup or an old controller finds a higher generation and is
// refused even when no relay runs to say no.
type Fence struct {
	Installation string `json:"installation"`
	Entry        int64  `json:"entry"`
	Generation   int64  `json:"generation"`
	Off          bool   `json:"off"`
	PID          int    `json:"pid"`
	Token        string `json:"token"`
	Ports        []int  `json:"ports"`
	Updated      string `json:"updated"`
}

var (
	slotRE  = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	tokenRE = regexp.MustCompile(`^[A-Za-z0-9_-]{8,128}$`)
)

// Takeover bounds: SIGTERM, SIGKILL after this, everything gone by the hard limit.
const (
	takeoverGrace = 5 * time.Second
	takeoverLimit = 15 * time.Second
)

type fenceFile struct {
	dir, inst string
	lock      *os.File
}

func fenceDir(home string) string { return filepath.Join(home, ".pocket-ai", "relay") }

// lockFence takes the installation's flock (waiting up to wait).
func lockFence(home, inst string, wait time.Duration) (*fenceFile, error) {
	if !slotRE.MatchString(inst) {
		return nil, fmt.Errorf("bad installation %q", inst)
	}
	dir := fenceDir(home)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	// the lock file stays: removing it would let two holders lock two files
	l, err := os.OpenFile(filepath.Join(dir, inst+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	end := time.Now().Add(wait)
	for syscall.Flock(int(l.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		if time.Now().After(end) {
			l.Close()
			return nil, errors.New("the fence is locked by another action")
		}
		time.Sleep(50 * time.Millisecond)
	}
	return &fenceFile{dir: dir, inst: inst, lock: l}, nil
}

func (f *fenceFile) unlock() { _ = f.lock.Close() } // closing drops the flock

func (f *fenceFile) path() string { return filepath.Join(f.dir, f.inst+".fence") }

// read returns the fence; ok is false when there is none yet.
func (f *fenceFile) read() (Fence, bool, error) {
	var fe Fence
	b, err := os.ReadFile(f.path())
	if errors.Is(err, os.ErrNotExist) {
		return fe, false, nil
	}
	if err != nil {
		return fe, false, err
	}
	if len(b) == 0 {
		return fe, false, nil
	}
	if err := json.Unmarshal(b, &fe); err != nil {
		return fe, false, fmt.Errorf("fence %s: %v", f.path(), err)
	}
	return fe, true, nil
}

func (f *fenceFile) write(fe Fence) error {
	fe.Installation = f.inst
	fe.Updated = time.Now().UTC().Format(time.RFC3339)
	if fe.Ports == nil {
		fe.Ports = []int{}
	}
	b, _ := json.Marshal(fe)
	tmp := f.path() + ".part"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, f.path())
}

// otherEntry: the fence belongs to another machine entry of this installation.
func otherEntry(fe Fence, have bool, entry int64) bool {
	return have && fe.Entry != 0 && fe.Entry != entry
}

func refuse(reason string, fe Fence, format string, a ...any) *Refused {
	return &Refused{Reason: reason, Entry: fe.Entry, Generation: fe.Generation, Msg: fmt.Sprintf(format, a...)}
}

// fenceStart is a run-mode relay's start: it claims the fence for
// (entry, gen) and ends the relay recorded before it, so that nothing of the
// old one is left when this one answers. A *Refused comes back when it may not run.
func fenceStart(home, inst string, entry, gen int64, token string) error {
	if !tokenRE.MatchString(token) {
		return errors.New("bad token")
	}
	f, err := lockFence(home, inst, 10*time.Second)
	if err != nil {
		return &Refused{Reason: "busy", Msg: err.Error()}
	}
	fe, have, err := f.read()
	if err != nil {
		f.unlock()
		return err
	}
	if otherEntry(fe, have, entry) {
		f.unlock()
		return refuse("other-entry", fe, "machine entry %d runs the link for this account", fe.Entry)
	}
	if gen <= fe.Generation {
		f.unlock()
		reason := "newer"
		if fe.Off {
			reason = "off"
		}
		return refuse(reason, fe, "generation %d is not above the fence's %d", gen, fe.Generation)
	}
	old := fe
	fe.Entry, fe.Generation, fe.Off = entry, gen, false
	fe.PID, fe.Token, fe.Ports = os.Getpid(), token, nil
	if err := f.write(fe); err != nil {
		f.unlock()
		return err
	}
	f.unlock()

	// end the previous relay outside the lock (it clears its own record
	// under it when it exits)
	start := time.Now()
	if err := endRelay(old.PID, old.Token, old.Ports, start.Add(takeoverLimit)); err != nil {
		// put the old one back so a later, higher start ends it; the
		// generation stays ours
		if f, lerr := lockFence(home, inst, 5*time.Second); lerr == nil {
			if cur, ok, _ := f.read(); ok && cur.Token == token {
				cur.PID, cur.Token, cur.Ports = old.PID, old.Token, old.Ports
				_ = f.write(cur)
			}
			f.unlock()
		}
		return refuse("busy", fe, "the previous relay is still there: %v", err)
	}
	return nil
}

// endRelay ends the relay pid if it still is the one with token, and waits
// (until deadline) for it to be gone and its ports free.
func endRelay(pid int, token string, ports []int, deadline time.Time) error {
	if pid > 0 && token != "" {
		still := func() bool { return isRelay(pid, token) }
		if err := endProcess(pid, still, takeoverGrace, deadline); err != nil {
			return err
		}
	}
	if held := waitPortsFree(ports, deadline); len(held) > 0 {
		return fmt.Errorf("ports still held: %s", intsString(held))
	}
	return nil
}

// fenceUpdate rewrites the fence with fn when it still records token (this
// relay); a successor's record is never touched.
func fenceUpdate(home, inst, token string, fn func(*Fence)) {
	f, err := lockFence(home, inst, 5*time.Second)
	if err != nil {
		return
	}
	defer f.unlock()
	fe, have, err := f.read()
	if err != nil || !have || fe.Token != token {
		return
	}
	fn(&fe)
	_ = f.write(fe)
}

// fenceAction runs a `pocket-hold fence` action. out gets the fence for show.
// The decision and the fence write happen under the lock; ending a relay
// happens after it, because the relay takes the lock to clear its record on
// the way out.
func fenceAction(home, inst string, entry, gen int64, action string, args []string, out *Fence) error {
	then, err := fenceDecide(home, inst, entry, gen, action, args, out)
	if err != nil || then == nil {
		return err
	}
	return then()
}

func fenceDecide(home, inst string, entry, gen int64, action string, args []string, out *Fence) (func() error, error) {
	f, err := lockFence(home, inst, 20*time.Second)
	if err != nil {
		return nil, err
	}
	defer f.unlock()
	fe, have, err := f.read()
	if err != nil {
		return nil, err
	}
	if action == "show" {
		fe.Installation = inst
		*out = fe
		return nil, nil
	}
	// a fenced action of another entry never touches this entry's relay
	if otherEntry(fe, have, entry) {
		return nil, refuse("other-entry", fe, "machine entry %d runs the link for this account", fe.Entry)
	}
	stale := func() error {
		if gen < fe.Generation {
			reason := "newer"
			if fe.Off {
				reason = "off"
			}
			return refuse(reason, fe, "generation %d is below the fence's %d", gen, fe.Generation)
		}
		return nil
	}
	// ending: the record is cleared first (nothing may start the old relay's
	// ports again meanwhile), and put back when it cannot be ended, so a
	// later action tries again
	ending := func(old Fence) func() error {
		return func() error {
			err := endRelay(old.PID, old.Token, old.Ports, time.Now().Add(takeoverLimit))
			if err == nil {
				return nil
			}
			if f, lerr := lockFence(home, inst, 5*time.Second); lerr == nil {
				if cur, ok, _ := f.read(); ok && cur.PID == 0 && cur.Generation == fe.Generation {
					cur.PID, cur.Token, cur.Ports = old.PID, old.Token, old.Ports
					_ = f.write(cur)
				}
				f.unlock()
			}
			return refuse("busy", fe, "%v", err)
		}
	}
	switch action {
	case "check":
		return nil, stale()
	case "end-relay":
		if err := stale(); err != nil {
			return nil, err
		}
		if !have || fe.PID == 0 {
			return nil, nil
		}
		old := fe
		fe.PID, fe.Token, fe.Ports = 0, "", nil
		if err := f.write(fe); err != nil {
			return nil, err
		}
		return ending(old), nil
	case "off":
		if gen <= fe.Generation {
			return nil, refuse("newer", fe, "generation %d is not above the fence's %d", gen, fe.Generation)
		}
		// advance first: whatever comes later with an older generation is refused
		old := fe
		fe.Entry, fe.Generation, fe.Off = entry, gen, true
		fe.PID, fe.Token, fe.Ports = 0, "", nil
		if err := f.write(fe); err != nil {
			return nil, err
		}
		return ending(old), nil
	case "release":
		// the machine entry is being deleted: its relay ends and the account
		// is nobody's, so another entry may take it; the generation stays,
		// so whatever of the deleted entry comes late is still refused
		if err := stale(); err != nil {
			return nil, err
		}
		old := fe
		fe.Entry, fe.Generation, fe.Off = 0, max(gen, fe.Generation), true
		fe.PID, fe.Token, fe.Ports = 0, "", nil
		if err := f.write(fe); err != nil {
			return nil, err
		}
		if old.PID == 0 {
			return nil, nil
		}
		return ending(old), nil
	case "free-ports":
		if err := stale(); err != nil {
			return nil, err
		}
		slot, ports, err := freePortsArgs(args)
		if err != nil {
			return nil, err
		}
		// the recorded relay's own ports are its business: end-relay first
		if fe.PID > 0 && isRelay(fe.PID, fe.Token) {
			for _, p := range ports {
				if slices.Contains(fe.Ports, p) {
					return nil, refuse("busy", fe, "port %d belongs to the running relay (pid %d)", p, fe.PID)
				}
			}
		}
		// under the lock: a relay start waits instead of binding meanwhile
		_, err = FreePorts(home, slot, ports, 5*time.Second)
		return nil, err
	}
	return nil, fmt.Errorf("unknown fence action %q", action)
}

func freePortsArgs(args []string) (slot string, ports []int, err error) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "-slot" && i+1 < len(args) {
			slot = args[i+1]
			i++
			continue
		}
		var p int
		if _, err := fmt.Sscanf(a, "%d", &p); err != nil || p < 1 || p > 65535 || fmt.Sprint(p) != a {
			return "", nil, fmt.Errorf("bad port %q", a)
		}
		ports = append(ports, p)
	}
	return slot, ports, nil
}
