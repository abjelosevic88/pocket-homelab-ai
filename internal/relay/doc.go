// Package relay is the machine link (docs/machine-link-impl.md): one ssh
// connection per person and machine that carries every agent's holder
// stream, the Pocket tools traffic and short commands, multiplexed with
// yamux. The machine side runs as `pocket-hold relay` (Serve) and keeps its
// fence (`pocket-hold fence`); the server side is Client.
package relay
