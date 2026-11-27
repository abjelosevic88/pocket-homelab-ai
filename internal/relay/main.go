package relay

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// Main is the relay's part of pocket-hold's command line:
//
//	pocket-hold relay -installation I -entry E -generation G -token T [-test]
//	pocket-hold fence -installation I -entry E -generation G check|end-relay|off|release|free-ports [-slot S] P…|show
//	pocket-hold probe PORT    the link's tools test: a line to 127.0.0.1:PORT, its answer printed
//
// relay exits 0 when its link ended, 2 on a bad start, 3 when the fence
// refused it, 1 otherwise. fence exits 0 when done, 3 when refused (the
// reason as JSON on stdout), 1 on an error.
func Main(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: pocket-hold relay|fence …")
		return 2
	}
	switch args[0] {
	case "relay":
		return relayMain(args[1:])
	case "fence":
		return fenceMain(args[1:])
	case "probe":
		return probeMain(args[1:])
	}
	fmt.Fprintln(os.Stderr, "pocket-hold: unknown command", args[0])
	return 2
}

func relayMain(args []string) int {
	fs := flag.NewFlagSet("pocket-hold relay", flag.ContinueOnError)
	var o Options
	fs.StringVar(&o.Installation, "installation", "", "Pocket AI installation id")
	fs.Int64Var(&o.Entry, "entry", 0, "machine entry id")
	fs.Int64Var(&o.Generation, "generation", 0, "link generation")
	fs.StringVar(&o.Token, "token", "", "this relay's token (its identity in the fence)")
	fs.BoolVar(&o.Test, "test", false, "candidate relay: no fence, binds only what the server binds")
	fs.DurationVar(&o.Ping, "ping", 0, "ping interval (default 5s; for tests)")
	fs.DurationVar(&o.Dead, "dead", 0, "dead after this long without an answer (default 30s; for tests)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	o.Log = log.New(os.Stderr, "pocket-hold relay: ", log.LstdFlags)
	stop := make(chan struct{})
	o.Stop = stop
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	go func() { <-sigs; close(stop) }()
	// a write to the gone ssh session fails with EPIPE instead of killing us
	// before the fence record is cleared
	pipe := make(chan os.Signal, 1)
	signal.Notify(pipe, syscall.SIGPIPE)
	go func() {
		for range pipe {
		}
	}()
	err := Serve(stdio{}, o)
	var rf *Refused
	switch {
	case err == nil:
		return 0
	case errors.As(err, &rf):
		o.Log.Print(err)
		return 3
	case errors.Is(err, ErrBadStart):
		o.Log.Print(err)
		return 2
	}
	o.Log.Print(err)
	if o.Stop != nil {
		select {
		case <-stop:
			return 0 // asked to end (a takeover, a signal)
		default:
		}
	}
	return 1
}

type stdio struct{}

func (stdio) Read(p []byte) (int, error)  { return os.Stdin.Read(p) }
func (stdio) Write(p []byte) (int, error) { return os.Stdout.Write(p) }
func (stdio) Close() error {
	os.Stdout.Close()
	return nil // stdin stays: a blocked read on it ends with the process
}

func fenceMain(args []string) int {
	fs := flag.NewFlagSet("pocket-hold fence", flag.ContinueOnError)
	inst := fs.String("installation", "", "Pocket AI installation id")
	entry := fs.Int64("entry", 0, "machine entry id")
	gen := fs.Int64("generation", 0, "the caller's link generation")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) == 0 {
		fmt.Fprintln(os.Stderr, "pocket-hold fence: no action")
		return 2
	}
	action := rest[0]
	if action != "show" && (*entry <= 0 || *gen <= 0) {
		fmt.Fprintln(os.Stderr, "pocket-hold fence: -entry and -generation are needed")
		return 2
	}
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "pocket-hold fence:", err)
		return 1
	}
	var fe Fence
	err = fenceAction(home, *inst, *entry, *gen, action, rest[1:], &fe)
	var rf *Refused
	switch {
	case errors.As(err, &rf):
		b, _ := json.Marshal(rf)
		fmt.Println(string(b))
		return 3
	case err != nil:
		fmt.Fprintln(os.Stderr, "pocket-hold fence:", err)
		return 1
	}
	if action == "show" {
		if fe.Ports == nil {
			fe.Ports = []int{}
		}
		b, _ := json.Marshal(fe)
		fmt.Println(string(b))
	}
	return 0
}

// probeMain is the far end of the button's tools test: it connects to a port
// the relay was told to bind, says "pocket-link-probe", and prints the line
// that comes back (from the server's end of the tools stream).
func probeMain(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: pocket-hold probe PORT")
		return 2
	}
	c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", args[0]), 10*time.Second)
	if err != nil {
		fmt.Fprintln(os.Stderr, "pocket-hold probe:", err)
		return 1
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(20 * time.Second))
	if _, err := io.WriteString(c, ProbeLine+"\n"); err != nil {
		fmt.Fprintln(os.Stderr, "pocket-hold probe:", err)
		return 1
	}
	line, err := bufio.NewReader(c).ReadString('\n')
	if err != nil {
		fmt.Fprintln(os.Stderr, "pocket-hold probe:", err)
		return 1
	}
	fmt.Print(line)
	return 0
}

// ProbeLine is what pocket-hold probe sends; the server's test target
// answers it with ProbeAnswer.
const (
	ProbeLine   = "pocket-link-probe"
	ProbeAnswer = "pocket-link-ok"
)
