# Pocket AI: one link per machine

Plan, version 5. Earlier versions and what each council round changed are in the appendix at the end. Version 5 answers the third round, where all four participants voted "Go with changes". It fixes two contradictions in version 4:

- Version 4 said the relay and the per-agent forwards may never both run on one machine, yet its cutover moves agents one at a time. Version 5 makes the rule per tools port instead.
- Version 4 started an agent before binding its tools port. Version 5 binds the port first.

It also makes takeover ownership hold even after a relay has exited, requires answered pings in both directions, and matches each preflight test to the phase that provides it.

## Recommendation

Build a small **relay** into `pocket-hold` and run exactly one per person and machine, over the SSH connection Pocket AI already has. Every agent's stream, the Pocket tools traffic and the short commands travel inside that one connection.

Keep SSH as the transport. A machine-side daemon that dials out to the server is out of scope until a named machine needs it.

Why it is cheaper than it sounds:

- **The holders stay untouched.** Each agent already runs under `pocket-hold serve`, which listens on a Unix socket (`~/.pocket-ai/hold/<slot>/s`) and speaks a framed protocol with byte offsets. The relay only connects streams to those sockets. Agents started before the upgrade keep working.
- **The resume logic stays untouched.** `internal/agent/link.go` already reattaches at an exact byte offset and resends input the holder did not get. That is what makes a shared connection safe.
- **No new access model.** Same SSH key, same per-person sandbox, no new open ports, no service installed on the machine.

A machine is switched to the link from **Settings → Machines** with one button, over the SSH connection it already has. Pocket AI installs the relay there and tests it without touching running agents. It moves the machine's agents over only after the person confirms. Nothing has to be done on the machine itself.

**Phase 0 can start now.** Everything after it is gated on the link lifecycle rules below being implemented and tested. Phase 2 (live agents) is also gated on fixing today's "port still held" recovery bug.

## Where we are today (v0.120.0)

| Traffic | How it travels now | Connections |
|---|---|---|
| A working agent | `ssh … pocket-hold attach <slot>` | 1 per agent |
| An idle agent | parked after 10 min (`park.go`), reattached every 6 h so its holder does not end it | 0, plus 1 watcher per machine |
| Pocket tools (MCP) | `-R` port forward on the agent's connection, in place before the CLI starts | rides the agent's |
| Short commands (29 call sites of `Remote.Run`) | the server's own machines: shared OpenSSH ControlMaster (`muxArgs`, `ControlPersist=300`). People's machines: a new SSH connection each, because the sandbox ends with each `ssh` | 1 per command on people's machines |

What remains after parking:

- Busy agents cost a connection each.
- A restart brings agents back one by one (56 took about 9 minutes).
- Every short command on a person's machine pays an SSH handshake (about 1.3 s to a far box).
- Tool forwarding fails in its own way: "remote port forwarding failed … port still held by an old connection" was seen once in today's deploy, and the existing cleanup did not free the port on the first try.

## Target design

```
Pocket AI server                          machine
┌───────────────┐   one ssh process   ┌──────────────────┐
│ machineLink   │═════════════════════│ pocket-hold relay│
│  (per person, │  multiplexed        │   ├─ control     │
│   per machine)│  streams            │   ├─ slot A sock │── holder A ── claude
└───────────────┘                     │   ├─ slot B sock │── holder B ── codex
                                      │   ├─ 127.0.0.1:p │◄─ CLI's MCP calls
                                      │   └─ sh (exec)   │
                                      └──────────────────┘
```

### Stream kinds

| Kind | Direction | What the relay does | Replaces |
|---|---|---|---|
| `control` | server → relay, one per link | carries `sync`, `bind(port)` and `unbind(port)`, each acknowledged once the listener is up or closed, plus status and pings. `sync` lists only the ports currently assigned to the relay. It is sent after every connect or reconnect, and `bind` before an agent is started or reattached | (new) |
| `hold` | server → relay | connects the stream to a slot's holder socket and copies bytes both ways | `pocket-hold attach` per agent |
| `tools` | relay → server | for each port bound through `control`, listens on loopback; each accepted connection becomes a stream to the server's Pocket tools endpoint | `-R` forwards |
| `exec` | server → relay | runs a shell script, with frames for stdin, `stdin_eof`, `cancel`, stdout, stderr and exit code | `Remote.Run`, uploads, starting a holder |
| `pty` (optional) | server → relay | a shell with a terminal | terminal connections |

