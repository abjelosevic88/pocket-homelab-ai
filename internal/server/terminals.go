package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/coder/websocket"
	"github.com/creack/pty"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

// Terminals: a shell on this server or on one of the person's machines, in
// the browser. The shell runs on a pty here (ssh -tt for other machines) and
// lives on when the page closes: opening it again replays what it printed
// (the last termScrollback bytes) and carries on. A restart of Pocket AI
// ends them.
//
// This server's shell is the server account itself, so only the box owner
// gets it; everyone else gets one in their jail, or on their own machines.

const (
	termScrollback = 512 << 10
	termPerPerson  = 16
	termIdle       = 12 * time.Hour   // nobody looked at a running one this long: closed
	termGone       = 10 * time.Minute // an ended one nobody looks at disappears
)

type terminal struct {
	ID       string    `json:"id"`
	HostID   int64     `json:"host_id"`
	HostName string    `json:"host_name"`
	Title    string    `json:"title"`
	Cwd      string    `json:"cwd,omitempty"`
	Created  time.Time `json:"created_at"`

	uid    int64
	cmd    *exec.Cmd
	ptmx   *os.File
	direct bool // cmd is the shell itself (this server, not jailed): busy is exact

	mu       sync.Mutex
	buf      []byte
	subs     map[chan termMsg]struct{}
	ended    bool
	code     int
	lastSeen time.Time
	size     [2]uint16
	lastIn   time.Time // last keystroke
	lastOut  time.Time // last output
}

type termMsg struct {
	data []byte
	end  bool
}

type termView struct {
	*terminal
	Ended    bool `json:"ended"`
	ExitCode int  `json:"exit_code"`
	Viewers  int  `json:"viewers"`
	Busy     bool `json:"busy"` // a command is running
}

func (t *terminal) view() termView {
	t.mu.Lock()
	defer t.mu.Unlock()
	return termView{terminal: t, Ended: t.ended, ExitCode: t.code, Viewers: len(t.subs), Busy: t.busyLocked()}
}

// busyLocked: a command is running in it. For a shell started here the
// terminal's foreground process group tells exactly (not the shell's own =
// something runs). Through ssh or the jail the foreground is always ssh or
// bwrap, so only output tells: it printed in the last 3 s, and not merely
// the echo of what was typed.
func (t *terminal) busyLocked() bool {
	if t.ended {
		return false
	}
	if t.direct && t.cmd.Process != nil {
		var pg int32
		var errno syscall.Errno
		if rc, err := t.ptmx.SyscallConn(); err == nil {
			_ = rc.Control(func(fd uintptr) {
				_, _, errno = syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TIOCGPGRP, uintptr(unsafe.Pointer(&pg)))
			})
			if errno == 0 && pg > 0 {
				return int(pg) != t.cmd.Process.Pid
			}
		}
	}
	return time.Since(t.lastOut) < 3*time.Second && t.lastOut.Sub(t.lastIn) > 800*time.Millisecond
}

// termSummary is what the sidebar shows: open terminals, and which are busy.
type termSummary struct {
	Open    int      `json:"open"`
	Busy    int      `json:"busy"`
	BusyIDs []string `json:"busy_ids"`
}

// watchTerminals tells each person's pages when their terminals' summary
// changes (opened, closed, ended, a command started or finished).
func (s *Server) watchTerminals() {
	last := map[int64]string{}
	for range time.Tick(time.Second) {
		now := map[int64]*termSummary{}
		s.terms.mu.Lock()
		for _, t := range s.terms.terms {
			sum := now[t.uid]
			if sum == nil {
				sum = &termSummary{BusyIDs: []string{}}
				now[t.uid] = sum
			}
			t.mu.Lock()
			if !t.ended {
				sum.Open++
			}
			if t.busyLocked() {
				sum.Busy++
				sum.BusyIDs = append(sum.BusyIDs, t.ID)
			}
			t.mu.Unlock()
		}
		s.terms.mu.Unlock()
		gone := map[int64]bool{} // their last terminal went: say so once
		for uid := range last {
			if now[uid] == nil {
				now[uid] = &termSummary{BusyIDs: []string{}}
				gone[uid] = true
			}
		}
		for uid, sum := range now {
			sort.Strings(sum.BusyIDs)
			b, _ := json.Marshal(sum)
			if last[uid] == string(b) {
				continue
			}
			s.rt.toUser(uid, sseEvent{Name: "terminals", Data: sum})
			if gone[uid] {
				delete(last, uid)
			} else {
				last[uid] = string(b)
			}
		}
	}
}

type termHub struct {
	mu      sync.Mutex
	terms   map[string]*terminal
	layouts map[int64]json.RawMessage // per person: tabs and their splits (the page's own shape)
	once    sync.Once
}

