# Machine link — implementation contract

The plan is `docs/machine-link-plan.md` (version 5). This file fixes the
names, packages, wire format and Go APIs so that work done in parallel fits
together. Where this file and the plan disagree on *behaviour*, the plan wins;
on *names and wire details*, this file wins.

Decisions taken: yamux (`github.com/hashicorp/yamux`) is the multiplexer;
everything ships on `main` behind the per-machine switch, off by default.

## Packages

| Package | What lives there |
|---|---|
| `internal/hold` | holders (unchanged behaviour). Phase 0 adds `client.go`: the in-process holder client (`hold.Conn`), the additive `proto` header field, `attach -start-only`, "slot busy" |
| `internal/relay` (new) | everything of the link that is not Pocket AI server logic: wire protocol, machine side (`relay.Serve`, run by `pocket-hold relay`), fence (`pocket-hold fence …`), server side client (`relay.Client`), test helpers (WAN shaper) |
| `internal/agent` | `link.go` gets a transport interface (phase 0). `Remote` gets an optional `Link` (phase 1/2/4) |
| `internal/server` | `machine_link.go` (state machine per machine, lease, generations, fallback, routes, reconciliation), API, `park.go` change (phase 3) |
| `cmd/pocket-hold` | dispatches `relay` and `fence` to `relay.Main`, the rest to `hold.Main` |

Import direction: `relay` may import `hold`; `hold` never imports `relay`.
`agent` imports both. `server` imports all.

## Holder protocol additions (phase 0)

- `hold.Header` gets `Proto int \`json:"proto,omitempty"\``. Holders built from
  now on send `Proto: 1` (`hold.Proto = 1`). Absent = 0 = "before the link".
  A client that sees `Proto > hold.Proto` refuses with
  `fmt.Errorf("holder speaks protocol %d, this Pocket AI knows %d: update Pocket AI", …)`.