There is no `watch` kind any more. With one link the server simply keeps a `hold` stream open for every live agent. An idle stream costs a Unix socket on the machine and a few kilobytes on the server. The server stays the holder's one attached client, exactly as today, and output arrives the moment it is produced.

**Idle expiry is unchanged by this.** The holder's 24-hour rule only applies while nobody is attached (`serve.go`: `away && !busy && quiet > IdleDetached`). Today parking already reattaches every 6 hours (`parkTouch`), so that the server's live agents are *not* ended, because Remote Control is meant to stay up. In practice the rule only ends agents the server no longer tracks. With the link, a tracked agent has an open stream and an untracked one has none, so the outcome is the same. A test covers this before phase 3.

The relay is kept minimal. Each stream's handler recovers from its own panic and closes only that stream, so one bad stream cannot drop the whole machine.

### Multiplexing

`hashicorp/yamux` runs over the SSH process's stdin/stdout. It gives per-stream flow control and keep-alives, and each stream is a plain `net.Conn`, so the holder protocol runs over it unchanged. It is MPL-2.0, which is compatible with this MIT project as an unmodified dependency.

yamux inside one SSH channel means two layers of flow control, and yamux has no priorities. One stream flooding the SSH channel window would delay every other stream, pings included. The countermeasures keep pings fast instead of excusing them when they are slow:

- **Conservative windows.** Per-stream yamux windows are kept small, so no single stream can fill the SSH channel window on its own.
- **A cap on catch-up bytes in flight** across all `hold` streams (see Reconnect).
- **Liveness needs answered pings, in both directions.** Incoming output proves only that one direction works. The link counts as healthy only while each side's pings are answered within the bound (see Dead-link detection). If requests stop reaching the relay while output keeps arriving, that is caught.

This stays unproven until the WAN soak test, which must show both things: no false trips under load, and detection of a one-way stall. It is a hard gate for phase 2.

**Rejected alternatives:**

- **Native SSH channels with `x/crypto/ssh`.** That would replace the OpenSSH client, and with it the per-person sandbox around `ssh`, people's own `~/.ssh/config`, host-key handling and Tailscale/ProxyJump setups.
- **Own framing.** Flow control is where the subtle bugs would be.
- **OpenSSH ControlMaster for everything**, which the server's own machines already use for short commands. It is not enough:
  - On people's machines the sandbox ends with each `ssh`, so a long-lived master would need its own lifetime management inside the sandbox.
  - `sshd`'s default `MaxSessions=10` caps the channels on one connection below 50 agents.
  - Forwards added with `-O forward` keep today's stale-port problem.
  - Nothing controls flow between channels.

  On a machine that is on the link, short commands go over the link and `muxArgs` is not used. `muxArgs` stays for machines on the per-agent path and for the steps that must use plain SSH.

### Link lifecycle (the part that has to be right)

1. **One owner per machine.** On the server, each machine's link is driven by a single state machine. Starting, reconnecting, falling back, enabling, switching back and the emergency off switch all go through it, one at a time. No two of them run at once for the same machine.

2. **One controlling server process.** Each installation has a **controller lease** in its database: a row naming the server process that holds it, with an expiry. The holder renews it every 10 s and it expires after 30 s. Only the lease holder may allocate a generation, and it does so in the same transaction that checks the lease. So a stale process cannot simply ask for a fresh, higher generation. A process that loses or cannot renew its lease stops every link it drives at once. During a deploy, the new process takes the lease when the old one releases it on shutdown, or when it expires.

3. **Start.** There is one start at a time per (person, machine), however many agents ask at once. The SSH command runs with `-T`, `BatchMode=yes` and stderr kept apart. Before multiplexing begins, both sides exchange a short magic line with a protocol version. Stray shell output or a mismatched binary then fails cleanly instead of corrupting every stream.