func (s *Server) terminalRoutes(r chi.Router) {
	r.Get("/terminals", s.handleListTerminals)
	r.Get("/terminals/layout", s.handleTerminalLayout)
	r.Put("/terminals/layout", s.handleTerminalLayout)
	r.Post("/terminals", s.handleCreateTerminal)
	r.Patch("/terminals/{tid}", s.handleRenameTerminal)
	r.Delete("/terminals/{tid}", s.handleCloseTerminal)
}

func (s *Server) termsOf(uid int64) []*terminal {
	s.terms.mu.Lock()
	defer s.terms.mu.Unlock()
	out := []*terminal{}
	for _, t := range s.terms.terms {
		if t.uid == uid {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.Before(out[j].Created) })
	return out
}

func (s *Server) termOf(r *http.Request) *terminal {
	s.terms.mu.Lock()
	defer s.terms.mu.Unlock()
	t := s.terms.terms[chi.URLParam(r, "tid")]
	if t == nil || t.uid != me(r) {
		return nil
	}
	return t
}

func (s *Server) handleListTerminals(w http.ResponseWriter, r *http.Request) {
	out := []termView{}
	for _, t := range s.termsOf(me(r)) {
		out = append(out, t.view())
	}
	writeJSON(w, http.StatusOK, out)
}

// handleTerminalLayout keeps how a person's terminals are arranged (tabs,
// split panes), so every device shows the same. Like the terminals
// themselves it lives until Pocket AI restarts.
func (s *Server) handleTerminalLayout(w http.ResponseWriter, r *http.Request) {
	uid := me(r)
	if r.Method == http.MethodPut {
		var in struct {
			Layout json.RawMessage `json:"layout"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in); err != nil || len(in.Layout) == 0 || !json.Valid(in.Layout) {
			writeErr(w, http.StatusBadRequest, "invalid layout")
			return
		}
		s.terms.mu.Lock()
		if s.terms.layouts == nil {
			s.terms.layouts = map[int64]json.RawMessage{}
		}
		s.terms.layouts[uid] = in.Layout
		s.terms.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
		return
	}
	s.terms.mu.Lock()
	l := s.terms.layouts[uid]
	s.terms.mu.Unlock()
	if l == nil {
		l = json.RawMessage("null")
	}
	writeJSON(w, http.StatusOK, map[string]json.RawMessage{"layout": l})
}

func (s *Server) handleCreateTerminal(w http.ResponseWriter, r *http.Request) {
	if !s.requireRun(w, r) {
		return
	}
	var in struct {
		HostID int64  `json:"host_id"`
		Cwd    string `json:"cwd"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	u := userFrom(r.Context())
	if len(s.termsOf(u.ID)) >= termPerPerson {
		writeErr(w, http.StatusBadRequest, "you have "+strconv.Itoa(termPerPerson)+" terminals open already: close one first")
		return
	}
	cmd, name, err := s.terminalCmd(r.Context(), u, in.HostID, strings.TrimSpace(in.Cwd))
	if err != nil {
		s.fail(w, err, "terminal")
		return
	}
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 100, Rows: 30})
	if err != nil {
		s.fail(w, err, "terminal")
		return
	}
	t := &terminal{ID: uuid.NewString(), HostID: in.HostID, HostName: name, Title: name, Cwd: in.Cwd, Created: time.Now(),
		uid: u.ID, cmd: cmd, ptmx: ptmx, subs: map[chan termMsg]struct{}{}, lastSeen: time.Now(), size: [2]uint16{100, 30},
		direct: in.HostID == 0 && !s.jailed(r.Context(), u.ID)}
	s.terms.mu.Lock()
	if s.terms.terms == nil {
		s.terms.terms = map[string]*terminal{}
	}
	n := 1
	for _, o := range s.terms.terms {
		if o.uid == u.ID && o.HostID == in.HostID {
			n++
		}
	}
	if n > 1 {
		t.Title = name + " " + strconv.Itoa(n)
	}
	s.terms.terms[t.ID] = t
	s.terms.mu.Unlock()
	s.terms.once.Do(func() { go s.reapTerminals(); go s.watchTerminals() })
	go s.pumpTerminal(t)
	s.log.Info("terminal opened", "user", u.ID, "host", in.HostID, "id", t.ID)
	writeJSON(w, http.StatusCreated, t.view())
}

// terminalCmd is the shell for a person on a machine, and the machine's name.
func (s *Server) terminalCmd(ctx context.Context, u *store.User, hostID int64, cwd string) (*exec.Cmd, string, error) {
	if hostID != 0 {
		h, err := s.checkHostUse(ctx, u, hostID)
		if err != nil {
			return nil, "", err
		}
		cmd, err := remoteOf(h).Shell(cwd)
		return cmd, h.Name, err
	}
	name := "This server"
	if hn, err := os.Hostname(); err == nil && hn != "" {
		name = hn
	}
	if s.jailed(ctx, u.ID) {
		if err := s.ensurePerson(u.ID); err != nil {
			return nil, "", err
		}
		home := s.personHome(u.ID)
		dir := home
		if cwd != "" && agent.IsPersonPath(cwd) && agent.Confined(cwd) {
			dir = cwd
		}
		cmd, err := agent.LocalCmd(nil, dir, []string{"HOME=" + home, "TERM=xterm-256color", "COLORTERM=truecolor"}, "bash", "-l")
		return cmd, name, err
	}
	if !s.isOwner(ctx, u.ID) && u.Role != "admin" {
		return nil, "", errForbidden("a shell on this server is the server's own account: only an admin gets one")
	}
	home, _ := os.UserHomeDir()
	dir := home
	if cwd != "" {
		if fi, err := os.Stat(cwd); err == nil && fi.IsDir() {
			dir = cwd
		}
	}
	sh := loginShell()
	cmd := exec.Command(sh, "-l")
	cmd.Dir = dir
	cmd.Env = shellEnv(home, sh)
	return cmd, name, nil
}

// loginShell is the server account's shell from /etc/passwd.
func loginShell() string {
	if f, err := os.Open("/etc/passwd"); err == nil {
		defer f.Close()
		uid := strconv.Itoa(os.Getuid())
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			p := strings.Split(sc.Text(), ":")
			if len(p) >= 7 && p[2] == uid && p[6] != "" {
				if _, err := os.Stat(p[6]); err == nil {
					return p[6]
				}
			}
		}
	}
	if sh := os.Getenv("SHELL"); sh != "" {
		return sh
	}
	return "/bin/bash"
}