- `hold.Conn` — the client half of `attach.go`, over any `net.Conn` (a Unix
  socket here, a relay `hold` stream there):

  ```go
  // Open does the hello/header exchange on c (already connected to a holder).
  func Open(c net.Conn, from int64) (*Conn, Header, error)
  func (c *Conn) Read() (typ byte, p []byte, err error) // fOut / fErr / fExit frames
  func (c *Conn) WriteIn(p []byte) error
  func (c *Conn) Stop() error
  func (c *Conn) Ack() error
  func (c *Conn) Close() error
  // Peek / StopVia: status and stop over an already-dialled conn
  func Peek(c net.Conn) (Header, error)
  ```
  plus an adapter that makes a `Conn` look exactly like the stdout/stdin of
  `pocket-hold attach` (header line, CLI stdout, exit line; stdin lines, the
  StopLine → fStop, exit ack after the exit line was handed on), so
  `agent/link.go`'s reading code does not change:

  ```go
  // AttachStream: what `pocket-hold attach` prints and reads, in-process.
  func AttachStream(c *Conn, hd Header, stderr io.Writer) (stdout io.ReadCloser, stdin io.WriteCloser, wait func() int)
  ```
  `wait` returns attach's exit code with the same meaning as the process
  (CLI's code; 254 lost the holder; 141 not delivered).
- `pocket-hold attach -start-only …SLOT -- ARGV`: start the holder if there is
  none (exactly as attach would, through the slot `flock`), print the header
  line, exit 0 **without attaching**. If the slot is held by another starter
  that did not get to listening within the wait, print the header with
  `"slot_busy":true` (new field `SlotBusy bool \`json:"slot_busy,omitempty"\``)
  and exit 75. `serve` that loses the flock and gives up waiting exits 75
  (not 0) so a starter can tell.

## Wire protocol (`internal/relay`)

### Process and handshake

The server runs over its existing ssh machinery (same sandbox for people's
machines, same `sshArgs` but `-T`, `BatchMode=yes`, **no ControlMaster**
(`-o ControlMaster=no -o ControlPath=none`), `ServerAliveInterval=5`,
`ServerAliveCountMax=3`, stderr kept apart):

```
<profile+PATH preamble>; exec "$BIN" relay -installation I -entry E -generation G -token T [-test]
```

1. Server writes `POCKET-LINK 1\n` on stdin.
2. Relay reads exactly one line; anything else → it writes
   `POCKET-RELAY-ERROR {"error":"…"}\n` and exits 2.
3. Relay does the fence work (run mode only), then writes **one** line:
   `POCKET-RELAY 1 {"pid":…,"version":"…","os":"…","arch":"…","generation":G,"test":bool}\n`
   or `POCKET-RELAY-REFUSED {"reason":"newer"|"other-entry"|"off"|"busy","entry":N,"generation":G,"error":"…"}\n` and exits 3.
4. The server's first line read must start with `POCKET-RELAY` (no skipping
   stray output: stray output → error "unexpected output before the link
   started: …", the process is killed).
5. Then yamux: **the relay is the yamux server, Pocket AI the yamux client**.
   Config: `AcceptBacklog 256`, `EnableKeepAlive false` (our own pings),
   `MaxStreamWindowSize 256 KiB` (yamux minimum = initial window: per-stream
   windows stay small), `StreamOpenTimeout 30s`, `ConnectionWriteTimeout 30s`,
   `LogOutput io.Discard`.

### Streams

Every stream starts with an open header written by the opener:
4-byte big-endian length + JSON `{"kind":"…", …}`; the acceptor answers with
the same framing `{"ok":true}` or `{"ok":false,"error":"…"}` before any raw
bytes.

| kind | opened by | header fields | after the answer |
|---|---|---|---|
| `control` | server, exactly one per link | — | newline-delimited JSON messages, both ways |
| `hold` | server | `slot` | raw bytes copied to/from `~/.pocket-ai/hold/<slot>/s` (the server speaks the holder protocol with `hold.Open`) |
| `exec` | server | `script`, `args` (`$1…`), `stdin` (bool: input will follow) | exec frames |
| `tools` | relay | `port` | raw bytes; the server dials that port's registered local target and copies |
| `pty` | server | `cols`, `rows`, `dir` | raw tty bytes; resize via 4-byte length-prefixed JSON in-band is not needed now — optional, may be left unimplemented |

### control messages

`{"id":N,"op":"…",…}`; replies `{"re":N,"ok":bool,"error":"…",…}`.

| op | from | meaning |
|---|---|---|
| `ping` | both | answered by `{"re":N,"ok":true}` at once |
| `sync` | server | `ports:[…]` — exactly the ports the relay must listen on now; it closes listeners not listed, opens the listed ones; reply lists `bound` and `failed:{port:error}` |
| `bind` | server | `port` — listen on 127.0.0.1:port; reply after the listener is up (or the error: `"in use"`…) |
| `unbind` | server | `port` — close the listener and its accepted conns' streams; reply once closed |
| `status` | server | reply: `pid`, `uptime_s`, `streams` (counts by kind), `ports` |

### exec frames (after the open answer)

Type byte + 4-byte big-endian length + data, like the holder.

server → relay: `i` stdin bytes, `E` stdin EOF, `c` cancel.
relay → server: `S` started (`{"pid":N}`), `o` stdout, `e` stderr,
`x` exit (`{"code":N,"signal":"…"}`) — last frame.

The relay runs `/bin/sh -c <script> sh <args…>` in its own process group
(`Setpgid`), stdin from `i` frames (closed on `E`; if `stdin` was false it is
`/dev/null`). Cancel, stream reset, or the link dying → SIGTERM to the group,
SIGKILL 3 s later. A script that started a holder is unaffected (holders
`Setsid`).

### Fence

Files: `~/.pocket-ai/relay/<installation>.fence` (JSON) guarded by `flock` on
`~/.pocket-ai/relay/<installation>.lock`. Content:

```json
{"installation":"…","entry":12,"generation":41,"off":false,"pid":1234,"token":"…","ports":[41234,…],"updated":"RFC3339"}
```

- Run-mode relay start: lock; refuse `other-entry` if `entry` differs and the
  fence is not empty; refuse `newer` if `generation <= fence.generation`
  (equal also refused); record `generation`, own `pid`, `token`, `off=false`,
  `ports=[]`, unlock; then end the previous relay (identity = `ps -o args= -p
  PID` contains `pocket-hold` … ` relay ` and `-token <old token>`): SIGTERM,
  wait 5 s, SIGKILL, check gone and its ports free (hard 15 s) — **before**
  binding anything and before writing the `POCKET-RELAY` line. On failure:
  `POCKET-RELAY-REFUSED {"reason":"busy",…}`.
- The relay keeps `ports` in the fence current (rewrites under the lock after
  each bind/unbind) so a successor and the cleanup know them.
- On exit the relay clears `pid`/`token` (only if they are still its own);
  `generation` stays.
- Test-mode relays never read or write the fence.

`pocket-hold fence -installation I -entry E -generation G ACTION [args]`
(exit 0 = done, 3 = refused with a JSON reason on stdout, 1 = error):

| action | what |
|---|---|
| `check` | allowed iff `G >= fence.generation` and the entry matches (or no fence) |
| `end-relay` | allowed iff `G >= fence.generation`; ends the recorded relay by token (as above), clears pid/token |
| `off` | requires `G > fence.generation`; sets generation=G, off=true, ends the recorded relay |
| `free-ports P…` | allowed iff `G >= fence.generation`; frees the given ports from **this account's own** stale `sshd` forwards / `pocket-hold` processes only (shares the logic with `agent.freePorts`), never another listener |
| `show` | prints the fence JSON |

Every fenced action and every run-mode start takes the same `flock`.

### Liveness

Each side sends `ping` every 5 s on the control stream and records when its
last ping was answered. Dead = no answer to own pings for 30 s **or** input
closed. Received output does not count. Relay on dead link: closes the
session, clears its fence pid, exits. Server on dead link: kills the ssh
process, closes the session.

## Server side API (`relay.Client`)

```go
type DialOptions struct {
    Installation string
    Entry        int64
    Generation   int64
    Token        string
    Test         bool
    Tools        func(port int) (net.Conn, error) // dial the local target registered for port
    OnDead       func(err error)
}
// Start runs cmd (an *exec.Cmd for the ssh process, not started yet),
// does the handshake and returns a live client. Refusals come back as *Refused.
func Start(ctx context.Context, cmd *exec.Cmd, o DialOptions) (*Client, error)

type Refused struct{ Reason string; Entry, Generation int64; Msg string }

func (c *Client) Hello() Hello                       // the relay's POCKET-RELAY JSON
func (c *Client) OpenHold(ctx context.Context, slot string) (net.Conn, error)
func (c *Client) Exec(ctx context.Context, req ExecRequest) (ExecResult, error)
func (c *Client) Bind(ctx context.Context, port int) error
func (c *Client) Unbind(ctx context.Context, port int) error
func (c *Client) Sync(ctx context.Context, ports []int) (bound []int, failed map[int]string, err error)
func (c *Client) Status(ctx context.Context) (Status, error)
func (c *Client) RTT() time.Duration                 // last ping round trip
func (c *Client) Done() <-chan struct{}               // closed when the link is dead/closed
func (c *Client) Err() error
func (c *Client) Close() error

type ExecRequest struct {
    Script string; Args []string
    Stdin  io.Reader // nil: /dev/null; else copied in `i` frames then `E`
    Stdout, Stderr io.Writer
}
type ExecResult struct{ Code int; Signal string; Started bool }
// Exec errors: ErrNotSent (the stream could not be opened: safe to retry
// anywhere) vs ErrUnknown (the request went out, no exit came: "unknown").
var ErrNotSent, ErrUnknown error
```

## As built (`internal/relay`, branch ml-relay)

Small changes and additions to the above, found while building it:

- **`DialOptions.PingEvery` / `DeadAfter`** (0 = 5 s / 30 s) and the relay's
  `-ping` / `-dead` flags: the liveness intervals, shorter in tests.
- **`relay.NewClient(ctx, conn, o)`**: the server side over any byte stream
  (`Start` uses it for the ssh process; tests use pipes and the WAN shaper).
- **`x` frame** may carry `"error"` when the command could not be started;
  `Exec` then returns `ErrNotSent` (nothing ran).
- **`POCKET-RELAY-REFUSED` reason `off`** is used when the fence was switched
  off (a delayed start after Switch back); `newer` otherwise.
- **`end-relay`, `off` and `free-ports` also refuse `other-entry`**: the
  contract checked only the generation there, which would let a second
  machine entry (with its own, unrelated generation counter) end the first
  entry's relay.
- **`free-ports [-slot S] P…`**: the slot is needed to find a stale sshd on
  Linux, where sshd is non-dumpable and no tool maps a port to it. With
  `-slot` it ends the sshd recorded in `~/.pocket-ai/run/<slot>.sshd`
  (`"PID LSTART"`, only while the recorded start time still matches) and the
  sshd above the slot's `pocket-hold … attach … SLOT --`. By port it ends
  only a listener that **is itself** an sshd or a `pocket-hold` of this
  account. Unlike `agent.freePorts` it never walks up from an arbitrary
  listener (that could end the person's own ssh session above an unrelated
  program). Ports of the live recorded relay are refused (`busy`):
  `end-relay` first. Exit 1 when ports are still held after 5 s.
- **The relay ends a recorded relay outside the fence lock** (the old relay
  takes the lock to clear its record on the way out); on failure the old
  record is put back so a later action tries again, the generation stays.
- **`relay.Gate`** (`NewGate(n)`, `Acquire(ctx, Viewed|Busy|Idle)` →
  `release`): the catch-up cap, counted in streams. One stream has at most
  one 256 KiB window in flight, so n streams ≈ n × 256 KiB.
  `DefaultCatchUp = 4` (1 MiB). The soak (100 ms RTT, 10 Mbit/s, 200 ms
  stalls) measured small round trips at p95 ≈ 1 s with 4, ≈ 0.5 s with 1
  (catch-up twice as slow) and ≈ 2.9 s with 16 (the plan's 4 MB): 4 MB is
  too much at 10 Mbit/s; roughly one second of the link's bandwidth is right.
- **Dead = 30 s since the last answer** to one of our own pings. With pings
  every 5 s that is noticed 25–30 s after a stall starts (+ round trip).
- **yamux `Session.Close` waits** for its reader and writer, which never end
  on a half-open link (the relay's stdin stays open, stdout is stuck): both
  sides close it in the background and do not wait.
- Not built: `pty` streams (optional). `Version` is set with
  `-ldflags -X …/internal/relay.Version=…`, else the VCS revision, else "dev".

## agent package hooks

```go
// RemoteLink is a machine's link as the agent package uses it (server's machineLink implements it).
type RemoteLink interface {
    // Exec runs script like Remote.Run would, over the link. ErrLinkNotSent / ErrLinkUnknown as above.
    Exec(ctx context.Context, script string, args []string, stdin io.Reader, stdout, stderr io.Writer) (code int, err error)
    UseFor(kind LinkUse) bool // LinkStatus (phase 1 status scripts), LinkCommands (phase 4), LinkAgents (phase 2)
    // phase 2:
    OpenHold(ctx context.Context, slot string) (net.Conn, error)
    BindTools(ctx context.Context, slot string, remotePort int, localAddr string) error
    UnbindTools(ctx context.Context, slot string, remotePort int) error
}
type Remote struct { …; Link RemoteLink } // nil: ssh as always
```

Call-site marks: `agent.Repeatable(ctx)` (safe to run twice), `agent.StatusScript(ctx)`
(read-only status script: repeatable, goes over the link already in `status`
mode), `agent.PlainSSH(ctx)` (never over the link: holder upload, fenced relay
end, fallback cleanup). Default: not repeatable.

## Server state (migration 0040)

```sql
CREATE TABLE controller_lease (id INTEGER PRIMARY KEY CHECK (id = 1), holder TEXT NOT NULL, expires_at INTEGER NOT NULL);
-- installation id: settings key "installation_id" (uuid, made once)
ALTER TABLE hosts ADD COLUMN link_mode TEXT NOT NULL DEFAULT 'off';      -- off | status | agents
ALTER TABLE hosts ADD COLUMN link_generation INTEGER NOT NULL DEFAULT 0; -- last allocated
ALTER TABLE hosts ADD COLUMN link_fallback TEXT NOT NULL DEFAULT '';     -- JSON {at, why} when fallen back
CREATE TABLE agent_routes (thread_id TEXT PRIMARY KEY, host_id INTEGER NOT NULL, port INTEGER NOT NULL DEFAULT 0, route TEXT NOT NULL, updated_at TEXT NOT NULL); -- ssh | to-link | link | to-ssh
```

(Check the real store conventions — table and column names may need to
follow `internal/store`; keep the meaning.)

Environment: `POCKET_LINK=off` is the server-wide emergency switch.

## Server side as built (branch ml-server)

- `internal/server/link_relay.go`: `linkConn` (the `relay.Client` methods the
  server uses, with server-side mirror types `linkHello`, `linkExecRequest`,
  `linkExecResult`, `relayStatus`, `relayDialOptions`), the sentinels
  `errRelayNotSent` / `errRelayUnknown`, `*relayRefused`,
  `*relayHandshakeError`, and the hook `startRelay`.
  **TODO(integration): relay.Start** — wire `startRelay` to `relay.Start` with
  a thin adapter (convert the types, map `relay.ErrNotSent`/`ErrUnknown`,
  `*relay.Refused`, and the relay's handshake/version error), then set
  `relayAvailable = true` (until then the button is disabled with "This
  Pocket AI build cannot start the link yet"). Also confirm `checkHello`'s
  version comparison matches what the relay reports as `version`.
- `controller_lease.go`, `machine_link.go` (state machine per machine),
  `host_link_api.go` (API), `link_callsites.go` (the mark of every call site).
- `agent/remote_link.go`: `RemoteLink`, `LinkUse`, the marks, `ErrLinkNotSent`
  / `ErrLinkUnknown`, `LinkUnknownError`, `LinkExitError`,
  `RemoteScriptText`, `Remote.LinkCommand`.
- Not yet: phase 2 (hold/tools streams, `bind`/`sync`, routes beyond the
  table and its fallback cleanup), FreeForwards marked PlainSSH (left for the
  owners of `freePorts`/`link.go`).

## Phases 2–5 as built (branch ml-phase2)

### Integration

- `startRelay` is `relay.Start` behind `relayConn` (link_relay.go);
  `relay.Refused` → `*relayRefused`, the new `*relay.HandshakeError` (stray
  output, another protocol, a `POCKET-RELAY-ERROR`, a generation/test-mode
  mismatch) → `*relayHandshakeError` (falls back at once),
  `ErrNotSent`/`ErrUnknown` → the server sentinels. `relayAvailable = true`.
- **Version check = protocol + content hash, not version strings.** The
  magic lines already pin the link protocol (`POCKET-RELAY 1`). The hello
  now carries `sum`: the first 6 bytes (hex) of the sha256 of the relay's own
  executable, which is exactly how `hold.Binary` names the uploaded file.
  `checkHello(hello, bin, test)` compares it with the `pocket-hold-<sum>`
  name the server installed, for test and run mode alike. Version strings
  are not compared: the server's comes from `git describe` (Makefile,
  deploy.sh), the relay's from `-X …/relay.Version` (build-bins.sh now stamps
  `$VERSION` or `git describe`, for people to read) — two spellings of one
  build would fail a string compare, and a content hash cannot lie about
  which build runs. An update needs no button: the new process uploads its
  own pocket-hold (new hash, new file), starts it with a new generation, and
  the old relay is taken over.
- **exec scripts travel in the environment** (`POCKET_RELAY_SCRIPT`, taken
  out by a shim before the script runs), not in `sh -c`'s argv: the agent's
  slot takeover looks for processes by command line (`stream-json`) and
  found a concurrent start's own script text over the link, ending it
  (exit 129). Also keeps scripts out of `ps` for other accounts.
- **The relay starts without the profile/PATH preamble**
  (`Remote.LinkCommand`): every command brings its own (`RemoteScriptText`),
  so applying it in the relay too doubled PATH and failed the env test on a
  real machine.
- **Generations rise installation-wide** (`settings.link_generation_seq`):
  a later machine entry, or one that got a deleted entry's id, starts above
  every generation handed out before.
- **Fence `release`** (`pocket-hold fence … release`): used when a machine
  entry on the link is deleted (`linkManager.forget`, before the row goes,
  with a freshly allocated generation): ends the relay, sets `entry=0`,
  `off=true`, keeps the generation (a delayed start of the deleted entry is
  still refused), so another entry may take the account.
- `free-ports` names another program on a port (`port P is in use by COMM
  (pid N), not by an agent's connection`, `relay.NotAnAgent`) when lsof can
  see it, and never walks up from an attach the relay started (its sshd is
  the link's).
- `pocket-hold probe PORT`: the button's tools round trip (dials the bound
  port, sends `pocket-link-probe`, prints the answer).
- `relay.Client.OpenHold` wraps `hold.ErrNoHolder` when nothing listens for
  the slot.
- **Relay hang on a dead link fixed** (TestRelayProcessExitsWhenStuck failed
  about half the time, also on the base): `control.die` closed its yamux
  stream synchronously, and the FIN queued behind the stuck sender (30 s
  timeout, then again); the relay never left. The close is now in the
  background with a write deadline, and the shutdown sets a write deadline
  on running execs' streams so their last frames cannot hold it.

### Agents on the link (machine_link_agents.go)

- Every held agent on another machine is wired at start
  (`linkManager.wireAgent`, runtime.go): `Hold.Route`, `Dial`, `StartOnly`,
  `CatchUp`, whatever the machine's mode, so a machine switched later moves
  its running agents and one switched back gives them their own connections.
  agent.Hold gained `Route func(ctx, start) (HoldPath, error)` — asked before
  every connection — and `CatchUp`; `HoldPath{Link, NoForwards, Guard, Free,
  Done}`; sessions are `agent.Reroutable` (`link.Reroute`: end the current
  connection on purpose, reconnect at once, the route decides where).
- `agentPath` (the route): machine wants its agents on the link and the link
  is up → `toLink`; route in a move state or `link` while the link is down →
  wait on the link's change channel (up, fallback, switch back) for up to
  2 min, then fail that attempt (link.go retries with its backoff) — a dead
  link is not hammered; otherwise the per-agent path.
- `toLink`: already bound by the current relay → hold stream; else `to-link`
  recorded, the old connection's port freed with **fenced** `free-ports
  -slot S P` (plain ssh, current generation), `bind` acknowledged, hold
  stream, `link` recorded only when the connection is up (`Done`). Bind fails
  (not "in use") → `ssh` recorded, the agent stays on its own connection for
  this run (`stay`), the cutover goes on (mixed routes). Port cannot be freed
  or is "in use" → an unrelated program (or one Pocket AI cannot tell is its
  own) has it: left alone, the agent goes on over the link **without its
  tools**, its thread gets a notice once (also when that happens during its
  start), each later connection tries again.
- `toSSH` (switch back per agent): `to-ssh`, `unbind` acknowledged, fenced
  `free-ports` confirms the port free, then the per-agent path; `ssh` once up.
- Per-agent path on a machine with a fence (generation > 0): its script runs
  `"$bin" fence … check` first (`Remote.Guard`, exit 75 when refused as
  newer/off; another entry owning the account does not block), and a held
  port is freed with the fenced `free-ports` (`HoldPath.Free`) instead of
  the unfenced script. `Remote.FreeForwards` itself is PlainSSH.
- Start on the link: `bind` (route) → `startOnly`: today's start script
  (`remoteScript`, with `attach -start-only`: profile, folder, login folder,
  slot takeover, pocket-gui bridge as before) as an exec → hold stream. An
  unknown result or `slot_busy`: the slot's status is asked (over the link,
  else plain ssh) before any new launch; a running holder is attached to,
  "gone" starts again (the slot's flock makes it safe), up to 4 tries.
- Tools: `DialOptions.Tools` = `toolsDial`, only the registered endpoint of
  the agent with that port. Same port allocation and pinning as before
  (`remotePocketTools` still writes the `-R` spec, used only on the
  per-agent path; `forwardPorts` reads port and endpoint from it).
- After every (re)connect, before the link counts as up (`afterConnect`):
  routes of agents whose holder is gone (one exec asks all) and that are
  not tracked are deleted; `to-link` → `link` if the relay holds the port,
  else `ssh` (its next connection moves it again); `to-ssh` → `link` if held
  and the machine still wants the link, else unbind + `ssh`; then `sync`
  lists exactly the `link` routes' ports. Then `converge` moves agents still
  on ssh one at a time (also after an agent attaches while the link is up —
  agents adopted before the link came up). A new relay never holds ports at
  start, so after a crash `to-link` is reversed and redone by the agent's
  next connection; within one link it is finished (tested both ways).
- Switch back: mode `off` is stored **first** (a restart in the middle
  finishes it: `resumeAll` runs `finishSwitchBack` — fence off with a new
  generation, ports freed, routes `ssh` — for mode-off machines with non-ssh
  routes), then each agent moves back while the relay serves the rest (stops
  early if the link drops), then the relay ends and the fence goes off;
  agents that did not move are put back like in a fallback.
- Full fallback (`releaseRelayLocked`): fenced `end-relay`, fenced
  `free-ports` of every non-ssh route's port, and only then the routes become
  `ssh` (which is what lets waiting agents take their own connections) — a
  route reads `ssh` only once its port is confirmed free.
- Reconnect: `sync` first (in `afterConnect`), then waiting agents see the
  link up; hold streams that resume (`only`) go through the machine's
  `relay.Gate` (`DefaultCatchUp` = 4 streams ≈ 1 MiB in flight; viewed >
  busy > idle via `rt.catchUpPriority`); a stream counts as catching up until
  it delivered the header's `Live − Offset` bytes (`catchUpReader`).
- `untrackAgent` (from `runSink.Exited`): the CLI ended (not Pocket AI
  stopping) → unbind + route deleted.
- link.go fix found by the crash tests: a reattach that completes after
  Detach ends its new connection (it used to keep reading forever).

### The button for agents

- `POST /hosts/{id}/link {"mode":"agents"}` is accepted from off or status
  ("Move agents too" on a status card, `move_agents` in the view; the off
  card's button now asks for `agents`). Test = env diff + `testAgents`: a
  disposable holder `linktest-<rand>` started by `attach -start-only -idle 2m`
  over the candidate's exec, checked with `hold.Open` over a hold stream
  (input echoed, stop, exit), removed after; an ephemeral port (random,
  retried on "in use") bound through control with a `pocket-hold probe`
  round trip to the server's `linkProbe`; unbound after. Confirm says how
  many running agents move. Switch (`switchLocked`) stores the mode, starts
  or reuses the link and runs `converge`.

### Phases 3–5

- Phase 3: `parkIdle` skips agents `keepsStream` says the link carries
  (machine wants the link, route `link`/`to-link`, not `stay`).
- Phase 4: every Remote call site carries its mark (link_callsites.go,
  reviewed and extended); uploads use stdin then its end. **Terminals stay
  on ssh** (`pty` streams are optional in the plan and not built).
- Phase 5: `linkManager.setUpNew` runs after a new machine's first passing
  check (create, or the first check of one never checked to the end):
  the same Install/Connect/Test with the agents' test, then Switch to
  `agents` without a Confirm (no agents yet). Only for mode off and
  generation 0, so a machine that was switched back is never switched on
  again by default. `POCKET_LINK_DEFAULT=off` turns it off.

### Numbers changed

- Catch-up cap: 4 streams (≈ 1 MiB in flight) instead of 4 MB — the soak
  showed 16 streams (≈ 4 MiB) put small round trips at p95 ≈ 2.9 s at
  10 Mbit/s, 4 at ≈ 1 s (noted in the plan's numbers).
- Agent waiting for its port on a down link: 2 min per attempt; one agent's
  move (cutover, switch back): 1 min.