4. **Takeover, fenced on the machine.**
   - **The fence.** On the machine, a fence file under `~/.pocket-ai/relay/` is held with `flock` and records:
     - the installation ID, which is stable across restarts and deploys (not a process or boot ID),
     - the machine entry ID,
     - the highest generation accepted so far,
     - the current relay's PID and a random token, or "none".

     The fence **outlives the relay**. When a relay exits it clears the PID, but the generation stays.
   - **Scope.** The fence is per (installation, remote account), because ports and holder sockets belong to the account. If the fence names a different machine entry of the same installation, a start is refused whatever its generation, and the card names the owning entry. The check compares (installation ID, machine entry ID) before it compares generations.
   - **Order.** A start is accepted only with a generation *higher* than the fence's. When it is accepted, the new relay records itself and ends the previous one:
     1. It sends `SIGTERM`, waits up to 5 s, then sends `SIGKILL`.
     2. It checks that the old process is gone and that its ports and sockets are free before binding anything.
     3. If they are still held after a hard 15 s timeout, the start fails with that reason.

     It never binds alongside the old relay. A server whose relay was replaced, or whose start was refused for a newer generation, stops reconnecting to that machine.
   - **Every action is fenced, not only starts.** These steps carry the caller's generation and run through `pocket-hold`, which checks the fence first:
     - ending a relay over plain SSH,
     - the port cleanup,
     - restoring a per-agent forward during fallback or switch back.

     An action with a generation lower than the fence's is refused. **Switch back and the emergency off switch advance the fence** to a new generation marked "off". A start or cleanup that was delayed from before the switch therefore finds an older generation and is refused, even though no relay is running to reject it.
   - **Killing needs proof of identity.** A process is only ended if it is a `pocket-hold relay` whose command line carries the token recorded in the fence. A matching PID and Unix account alone are not enough, because PIDs are reused.

   Holders already hand over to a new client by dropping the old one.

5. **Dead-link detection, both ways, under 30 seconds** while both hosts are awake.
   - Each side pings over the link every 5 s and expects an answer.
   - The link is declared dead when no ping in either direction has been answered for 30 s, or when the input closes. Received output does not count as an answer.
   - Server side, SSH keep-alives (5 s × 3) also apply.
   - Machine side, the relay exits on a dead link, because `sshd` often does not notice a dead client for minutes.

6. **Reconnect.**
   - Backoff goes up to one minute, as agents do today. After reconnecting, `control` sends `sync` first. Its list holds only the ports whose recorded owner is the relay (see per-port ownership below).
   - `hold` streams are then reopened a few at a time, each from its own byte offset: conversations someone is looking at first, then busy ones, then idle ones.
   - Catch-up bytes in flight across all `hold` streams are capped (start with 4 MB in total, tuned by the soak test). This keeps tool calls, `exec`, pings and live conversations responsive while 50 agents catch up.

7. **Outage behaviour is today's, stated explicitly.**
   - Agents keep working under their holders.
   - Messages wait in the queue and are left unsent after a day.
   - A holder keeps up to 256 MB of output. If more than that piles up, the oldest is dropped and the thread says output was skipped.
   - A holder ends an idle CLI nobody attached to for 24 hours.

8. **Port ownership and fallback.**
   - **One owner per port.** For each agent's tools port, the machine's state machine records exactly one owner: the relay or the per-agent SSH path. Mixed routes on one machine are allowed and expected, during a cutover, after a single failed bind, or during a per-agent switch back. What is never allowed is a single port with both a relay listener and an `-R` forward. A port changes owner only through the hand-over steps under "Pocket tools through the link".
   - **When to fall back.** A full fallback moves the whole machine to the per-agent path. It happens only when plain SSH works and either:
     - the relay fails its handshake or version check (a mismatch will not fix itself), or
     - three consecutive link starts fail over at least 5 minutes.

     A single failed reconnect is not enough.
   - **How a full fallback runs.**
     1. **All relay listeners go first.** The server ends the relay over plain SSH (a fenced action, by token).
     2. It confirms that every port the relay owned is free.
     3. Only then does it restore any per-agent forward.

     Cleanup only ever ends this account's own fenced `sshd` or `pocket-hold` processes, never an unrelated listener on a stored port.
   - **Going back to the link.** Fallback is sticky. The card has a **"Retry link"** button, and a probe runs every 30 minutes. The machine moves back only after the probe has passed the full test step twice in a row, so a flaky machine does not move back and forth.
   - **For good.** `pocket-hold attach` and the per-agent path are kept permanently. The card says when the machine fell back and why.

