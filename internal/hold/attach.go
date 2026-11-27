package hold

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

// AttachOptions shape an attach.
type AttachOptions struct {
	From int64 // FromReplay, or the stdout offset to continue from
	Only bool  // never start a CLI: attach to a running holder, or report Gone
	// StartOnly: start the holder when there is none (exactly as an attach
	// would, through the slot's lock), print its header and exit without
	// attaching. The connection to it is made another way (in-process,
	// hold.Open).
	StartOnly bool
	Serve     ServeOptions
	// Scope is a command the holder is started under (systemd-run …) so it
	// leaves the service that started attach; tried before a plain start.
	Scope []string
}

// ExitSlotBusy is attach's (and serve's) exit code when another start holds
// the slot and did not get to listening in time: nothing was started, and
// the header says slot_busy. The starter asks the slot's status before
// anything else.
const ExitSlotBusy = 75

var errSlotBusy = errors.New("slot busy: another start holds it and does not listen yet")

// Attach connects stdin and stdout to slot's holder, starting the holder
// with argv when there is none (or its CLI ended unseen). It prints the
// header line, then the CLI's output, then an exit line, and exits with the
// CLI's code. Anything else that ends it only detaches.
func Attach(slot string, o AttachOptions, argv []string) int {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGHUP, syscall.SIGTERM, syscall.SIGINT)
	go func() { <-sigs; os.Exit(129) }()

	dir, err := Dir(slot)
	if err != nil {
		fmt.Fprintln(os.Stderr, "pocket-hold:", err)
		return 125
	}
	if o.StartOnly && (o.Only || len(argv) == 0) {
		fmt.Fprintln(os.Stderr, "pocket-hold: -start-only needs a command to start, and no -only")
		return 2
	}
	start := len(argv) > 0 && !o.Only
	refused := func(err error) bool {
		var pe *ProtoError
		if errors.As(err, &pe) {
			fmt.Fprintln(os.Stderr, "pocket-hold:", err)
			return true
		}
		return false
	}
	var (
		c       net.Conn
		conn    *Conn
		hd      Header
		started bool
	)
	waits := 0
	for try := 0; ; try++ {
		if try == 4 {
			fmt.Fprintln(os.Stderr, "pocket-hold: could not reach the holder:", err)
			return 125
		}
		c, err = dial(dir)
		if err != nil {
			if !start {
				return printHeader(Header{Gone: true})
			}
			if err = spawn(dir, slot, o, argv); err != nil {
				fmt.Fprintln(os.Stderr, "pocket-hold:", err)
				if errors.Is(err, errSlotBusy) {
					if code := printHeader(Header{SlotBusy: true}); code != 0 {
						return code
					}
					return ExitSlotBusy
				}
				return 125
			}
			started = true
			continue
		}
		if start && !started && waits < 3 {
			// look before attaching: attaching takes the holder from whoever has it
			peek, err := handshake(c, hello{Peek: true})
			c.Close()
			if refused(err) {
				return 125
			}
			if err == nil && (peek.Exited || peek.Stopping) {
				// the last CLI ended with nobody there, or is ending: let its
				// holder go, then start anew
				waits++
				waitEnded(dir, 30*time.Second)
				try--
				continue
			}
			if c, err = dial(dir); err != nil {
				continue
			}
		}
		if o.StartOnly {
			hd, err = Peek(c)
			if refused(err) {
				return 125
			}
			if err != nil {
				time.Sleep(300 * time.Millisecond) // a holder on its way out
				continue
			}
			hd.Started = started
			return printHeader(hd)
		}
		conn, hd, err = Open(c, o.From)
		if refused(err) {
			return 125
		}
		if err != nil {
			time.Sleep(300 * time.Millisecond) // a holder on its way out
			continue
		}
		break
	}
	defer conn.Close()
	hd.Started = started
	code, lost := stream(conn, hd, os.Stdout, os.Stdin, os.Stderr)
	if lost != nil {
		fmt.Fprintln(os.Stderr, "pocket-hold: lost the holder:", lost)
	}
	return code
}

func printHeader(hd Header) int {
	hd.Type = HeaderType
	b, _ := json.Marshal(hd)
	if _, err := os.Stdout.Write(append(b, '\n')); err != nil {
		return 141
	}
	return 0
}

// spawn starts the holder in a session of its own (so a dropped ssh session
// or a stopped service leaves it), first under o.Scope when one is given,
// and waits until it listens.
func spawn(dir, slot string, o AttachOptions, argv []string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	// the meta (it can carry the Pocket tools key) goes in the environment,
	// which only this account can read; a command line every account can (ps)
	args := []string{"serve", "-start-mark", o.Serve.StartMark, "-end-mark", o.Serve.EndMark}
	for _, k := range o.Serve.KeepMarks {
		args = append(args, "-keep-mark", k)
	}
	if o.Serve.IdleDetached > 0 {
		args = append(args, "-idle", o.Serve.IdleDetached.String())
	}
	if o.Serve.MaxDetached > 0 {
		args = append(args, "-max", o.Serve.MaxDetached.String())
	}
	if o.Serve.Linger > 0 {
		args = append(args, "-linger", o.Serve.Linger.String())
	}
	args = append(append(args, slot, "--"), argv...)
	tries := [][]string{append([]string{self}, args...)}
	if len(o.Scope) > 0 {
		tries = append([][]string{append(append([]string{}, o.Scope...), tries[0]...)}, tries...)
	}
	var last error
	for _, a := range tries {
		cmd := exec.Command(a[0], a[1:]...)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if o.Serve.Meta != "" {
			cmd.Env = append(os.Environ(), MetaEnv+"="+o.Serve.Meta)
		}
		if err := cmd.Start(); err != nil {
			last = err
			continue
		}
		exited := make(chan error, 1)
		go func() { exited <- cmd.Wait() }()
		deadline := time.After(15 * time.Second)
		for {
			if c, err := dial(dir); err == nil {
				c.Close()
				return nil
			}
			select {
			case err := <-exited:
				// it ended without listening: another holder won the slot, or
				// the scope could not be made
				if c, e := dial(dir); e == nil {
					c.Close()
					return nil
				}
				var ee *exec.ExitError
				if errors.As(err, &ee) && ee.ExitCode() == ExitSlotBusy {
					return errSlotBusy // another start has the slot: no point in a second try
				}
				if err == nil {
					err = errors.New("the holder ended at once")
				}
				last = fmt.Errorf("%s: %w", a[0], err)
			case <-deadline:
				return errors.New("the holder did not start listening")
			case <-time.After(50 * time.Millisecond):
				continue
			}
			break
		}
	}
	return last
}

