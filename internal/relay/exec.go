package relay

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// execShim runs the script handed over in execScriptEnv, which it takes out
// of the environment first (the command's children never see it).
const (
	execScriptEnv = "POCKET_RELAY_SCRIPT"
	execShim      = `__pocket_relay_script=$POCKET_RELAY_SCRIPT; unset POCKET_RELAY_SCRIPT; eval "$__pocket_relay_script"`
)

// killAfter: SIGTERM to a command's group, SIGKILL this much later.
const killAfter = 3 * time.Second

// execRun is one command the relay runs for the server.
type execRun struct {
	stream   net.Conn // its exec stream (the relay ending unblocks writes on it)
	pgid     int
	killOnce sync.Once
	gone     chan struct{} // the command ended and its exit was handled
}

// kill ends the command's whole process group: SIGTERM, SIGKILL 3 s later.
// A holder the command started is not in it (holders Setsid).
func (e *execRun) kill() {
	e.killOnce.Do(func() {
		if e.pgid <= 1 {
			return
		}
		_ = syscall.Kill(-e.pgid, syscall.SIGTERM)
		go func() {
			time.Sleep(killAfter)
			_ = syscall.Kill(-e.pgid, syscall.SIGKILL)
		}()
	})
}

// frameWriter sends what a command prints as frames of typ.
type frameWriter struct {
	mu  *sync.Mutex
	s   net.Conn
	typ byte
}

func (w frameWriter) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		c := min(len(p), execChunk)
		w.mu.Lock()
		err := writeFrame(w.s, w.typ, p[:c])
		w.mu.Unlock()
		if err != nil {
			return n - len(p), err
		}
		p = p[c:]
	}
	return n, nil
}

// serveExec runs /bin/sh -c script sh args… (the script by way of the
// environment: execShim) in its own process group:
// stdin from i frames (closed on E), output as o/e frames, the exit last.
// Cancel, the stream going before the exit, or the link dying ends the group.
func (r *relay) serveExec(s net.Conn, h openHeader) {
	if h.Script == "" {
		_ = writeJSONFrame(s, openAnswer{Error: "no script"})
		return
	}
	if err := writeJSONFrame(s, openAnswer{OK: true}); err != nil {
		return
	}
	var wmu sync.Mutex
	send := func(typ byte, v any) error {
		b, _ := json.Marshal(v)
		wmu.Lock()
		defer wmu.Unlock()
		return writeFrame(s, typ, b)
	}
	// the script travels in the environment, not on the command line: ps
	// shows command lines to every account there, and a script that looks
	// for processes by their command line (an agent's slot takeover) would
	// find this one's text and take it for what it looks for
	cmd := exec.Command("/bin/sh", append([]string{"-c", execShim, "sh"}, h.Args...)...)
	cmd.Env = append(os.Environ(), execScriptEnv+"="+h.Script)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdout = frameWriter{&wmu, s, xOut}
	cmd.Stderr = frameWriter{&wmu, s, xErr}
	// a background child that keeps the output open must not hold the exit
	cmd.WaitDelay = 2 * time.Second
	var stdinW *os.File
	if h.Stdin {
		pr, pw, err := os.Pipe()
		if err != nil {
			_ = send(xExit, execExit{Code: 127, Error: err.Error()})
			return
		}
		cmd.Stdin, stdinW = pr, pw
		defer pr.Close()
	}
	if err := cmd.Start(); err != nil {
		if stdinW != nil {
			stdinW.Close()
		}
		_ = send(xExit, execExit{Code: 127, Error: err.Error()})
		return
	}
	if f, ok := cmd.Stdin.(*os.File); ok {
		f.Close() // the child has it
	}
	e := &execRun{stream: s, pgid: cmd.Process.Pid, gone: make(chan struct{})}
	defer close(e.gone)
	r.mu.Lock()
	closed := r.closed
	r.execs[e] = struct{}{}
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.execs, e)
		r.mu.Unlock()
	}()
	if closed {
		e.kill()
	}
	_ = send(xStart, execStarted{PID: cmd.Process.Pid})

	exited := make(chan struct{})
	go r.execInput(s, e, stdinW, exited)

	err := cmd.Wait()
	close(exited)
	x := execExit{}
	if ps := cmd.ProcessState; ps != nil {
		if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			x.Code, x.Signal = 128+int(ws.Signal()), signalName(ws.Signal())
		} else {
			x.Code = ps.ExitCode()
		}
	} else if err != nil {
		x.Code, x.Error = 1, err.Error()
	}
	if err != nil && !errors.Is(err, exec.ErrWaitDelay) {
		var ee *exec.ExitError
		if !errors.As(err, &ee) && x.Error == "" {
			x.Error = err.Error()
		}
	}
	_ = send(xExit, x)
	// done: the input reader need not wait for the server to close
	_ = s.SetReadDeadline(time.Now())
}

// execInput reads the server's frames: stdin, its end, cancel. The stream
// ending before the command did is a reset: the server is gone.
func (r *relay) execInput(s net.Conn, e *execRun, stdinW *os.File, exited <-chan struct{}) {
	// stdin goes through a queue so that a command slow to read does not stop
	// us reading (a cancel may come); a full queue does wait.
	var queue chan []byte
	if stdinW != nil {
		queue = make(chan []byte, 32)
		go func() {
			defer stdinW.Close()
			for p := range queue {
				if p == nil {
					return // E: stdin ends
				}
				if _, err := stdinW.Write(p); err != nil {
					for range queue { // the command closed it: drop the rest
					}
					return
				}
			}
		}()
		defer close(queue)
	}
	eof := false
	br := bufio.NewReaderSize(s, 64<<10)
	for {
		typ, p, err := readFrame(br)
		if err != nil {
			select {
			case <-exited:
			default:
				e.kill()
			}
			return
		}
		switch typ {
		case xIn:
			if queue == nil || eof || len(p) == 0 {
				continue
			}
			select {
			case queue <- p:
			case <-exited:
			}
		case xEOF:
			if queue != nil && !eof {
				eof = true
				select {
				case queue <- nil:
				case <-exited:
				}
			}
		case xCancel:
			e.kill()
		}
	}
}

func signalName(s syscall.Signal) string {
	switch s {
	case syscall.SIGHUP:
		return "SIGHUP"
	case syscall.SIGINT:
		return "SIGINT"
	case syscall.SIGQUIT:
		return "SIGQUIT"
	case syscall.SIGKILL:
		return "SIGKILL"
	case syscall.SIGPIPE:
		return "SIGPIPE"
	case syscall.SIGTERM:
		return "SIGTERM"
	case syscall.SIGABRT:
		return "SIGABRT"
	case syscall.SIGSEGV:
		return "SIGSEGV"
	case syscall.SIGUSR1:
		return "SIGUSR1"
	case syscall.SIGUSR2:
		return "SIGUSR2"
	}
	return s.String()
}