### Turning it on: Settings → Machines

Each machine that is set up over SSH gets a **"Use one connection"** button on its card. The relay is part of the `pocket-hold` helper, which Pocket AI already uploads to machines over SSH. The button therefore needs no new access and no manual step on the machine.

What the button does, in order, showing each step as it goes:

1. **Install.** Uploads the current `pocket-hold` to the machine if it is not there yet. This is the existing upload: a content-hashed file name, written to a temporary file and renamed into place, so a running `pocket-hold` is never overwritten. This adds a file on the machine; it does not touch running agents.
2. **Connect.** Starts a *candidate* relay over SSH in test mode and does the handshake and version check. A test-mode relay does not touch the fence, does not take over from anything, and binds nothing the agents use.
3. **Test**, with what the current phase supplies:
   - *From phase 1:* compares the environment of a command run through the candidate relay with a plain SSH command, and measures the round trip.
   - *From phase 2*, before "Move agents too" can be confirmed: starts a disposable holder (a throwaway slot running a trivial command) and checks the holder protocol against it. It never attaches to a live holder, because attaching would drop that holder's current client. It also binds an ephemeral loopback port and runs a tools round trip through it.

   At the end the candidate relay exits, any disposable holder is removed and any port is released.
4. **Confirm.** Says what will move. In phase 1 that is only status scripts. From phase 2 it also says how many running agents will move over and that their Pocket tools pause for a few seconds. Up to here, running agents keep their current connections and nothing is left running from the test. Cancelling at this step is tested.
5. **Switch.** Stores the requested mode with the machine. From phase 2 it also moves the machine's agents over, as described under "Pocket tools through the link".

If a step fails, the card shows which one and why, and the machine stays on per-agent SSH.

Afterwards the card shows the link's state:

- connected or not
- how many agents and commands run over it
- the round-trip time
- the last error
- "fell back to SSH per agent at 13:02 because …" when that happened

A **"Switch back"** button returns the machine to per-agent SSH.

Details:

- **Stored per machine, as two things.**
  - The *requested mode* is a setting on the machine's row: `off`, `status` (phase 1: read-only status scripts only) or `agents` (phase 2 onwards).
  - Each agent's *route* is stored separately. It is one of `ssh`, `to-link`, `link` or `to-ssh`. The move state is written *before* the old route is closed, and the final state only after the new route works.

  On every server start, and after any interrupted switch, the state machine reconciles the two. For each agent in a move state, it asks the relay (or, failing that, the machine over plain SSH) who actually holds the port. It then finishes or reverses that agent's move. One server-wide environment switch remains to turn the link off everywhere in an emergency. It goes through the same state machine and advances each machine's fence.