// shellEnv is a login's environment, not the service's: Pocket AI's own
// settings (.env, its Core token) must not leak into the shell and from
// there into whatever is started in it.
func shellEnv(home, sh string) []string {
	env := []string{"HOME=" + home, "SHELL=" + sh, "TERM=xterm-256color", "COLORTERM=truecolor"}
	for _, k := range []string{"USER", "LOGNAME", "PATH", "LANG", "LC_ALL", "TZ", "XDG_RUNTIME_DIR", "DBUS_SESSION_BUS_ADDRESS"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// pumpTerminal copies the shell's output to the scrollback and the viewers.
func (s *Server) pumpTerminal(t *terminal) {
	b := make([]byte, 32<<10)
	for {
		n, err := t.ptmx.Read(b)
		if n > 0 {
			chunk := append([]byte(nil), b[:n]...)
			t.mu.Lock()
			t.buf = append(t.buf, chunk...)
			t.lastOut = time.Now()
			if len(t.buf) > termScrollback {
				t.buf = append([]byte(nil), t.buf[len(t.buf)-termScrollback*3/4:]...)
			}
			for ch := range t.subs {
				select {
				case ch <- termMsg{data: chunk}:
				default: // too slow: it reconnects and replays the scrollback
					close(ch)
					delete(t.subs, ch)
				}
			}
			t.mu.Unlock()
		}
		if err != nil {
			break
		}
	}
	err := t.cmd.Wait()
	_ = t.ptmx.Close()
	code := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	}
	t.mu.Lock()
	t.ended, t.code, t.lastSeen = true, code, time.Now()
	for ch := range t.subs {
		select {
		case ch <- termMsg{end: true}:
		default:
		}
	}
	t.mu.Unlock()
	s.log.Info("terminal ended", "id", t.ID, "code", code)
}

func (t *terminal) kill() {
	if t.cmd.Process != nil {
		// the shell leads its own session: hang up everything in it
		_ = syscall.Kill(-t.cmd.Process.Pid, syscall.SIGHUP)
		_ = t.cmd.Process.Signal(syscall.SIGHUP)
	}
	_ = t.ptmx.Close()
}

func (s *Server) handleRenameTerminal(w http.ResponseWriter, r *http.Request) {
	t := s.termOf(r)
	if t == nil {
		writeErr(w, http.StatusNotFound, "that terminal is not open (any more)")
		return
	}
	var in struct {
		Title string `json:"title"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || strings.TrimSpace(in.Title) == "" {
		writeErr(w, http.StatusBadRequest, "a name is needed")
		return
	}
	t.mu.Lock()
	t.Title = strings.TrimSpace(in.Title)
	t.mu.Unlock()
	writeJSON(w, http.StatusOK, t.view())
}

func (s *Server) handleCloseTerminal(w http.ResponseWriter, r *http.Request) {
	t := s.termOf(r)
	if t == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	s.dropTerminal(t)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) dropTerminal(t *terminal) {
	s.terms.mu.Lock()
	delete(s.terms.terms, t.ID)
	s.terms.mu.Unlock()
	t.kill()
	t.mu.Lock()
	for ch := range t.subs {
		close(ch)
		delete(t.subs, ch)
	}
	t.mu.Unlock()
}

// reapTerminals closes running shells nobody opened for termIdle and
// forgets ended ones.
func (s *Server) reapTerminals() {
	for range time.Tick(time.Minute) {
		s.terms.mu.Lock()
		var old []*terminal
		for _, t := range s.terms.terms {
			t.mu.Lock()
			idle := len(t.subs) == 0 && (t.ended && time.Since(t.lastSeen) > termGone || !t.ended && time.Since(t.lastSeen) > termIdle)
			t.mu.Unlock()
			if idle {
				old = append(old, t)
			}
		}
		s.terms.mu.Unlock()
		for _, t := range old {
			s.dropTerminal(t)
		}
	}
}

// CloseTerminals hangs up every shell (shutdown).
func (s *Server) CloseTerminals() {
	s.terms.mu.Lock()
	list := make([]*terminal, 0, len(s.terms.terms))
	for _, t := range s.terms.terms {
		list = append(list, t)
	}
	s.terms.mu.Unlock()
	for _, t := range list {
		t.kill()
	}
}

// handleTerminalSocket attaches a browser to a terminal: first everything
// still in the scrollback, then live output as binary messages. The browser
// sends keystrokes as binary messages and {"type":"resize","cols","rows"} as
// text; the server says {"type":"end","code"} when the shell exits.
func (s *Server) handleTerminalSocket(w http.ResponseWriter, r *http.Request) {
	t := s.termOf(r)
	if t == nil {
		writeErr(w, http.StatusNotFound, "that terminal is not open (any more)")
		return
	}
	// same-origin only (the default): a page elsewhere cannot use the cookie
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer c.CloseNow()
	c.SetReadLimit(1 << 20)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	ch := make(chan termMsg, 256)
	t.mu.Lock()
	snap := append([]byte(nil), t.buf...)
	ended, code := t.ended, t.code
	if !ended {
		t.subs[ch] = struct{}{}
	}
	t.lastSeen = time.Now()
	t.mu.Unlock()
	defer func() {
		t.mu.Lock()
		delete(t.subs, ch)
		t.lastSeen = time.Now()
		t.mu.Unlock()
	}()

	endMsg := func(code int) {
		b, _ := json.Marshal(map[string]any{"type": "end", "code": code})
		_ = c.Write(ctx, websocket.MessageText, b)
	}
	if len(snap) > 0 {
		if err := c.Write(ctx, websocket.MessageBinary, snap); err != nil {
			return
		}
	}
	if ended {
		endMsg(code)
		c.Close(websocket.StatusNormalClosure, "ended")
		return
	}

	go func() { // keystrokes and resizes
		defer cancel()
		for {
			typ, data, err := c.Read(ctx)
			if err != nil {
				return
			}
			if typ == websocket.MessageBinary {
				t.mu.Lock()
				t.lastIn = time.Now()
				t.mu.Unlock()
				if _, err := t.ptmx.Write(data); err != nil {
					return
				}
				continue
			}
			var m struct {
				Type       string `json:"type"`
				Cols, Rows uint16
			}
			if json.Unmarshal(data, &m) == nil && m.Type == "resize" && m.Cols > 0 && m.Rows > 0 && m.Cols < 1000 && m.Rows < 500 {
				t.mu.Lock()
				t.size = [2]uint16{m.Cols, m.Rows}
				t.mu.Unlock()
				_ = pty.Setsize(t.ptmx, &pty.Winsize{Cols: m.Cols, Rows: m.Rows})
			}
		}
	}()

	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ping.C:
			pctx, pc := context.WithTimeout(ctx, 15*time.Second)
			err := c.Ping(pctx)
			pc()
			if err != nil {
				return
			}
		case m, ok := <-ch:
			if !ok { // fell behind or closed: the page reconnects
				c.Close(websocket.StatusTryAgainLater, "reconnect")
				return
			}
			if m.end {
				t.mu.Lock()
				code := t.code
				t.mu.Unlock()
				endMsg(code)
				c.Close(websocket.StatusNormalClosure, "ended")
				return
			}
			if err := c.Write(ctx, websocket.MessageBinary, m.data); err != nil {
				return
			}
		}
	}
}
