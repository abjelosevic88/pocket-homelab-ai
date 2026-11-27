package server

// Which short commands may go over a machine's link (docs/machine-link-plan.md,
// "Short commands (exec)"), call site by call site. Each call of
// Remote.Run / RunInput / Put / PutPrivate / Stream carries a mark in its
// context (agent/remote_link.go):
//
//   StatusScript  read-only status script: safe to repeat, and over the link
//                 already in mode "status" (phase 1)
//   Repeatable    safe to run twice: over the link in mode "agents" (phase 4);
//                 when the link drops while it runs, it is run again over ssh
//   PlainSSH      never over the link
//   (none)        the default, NOT repeatable: over the link in mode "agents";
//                 when the link drops while it runs, the caller gets "the
//                 result is unknown: the link to <machine> dropped while it ran"
//
// A command that provably never reached the machine (agent.ErrLinkNotSent)
// is run over ssh whatever its mark.
//
// | where                              | what                                   | mark         | why |
// |------------------------------------|----------------------------------------|--------------|-----|
// | hosts.go checkHost                 | hostProbe (the machine check)          | StatusScript | reads versions and logins |
// | hosts.go listRemoteDirs            | remoteDirs (folder picker)             | Repeatable   | lists folders |
// | host_logins.go hostLogins          | hostStatusScript (the logins check)    | StatusScript | reads `auth status` of each login |
// | host_logins.go handleHostLoginStart| token file there? (`[ -s …]`)          | Repeatable   | a test |
// | host_logins.go hostLoginViaServer  | RunInput writeTokenScript              | default      | writes a sign-in (upload) |
// | host_logins.go handleHostLoginAdd  | hostAddLoginScript                     | default      | makes a login folder |
// | host_logins.go handleHostLogout    | rm the token file                      | default      | deletes |
// | host_logins.go handleHostLogout    | the CLI's logout                       | default      | signs out |
// | host_logins.go handleHostLoginRemove | hostRemoveLoginScript                | default      | moves a folder away |
// | acp.go copy a sign-in              | readTokenScript                        | Repeatable   | reads a file |
// | acp.go copy a sign-in              | hostAddLoginScript                     | default      | makes a login folder |
// | acp.go copy a sign-in              | RunInput writeTokenScript              | default      | writes a sign-in (upload) |
// | held.go installHolder              | uname, "is it there", Put, chmod       | PlainSSH     | the pocket-hold upload: the relay is part of it, so it comes first |
// | held.go holderStatus               | `pocket-hold status`                   | Repeatable   | reads; not StatusScript: it decides adoption, phase 1 keeps to the two checks |
// | held.go stopHolder                 | `pocket-hold stop`                     | default      | ends an agent |
// | cost_repair_full.go                | RemoteClaudeRestoresSh                 | Repeatable   | reads session files |
// | cli_update.go                      | update/install a CLI                   | default      | installs software |
// | folders_api.go handleMkdir         | mkdir -p                               | default      | writes (idempotent, but default) |
// | folders_api.go handleClone         | git clone                              | default      | writes, long |
// | sentfiles.go                       | sentRead (base64 of a file)            | Repeatable   | reads a file |
// | move_folder.go                     | remoteMoveSession                      | default      | moves a session file |
// | reconnect.go probeClaude           | probeSh (process + log tail)           | Repeatable   | reads |
// | reconnect.go endTree               | endTreeSh                              | default      | kills processes |
// | reconnect.go codexActive           | find the rollout changed lately        | Repeatable   | reads |
// | rewind.go                          | find the session file                  | Repeatable   | reads |
// | rewind.go                          | cat the session file                   | Repeatable   | reads |
// | rewind.go                          | Put the rewound copy                   | default      | writes (upload) |
// | git_api.go remoteGitInfo           | git status/branch/log …                | Repeatable   | reads (GIT_OPTIONAL_LOCKS=0) |
// | git_api.go gitOn                   | any one git command                    | default      | the helper also runs ones that write |
// | files.go remoteList                | remoteFiles                            | Repeatable   | lists |
// | files.go remoteFile                | remoteRead                             | Repeatable   | reads |
// | files.go upload                    | RunInput remoteUpload                  | default      | writes (upload) |
// | files.go file op                   | remoteFileOp (rename, delete, mkdir)   | default      | writes |
// | runtime.go cost of a resumed turn  | RemoteClaudeCostSh                     | Repeatable   | reads |
// | runtime.go folder still there?     | remoteFolderCheck                      | Repeatable   | reads |
// | runtime.go CLI there?              | command -v                             | Repeatable   | a test |
// | runtime.go remoteEnsureSession     | copies a session between logins        | default      | writes |
// | worktree_git.go shOn               | every worktree script                  | default      | they create, archive and remove worktrees |
// | skills_sync.go remoteSkills        | RunInput skillsViewSh                  | Repeatable   | lists |
// | skills_sync.go push                | RunInput skillsPushSh                  | default      | writes (upload) |
// | skills_sync.go remove              | RunInput skillsRemoveSh                | default      | deletes |
// | skills_sync.go pull                | RunInput skillsPullSh                  | Repeatable   | reads there (tar to stdout) |
// | park.go watchOnce                  | Stream parkWatchScript                 | —            | Stream never uses the link; agents on the link are not parked (phase 3), so it only watches agents on the per-agent path |
// | machine_link.go (m.plain)          | fence actions, "plain ssh works", env over ssh | PlainSSH | fenced relay end, fallback cleanup, the Test step's ssh side |
// | machine_link.go (m.relayCmd)       | the relay itself                       | —            | its own ssh process (agent.Remote.LinkCommand) |
// | machine_link_agents.go startOnly   | the agent's start script, -start-only  | link exec    | atomic per slot; unknown or slot busy: the slot is asked first |
// | machine_link_agents.go slotStatus  | `pocket-hold status` of a slot         | link, else PlainSSH | reads |
// | machine_link_agents.go holdersGone | `pocket-hold status` of every routed slot | link exec | reads (after each connect) |
// | machine_link_agents.go freeAgentPort | fenced free-ports -slot S P          | PlainSSH     | the per-agent path's cleanup, fenced |
// | machine_link_agents.go testAgents  | test holder, probe, its cleanup        | candidate    | the button's test relay only |
//
// In the agent package (o.Remote of a running agent, mode "agents" only):
// MirrorSessions and the Antigravity usage script are Repeatable (they read);
// PutPrivate of the MCP config (an upload: stdin, then its end) and the
// holder stop in link.go keep the default (the stop goes over the agent's
// hold stream when it can). FreeForwards is PlainSSH: it is the per-agent
// path's cleanup; on a machine with a fence the agent's route gives a fenced
// one instead (HoldPath.Free), and its connection runs behind the fence's
// check (HoldPath.Guard).
//
// Not over the link: terminals (Remote.Shell, a tty over its own ssh). The
// plan leaves pty streams optional; they are not built.