- **Consent per capability.** A machine switched on in phase 1 (`status`) is not moved to `agents` by an update. When phase 2 ships, its card offers "Move agents too", with the phase 2 test steps and its own Confirm step, because that is the step that pauses tools.
- **Who may press it:** whoever may edit the machine today (its owner; an admin for the server's own machines).
- **After a Pocket AI update** the relay is updated the same way the holder is: the server uploads the binary it will talk to before it connects. No button press is needed. A new relay serves holders started by older builds (see compatibility below).
- **Machines the helper does not support** (no `pocket-hold` build for the platform) show the button disabled with the reason.
- **New machines:** once the link is the default (phase 5), adding a machine runs the same install and test as part of its first check. The button is then only needed to switch back.

If the dial-out daemon is ever built, this is also how it would be installed: over the SSH connection the machine already has, from the same card.

### Relay and holder compatibility

The relay only copies bytes to holder sockets. The holder protocol is spoken by the server, as today.

- **The rule:** a relay at version N must serve holders started by version N‑1 or older, and the server must speak to them.
- **The header:** the holder header gets an additive `proto` field. It is JSON with optional fields, so a header without `proto` means "before the link". The server refuses with a clear message, instead of guessing, when it sees a `proto` it does not know.
- **Phase 0 test:** the extracted in-process holder client against a holder from the previous release.
- **Phase 2 test:** relay N against holder N‑1.

### Pocket tools through the link

- **Identity does not change.** Each CLI already calls its own port with its own bearer key, both stored with its holder, and the server's endpoint checks the key. `tools` streams only ever go to that one endpoint on the server, never to an arbitrary address.
- **Which ports the relay binds** is decided by the server and sent over `control`. The relay listens on nothing it was not told about. Each `bind` and `unbind` is acknowledged, and the server records a port as owned by the relay only after the `bind` acknowledgement.
- **Starting a new agent on the link: the port first.** CLIs connect to their MCP servers at startup, and some give up on a server that is not there. So the order is:
  1. `bind(port)` and wait for the acknowledgement.
  2. The start script, run as `exec`.
  3. The `hold` stream.

  This matches today, where the `-R` forward exists before the CLI launches.
- **Cutover for agents that are already running** happens when the machine moves to `agents`. On later server starts it comes up on the link directly. For each agent, one at a time:
  1. Its route is recorded as `to-link`.
  2. Its old SSH connection (which carries the `-R`) is closed and the server confirms the port is free.
  3. `bind(port)` is sent, and the relay acknowledges that it is listening.
  4. The `hold` stream is opened, and the route is recorded as `link`.

  Tool calls fail for the seconds in between. Agents not yet moved keep their `-R` forwards meanwhile; each port has one owner throughout.
- **If a bind fails for an agent,** that agent is put back on the per-agent path, and the cutover continues with the next one. The machine then runs mixed routes, which is allowed. If putting the agent back also fails, the card and the thread name the agent and the reason.
- **A stale `sshd` still holding the port:** the existing cleanup runs for it, as a fenced action. This is the cleanup that failed on the first try today. Its fix gates phase 2.
- **A stored port taken by an unrelated program:** that program is left alone. The agent is treated like one whose link is down, and the thread says why.
- **Switch back, per agent.** For each agent, one at a time, while the relay keeps serving the rest:
  1. Its route is recorded as `to-ssh`.
  2. `unbind(port)` is sent and acknowledged.
  3. The server confirms the port is free.
  4. The per-agent forward is restored, and the route is recorded as `ssh`.

  After the last agent has moved, the relay exits and the fence advances to "off".
- **Full fallback** (the link is dead, so `unbind` cannot be sent) follows the fallback rule instead: all relay listeners go before any forward is restored.

### Short commands (`exec`)

- **Same environment by construction.** The relay is started through the same `sshd` → login shell → `/bin/sh` path as today's commands, and scripts keep their profile and `PATH` preamble. The button's test step diffs `env` between the two paths on each machine.
- **Input and cancellation are separate.** A `stdin_eof` frame closes the child's stdin; commands that read stdin to the end (uploads among them) then finish normally. Only a `cancel` frame, the stream being reset, or the timeout ends the child's whole process group.
- **At most once, decided at dispatch.**
  - Once the request frame has been sent, the command may have run. If the link dies before its exit status arrives, the result is "unknown", not a retry.
  - Automatic retry or fallback to plain SSH happens only when the command provably never reached the relay, or when the call site is marked safe to repeat.
  - Each of the 29 call sites gets a mark, and the default is *not* repeatable. Status and listing scripts are marked repeatable.
- **Agent starts are atomic per slot and safe to repeat.**
  - The holder already takes an exclusive `flock` on the slot's `lock` file before it starts the CLI (`serve.go`). A second `pocket-hold serve` for the same slot waits until it can reach the first holder's socket, and starts nothing. The slot claim therefore already covers the window before the socket exists, and both transports go through it.
  - Two changes are made. First, `attach -start-only` relies on that claim, not on a separate "is the socket there" check. Second, a second start that gives up waiting (today after about 10 s, while exiting 0) reports "slot busy, not ready" instead of success, so the server reconciles rather than assuming a holder exists.
  - After an "unknown" or "slot busy" start, the server asks the slot's status before any new launch, so a thread is never left stuck and never gets two CLIs.
  - The holder detaches into its own session (`Setsid`). Cancelling or timing out the start script therefore ends the script's process group and leaves a successfully started holder running. The cancellation test tells the two apart.
- **Plain-SSH steps stay on plain SSH:**
  - uploading the `pocket-hold` binary itself, which has to happen before the relay can run;
  - the fenced relay end;
  - the cleanup and forward restore during fallback.

## Phases

Each phase ships on `main`. A machine uses the link only once it is switched on for that machine, with the current path as fallback.

**0. Groundwork (small, no behaviour change). Start now.**
- Split `link.go`'s "start a process and read its stdout" from its resume logic behind an interface.
- Move the client half of `attach.go` into a library so the server can speak to a holder in-process.
- Add the `proto` field to the holder header.
- Run the existing held-agent tests against both implementations. Run the extracted client against a holder from the previous release.

**1. The link, proven on harmless traffic (medium)**
- `pocket-hold relay` with the lifecycle rules above (controller lease, fence, takeover, two-way detection, fallback), `control` (pings and status; no ports yet) and `exec`.
- `machineLink` and the per-machine state machine on the server.
- The **"Use one connection"** button with its install and connect steps and the phase 1 test step (`env` diff and round trip), the `status` mode, and the link state on the machine's card. In this phase, switching a machine on moves only read-only status scripts (logins check, machine check) to the link. They are safe to repeat and people notice the speed.
- Exit test: the phase 1 gates below pass on a Linux box and on a real Mac.

**Before phase 2:** fix today's "port still held by an old connection" recovery. It must free the port on the first try and never touch an unrelated listener. Cutover and switch back depend on it.

**2. Agents and their tools together (medium to large)**
- `hold` and `tools` streams and the `bind`/`unbind`/`sync` commands, switched on together, so no agent is ever on the link without its tools.
- Starting an agent: `bind`, then an `exec` of today's start script with a new `attach -start-only` flag (atomic per slot, as above), then a `hold` stream. Profile, folder, login folder and the Mac GUI bridge behave as now.
- The `agents` mode, with per-agent routes, per-port ownership and reconciliation. The phase 2 test steps (disposable holder, tools round trip) run before "Move agents too" can be confirmed. Press it on one of your machines first, then Arno's.
- Exit test: the phase 2 gates below pass on Linux and a real Mac.

**3. Stop parking where the link is on (small)**
- Machines on the link keep a stream per tracked agent; `park.go` stays for machines on the fallback path.
- Gated on the idle-expiry tests below.

**4. All short commands over the link (medium, mechanical)**
- The remaining call sites, each marked safe to repeat or not (default not); uploads using `stdin_eof`; terminals if wanted.

**5. Default on (small)**
- New machines are set up on the link as part of their first check; existing ones are offered the button.
- The fallback path and "Switch back" stay.

Out of scope: a machine-side daemon that dials out. The link is written against a plain byte stream, so it can be added later without changing the protocol.

## Tests that gate each switch-on

Soak conditions used below: simulated WAN with 100 ms round trip, 1% packet loss and 10 Mbit/s. These figures are my assumption, to be tuned once, then fixed before the test is run.

### Phase 0 gates

| Test | Passes when |
|---|---|
| Existing held-agent tests, both implementations | all pass |
| Extracted holder client against a holder from the previous release | output, input, offsets and exit work |

### Phase 1 gates (handshake, ownership, read-only commands)

| Test | Passes when |
|---|---|
| Stray output on the SSH channel before the handshake | clean error, no stream corruption |
| Half-open connection (packets dropped, no reset), both hosts awake | both sides notice in under 30 s; the old relay is gone |
| One-way stall: frames from the machine keep arriving while frames to it are dropped | declared dead within 30 s; output arriving does not hide it |
| Second relay of a higher generation starts while the first is alive | the first ends, and the second binds only after the first has exited |
| Relay of a lower generation starts while a newer one runs | it refuses and exits; the newer one is untouched |
| Two server processes of one installation during a deploy overlap | only the lease holder allocates generations; the old process stops its links; no back-and-forth takeovers |
| A stale controller reconnects after its relay has exited, and tries to end its replacement | both refused by the fence |
| A delayed start and a delayed cleanup arrive after Switch back | both refused; nothing is bound or killed |
| A recorded PID reused by an unrelated process | it is not killed |
| Two machine entries reaching the same account | the second is refused with a clear reason, whatever its generation |
| Fallback trigger: three failed starts over 5 minutes, then the link recovers | exactly one fallback; return only after two passing probes |
| `exec`: link cut after the request was sent | reported as unknown, not run twice |
| `exec`: lost acknowledgement after a side effect | reported as unknown; the side effect happened once |
| `exec`: stream cancelled | no child process left on the machine |
| `exec`: command that reads stdin to EOF | gets `stdin_eof`, finishes, and its output arrives |
| Button: a test step fails | the card names the step; the machine and its agents are unchanged |
| Button: cancelled at Confirm | no relay, disposable holder or bound port left on the machine; the fence is unchanged; running agents never lost their connections |
| Real Mac | handshake, status scripts, sleep and wake with a network change (Wi-Fi to another network or Tailscale address change); no stale relay after wake |

### Phase 2 gates (live agents, tools, cutover, switch back)

| Test | Passes when |
|---|---|
| Kill the network mid-turn, reconnect | no byte lost or read twice on any agent; ports and sockets come back |
| 100 streams, one flooding, on the simulated WAN, with 50 agents catching up | no false dead-link trips; a small tool call's p95 round trip stays under 2 s; a viewed conversation's new output appears within 2 s |
| Server restart with 50 held agents on one machine | all 50 are reattached *and* each has reached its tools endpoint within 60 s; time to drain the backlog is measured and reported separately |
| A newly started agent on the link | its first tool call succeeds |
| Agent started before the upgrade | adopted through the relay; makes a Pocket tool call after enable, after a reconnect, and after switch back |
| Relay N with a holder started by N‑1 | output, input and tools work |
| Second start for a slot while the first holds the slot lock but has no socket yet | exactly one CLI; the second reports "already running" or "slot busy", never a second launch |
| Agent start with a lost acknowledgement | the server reconciles against the slot; exactly one CLI runs |
| Start script cancelled after the holder detached | the script's process group is gone; the holder survives |
| Button: pressed with agents mid-turn | every agent keeps working; no byte lost; tool calls work again within seconds |
| "Switch back" with agents mid-turn | same, in the other direction; the relay exits only after the last agent moved |
| Every cutover, switch back and fallback test | no port ever has both a relay listener and an `-R` forward |
| Server crash after agent 7 of 50 has moved, in both directions | after restart, reconciliation leaves every agent on exactly one path with working tools |
| Crash after a `bind` succeeded but before the route was recorded | reconciliation finds the relay owns the port and records it; no double owner |
| Bind fails for one agent during cutover, then the server restarts with routes mixed | that agent stays on the per-agent path, the others on the link; `sync` lists only the relay's ports |
| Full fallback with mixed routes | every relay listener is gone before any forward is restored |
| An old relay's reconnect arrives during switch back | refused by the fence; switch back completes |
| A stored port held by an unrelated program | that program survives cutover and switch back; the agent shows why its tools are unavailable |
| Pocket AI updated while a machine is on the link | the new relay is uploaded and used without anyone pressing anything |
| Real Mac | GUI-session start, sleep and wake mid-turn with a network change, switch back |

### Phase 3 gates (no parking on the link)

| Test | Passes when |
|---|---|
| Agent the server stops tracking, idle | its stream is closed, and its holder ends it after 24 h as today |
| Busy agent with its stream lost | its turn is never cut; it is reattached at the right offset |

## How we will know it worked

- Connections to a machine in steady state: **1**, whatever the number of agents. Short-lived connections for bootstrap, migration and fallback are counted separately.
- A dead link, including a one-way stall, is noticed in under 30 seconds on both ends while both hosts are awake.
- 50 held agents on one machine are reattached, with tools working, within 60 seconds of a server restart.
- A short command on a person's machine no longer pays an SSH handshake.
- No "remote port forwarding failed" lines.
- No fallback flapping: at most one fallback per machine per incident.

## Main risks, most serious first

1. **Ports caught between two paths or two owners.** This could come from fallback flapping, overlapping server processes, delayed requests, or an interrupted switch. Countered by:
   - one state machine per machine and a controller lease;
   - a fence on the machine that outlives the relay;
   - exactly one recorded owner per port;
   - move states written before the old route is closed;
   - slow fallback;
   - fixing the existing port cleanup before phase 2.
2. **A duplicated side effect or launch after an ambiguous result.** Countered by at-most-once dispatch, a default of no replay, and starts that are atomic per slot.
3. **Shared congestion.** One TCP connection under two layers of flow control could delay tools, output and pings together, and turn a slow link into a machine-wide reconnect. Countered by conservative windows and the catch-up cap; proven only by the soak test.
4. **A long-lived link to a Mac across sleep and wake.** Still unjudged; it needs the real-Mac tests.
5. **Shared blast radius.** A relay crash drops every stream on that machine at once. Countered by a minimal relay with panic isolation per stream; agents survive under their holders either way.
6. **Phase 1 is large** (relay, controller lease, fence, `machineLink`, state machine, button and its UI). It can ship in more than one release behind the `status` mode.

## Decisions for you

1. **yamux as a dependency.** Recommended by me and by every council round. Yes or no?
2. **Build on `main` behind the per-machine switch**, in small releases as the project already works, with phase 0 first. The alternative is a long-lived branch, which I would avoid given how fast `main` moves.

## Still disputed or unverified

- **Native SSH channels instead of yamux** (raised by one model in round one): rejected for the reasons above; not tested.
- **Per-agent token on the tools port** (one model in round one): not added. Each call already carries the agent's bearer key, and the loopback exposure is the same as with `-R` today.
- **Two layers of flow control** under 50 parallel catch-ups: believed manageable with the countermeasures above; unproven until the soak test.
- **macOS sleep with one long-lived link:** nobody could judge it; it needs the real-Mac tests.
- **The numbers** are starting values, not measurements: 4 MB catch-up cap, 2 s p95, 5 s / 15 s takeover timeouts, 10 s / 30 s lease, three failures over 5 minutes, 30-minute probe.
  - *Measured since:* the catch-up cap is **4 streams catching up at once (about 1 MiB in flight)**, not 4 MB. The WAN soak (100 ms round trip, 10 Mbit/s, stalls) kept small round trips at p95 ≈ 1 s with 4, while 16 streams (about 4 MiB) put them at ≈ 2.9 s, over the 2 s bound. Roughly one second of the link's bandwidth is right.
- **Checked against the source (0.120.8):**
  - the holder's one-client handover
  - the 256 MB buffer
  - idle expiry only when nobody is attached, and parking's 6-hour reattach
  - the stored port and key
  - content-hashed uploads written to a temporary file and renamed
  - `muxArgs` on the server's own machines
  - no version field in the holder header today
  - the holder's per-slot `flock` taken before the CLI starts, and the ~10 s wait that exits 0
  - `Setsid` on holder start

  The rest of the design is untested.

---

## Appendix: history

### What version 4 added (third council round on version 3)

- One owner per machine's link; a stable installation ID and increasing generations on the lock; bounded takeover with escalation; stale servers stop reconnecting.
- Fallback that is slow to trigger, sticky, and ends the old relay over plain SSH first.
- A `control` stream for tools port binding; per-agent cutover order; per-agent recovery when a bind fails.
- A test step that changes nothing: a test-mode relay and a disposable holder. A requested mode stored separately from per-agent routes, with reconciliation after crashes. Separate consent for `status` and `agents`.
- `exec`: `stdin_eof` separate from `cancel`; "unknown" decided at dispatch; no replay by default; slot-keyed, repeatable agent starts.
- A relay/holder compatibility rule and a `proto` header field.
- The port-cleanup fix moved from "independent" to a gate for phase 2.
- ControlMaster added as a rejected alternative.
- Test gates split by phase, with a specified soak workload, latency bounds, a stricter "50 agents back", and crash, race and idle-expiry tests.

### What version 3 added

- A "Use one connection" button per machine in Settings → Machines: install over the existing SSH connection, connect, test, confirm, switch; link state on the card; "Switch back".
- The switch is stored with the machine instead of in an environment variable; one server-wide off switch remains.
- The button arrives in phase 1 (status scripts only) and gains the agent cutover in phase 2.
- Four tests for the button and for updates.

### What the first council round changed (version 2)

- Added the link lifecycle section: single start, takeover lock, dead-link detection with a number, staggered reconnect, handshake before multiplexing.
- Removed the `watch` stream and "the relay keeps holders attached"; the server keeps one stream per agent instead.
- Agents and their tools move to the link in the same phase, with a spelled-out cutover and rollback.
- Reordered: the link first carries only harmless status scripts, then agents.
- `exec` got at-most-once semantics, process-group cleanup and a per-call-site "safe to repeat" mark.
- The per-agent path is kept permanently as a fallback.
- A gating test table with pass criteria, including a slow-link soak and a real Mac.
- The dial-out daemon is out of scope rather than an optional phase.