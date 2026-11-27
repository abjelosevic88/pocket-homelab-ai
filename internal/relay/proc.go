package relay

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Process identity through ps, which reads the same on Linux and macOS. A
// pid alone proves nothing (pids are reused): every kill here first checks
// what the process is.

func psField(pid int, field string) (string, bool) {
	if pid <= 1 {
		return "", false
	}
	out, err := exec.Command("ps", "-o", field+"=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(out)), true
}

// ownProcess: the process exists and runs as this account.
func ownProcess(pid int) bool {
	uid, ok := psField(pid, "uid")
	return ok && uid == strconv.Itoa(os.Getuid())
}

// isRelay: pid is a pocket-hold relay whose command line carries token.
func isRelay(pid int, token string) bool {
	if token == "" {
		return false
	}
	args, ok := psField(pid, "args")
	if !ok {
		return false
	}
	f := strings.Fields(args)
	if len(f) == 0 || !strings.Contains(filepath.Base(f[0]), "pocket-hold") {
		return false
	}
	relay, tok := false, false
	for i, a := range f {
		if a == "relay" {
			relay = true
		}
		if a == "-token" && i+1 < len(f) && f[i+1] == token {
			tok = true
		}
		if a == "-token="+token {
			tok = true
		}
	}
	return relay && tok && ownProcess(pid)
}

// isPocketHold: pid is one of this account's pocket-hold processes.
func isPocketHold(pid int) bool {
	args, ok := psField(pid, "args")
	if !ok {
		return false
	}
	f := strings.Fields(args)
	return len(f) > 0 && strings.Contains(filepath.Base(f[0]), "pocket-hold") && ownProcess(pid)
}

// isRelayProc: pid is a pocket-hold relay (any token).
func isRelayProc(pid int) bool {
	args, ok := psField(pid, "args")
	if !ok {
		return false
	}
	f := strings.Fields(args)
	return len(f) > 1 && strings.Contains(filepath.Base(f[0]), "pocket-hold") && f[1] == "relay"
}

// isSSHD: pid is an sshd process of this account (the per-session one that
// holds a connection's forwards).
func isSSHD(pid int) bool {
	comm, ok := psField(pid, "comm")
	return ok && strings.Contains(filepath.Base(comm), "sshd") && ownProcess(pid)
}

// endProcess sends SIGTERM to pid while still is true, waits up to grace,
// then SIGKILL, and waits until still is false or the deadline. still must
// re-check identity: once the process is gone its pid may belong to anything.
func endProcess(pid int, still func() bool, grace time.Duration, deadline time.Time) error {
	if !still() {
		return nil
	}
	_ = syscall.Kill(pid, syscall.SIGTERM)
	soft := time.Now().Add(grace)
	for time.Now().Before(soft) {
		time.Sleep(100 * time.Millisecond)
		if !still() {
			return nil
		}
	}
	if still() {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
	for time.Now().Before(deadline) {
		if !still() {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("process %d did not end", pid)
}

// portFree: nothing listens on 127.0.0.1:port (binding it is the one check
// that sees every socket, whoever owns it).
func portFree(port int) bool {
	l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return false
	}
	l.Close()
	return true
}

func waitPortsFree(ports []int, deadline time.Time) []int {
	for {
		var held []int
		for _, p := range ports {
			if !portFree(p) {
				held = append(held, p)
			}
		}
		if len(held) == 0 || !time.Now().Before(deadline) {
			return held
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// listeners lists the pids lsof can see listening on 127.0.0.1:port. sshd
// makes itself non-dumpable, so on Linux it is not among them; on macOS it is.
func listeners(port int) []int {
	out, _ := exec.Command("lsof", "-t", "-nP", "-iTCP@127.0.0.1:"+strconv.Itoa(port), "-sTCP:LISTEN").Output()
	var pids []int
	for _, f := range strings.Fields(string(out)) {
		if p, err := strconv.Atoi(f); err == nil {
			pids = append(pids, p)
		}
	}
	return pids
}

// sshdRecord reads ~/.pocket-ai/run/<slot>.sshd ("PID LSTART", written by the
// agent's remote script) and returns the pid when it is still that same
// sshd (its start time matches).
func sshdRecord(home, slot string) int {
	b, err := os.ReadFile(filepath.Join(home, ".pocket-ai", "run", slot+".sshd"))
	if err != nil {
		return 0
	}
	f := strings.Fields(string(b))
	if len(f) < 2 {
		return 0
	}
	pid, err := strconv.Atoi(f[0])
	if err != nil {
		return 0
	}
	start, ok := psField(pid, "lstart")
	if !ok || strings.Join(strings.Fields(start), " ") != strings.Join(f[1:], " ") {
		return 0
	}
	if !isSSHD(pid) {
		return 0
	}
	return pid
}

// sshdAbove walks up from pid (at most 4 parents) to the sshd it runs under.
// An attach the relay started (a start over the link) has the link's sshd
// above it: none is returned for it.
func sshdAbove(pid int) int {
	for n := 0; pid > 1 && n < 5; n++ {
		if isSSHD(pid) {
			return pid
		}
		if n > 0 && isRelayProc(pid) {
			return 0
		}
		pp, ok := psField(pid, "ppid")
		if !ok {
			return 0
		}
		pid, _ = strconv.Atoi(pp)
	}
	return 0
}

// slotAttaches: this account's `pocket-hold … attach … SLOT -- …` processes.
func slotAttaches(slot string) []int {
	out, err := exec.Command("ps", "-eo", "pid=,args=").Output()
	if err != nil {
		return nil
	}
	var pids []int
	for _, l := range strings.Split(string(out), "\n") {
		l = strings.TrimSpace(l)
		pidS, args, ok := strings.Cut(l, " ")
		if !ok || !strings.Contains(args, " "+slot+" -- ") || !strings.Contains(args, " attach ") {
			continue
		}
		pid, err := strconv.Atoi(pidS)
		if err == nil && pid != os.Getpid() && isPocketHold(pid) {
			pids = append(pids, pid)
		}
	}
	return pids
}

// FreePorts ends what of this account still holds ports for a slot's old
// connection: the sshd recorded for the slot (when its record is still
// right), the sshd above the slot's pocket-hold attach, and on each port a
// listener that is itself an sshd or a pocket-hold of this account. Anything
// else listening there is left alone. It then waits up to wait for the ports
// to come free and returns those still held.
func FreePorts(home, slot string, ports []int, wait time.Duration) ([]int, error) {
	kill := map[int]bool{}
	if slot != "" {
		if !slotRE.MatchString(slot) {
			return ports, fmt.Errorf("bad slot %q", slot)
		}
		if p := sshdRecord(home, slot); p > 0 {
			kill[p] = true
		}
		for _, a := range slotAttaches(slot) {
			if p := sshdAbove(a); p > 0 {
				kill[p] = true
			}
		}
	}
	for _, port := range ports {
		for _, p := range listeners(port) {
			// only the listener itself: walking up from an unrelated program
			// would end the person's own ssh session
			if isSSHD(p) || isPocketHold(p) {
				kill[p] = true
			}
		}
	}
	for p := range kill {
		_ = syscall.Kill(p, syscall.SIGTERM)
	}
	held := waitPortsFree(ports, time.Now().Add(wait))
	if len(held) > 0 {
		// name another program on a port: it is left alone, and the caller
		// says why the port cannot be had
		for _, port := range held {
			for _, p := range listeners(port) {
				if !isSSHD(p) && !isPocketHold(p) {
					comm, _ := psField(p, "comm")
					return held, fmt.Errorf("port %d is in use by %s (pid %d), %s", port, filepath.Base(comm), p, NotAnAgent)
				}
			}
		}
		return held, errors.New("ports still held: " + intsString(held))
	}
	return nil, nil
}

// NotAnAgent ends FreePorts' error when another program listens on a port
// (the fence prints it): that program is left alone. The per-agent path's
// own cleanup (agent.freePorts) says the same.
const NotAnAgent = "not by an agent's connection"

func intsString(ps []int) string {
	s := make([]string, len(ps))
	for i, p := range ps {
		s[i] = strconv.Itoa(p)
	}
	return strings.Join(s, " ")
}