// waitEnded waits for the holder in dir to end its CLI, collects the exit
// so it goes, and waits until it has.
func waitEnded(dir string, max time.Duration) {
	end := time.Now().Add(max)
	for time.Now().Before(end) {
		c, err := dial(dir)
		if err != nil {
			return
		}
		hd, err := handshake(c, hello{Peek: true})
		c.Close()
		if err == nil && hd.Exited {
			if c, err := dial(dir); err == nil {
				r := bufio.NewReader(c)
				if _, err := handshakeR(c, r, hello{From: 1 << 62}); err == nil {
					_ = writeFrame(c, fAck, nil)
				}
				c.Close()
			}
			waitGone(dir, time.Until(end))
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func waitGone(dir string, max time.Duration) {
	end := time.Now().Add(max)
	for time.Now().Before(end) {
		c, err := dial(dir)
		if err != nil {
			return
		}
		c.Close()
		time.Sleep(100 * time.Millisecond)
	}
}

// MetaEnv carries the meta from attach to the serve it starts.
const MetaEnv = "POCKET_HOLD_META"

// Main is pocket-hold's command line:
//
//	pocket-hold attach [-from N] [-only | -start-only] [-start-mark S] [-end-mark E] [-meta M | -meta-file F] [-scope CMD] SLOT -- ARGV…
//	pocket-hold serve [-start-mark S] [-end-mark E] [-meta M] SLOT -- ARGV…   (meta also from $POCKET_HOLD_META)
//	pocket-hold status SLOT     the header as JSON ({"gone":true} without a holder)
//	pocket-hold stop SLOT       end the CLI
func Main(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: pocket-hold attach|serve|status|stop …")
		return 2
	}
	fs := flag.NewFlagSet("pocket-hold "+args[0], flag.ContinueOnError)
	from := fs.Int64("from", FromReplay, "stdout offset to continue from (-1: the running turn)")
	only := fs.Bool("only", false, "attach only; never start the CLI")
	startOnly := fs.Bool("start-only", false, "start the holder if there is none, print its header, do not attach")
	startMark := fs.String("start-mark", "", "a stdin line with this starts a turn")
	endMark := fs.String("end-mark", "", "a stdout line with this ends a turn")
	var keep multi
	fs.Var(&keep, "keep-mark", "stdout lines with this are kept for readers that replay (repeatable)")
	meta := fs.String("meta", "", "stored with the holder, returned in headers")
	metaFile := fs.String("meta-file", "", "read -meta from this file (it stays off the command line)")
	scope := fs.String("scope", "", "command to start the holder under (space-separated)")
	idle := fs.Duration("idle", 0, "end the CLI after this long detached with no turn running (default 24h)")
	maxd := fs.Duration("max", 0, "end the CLI after this long detached even mid-turn (default never)")
	linger := fs.Duration("linger", 0, "keep an ended CLI's output this long")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) == 0 {
		fmt.Fprintln(os.Stderr, "pocket-hold: no slot")
		return 2
	}
	slot, argv := rest[0], rest[1:]
	if len(argv) > 0 && argv[0] == "--" {
		argv = argv[1:]
	}
	if *metaFile != "" {
		b, err := os.ReadFile(*metaFile)
		if err != nil {
			fmt.Fprintln(os.Stderr, "pocket-hold:", err)
			return 2
		}
		*meta = strings.TrimSpace(string(b))
	}
	if *meta == "" {
		*meta = os.Getenv(MetaEnv)
	}
	os.Unsetenv(MetaEnv) // not for the CLI
	so := ServeOptions{StartMark: *startMark, EndMark: *endMark, KeepMarks: keep, Meta: *meta, IdleDetached: *idle, MaxDetached: *maxd, Linger: *linger}
	switch args[0] {
	case "attach":
		return Attach(slot, AttachOptions{From: *from, Only: *only, StartOnly: *startOnly, Serve: so, Scope: strings.Fields(*scope)}, argv)
	case "serve":
		return Serve(slot, so, argv)
	case "status":
		hd, ok, err := Status(slot)
		if err != nil {
			fmt.Fprintln(os.Stderr, "pocket-hold:", err)
			return 1
		}
		if !ok {
			hd = Header{Gone: true}
		}
		return printHeader(hd)
	case "stop":
		if err := Stop(slot); err != nil {
			fmt.Fprintln(os.Stderr, "pocket-hold:", err)
			return 1
		}
		return 0
	}
	fmt.Fprintln(os.Stderr, "pocket-hold: unknown command", args[0])
	return 2
}

type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }
