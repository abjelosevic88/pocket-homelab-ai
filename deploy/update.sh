#!/usr/bin/env bash
# Put a new Pocket AI binary in place without cutting agents off.
#
#   deploy/update.sh ./pocket-ai                 # installs to ~/.local/bin/pocket-ai
#   deploy/update.sh ./pocket-ai /path/to/pocket-ai
#
# A plain `systemctl --user restart pocket-ai` stops every agent mid-turn and
# leaves the ones on other machines running on their own (docs/design.md,
# "After a restart"). This keeps the old binary as <binary>.bak-<old version>,
# swaps the new one in, and restarts only when no agent is working: while one
# is (a turn, queued messages, sub-agents, background shells), the server gets
# SIGUSR1 and restarts itself into the new binary as soon as none is.
# PORT (default 8195) is where /healthz answers; UNIT the systemd user unit.
set -euo pipefail
NEW="${1:?usage: update.sh NEW-BINARY [INSTALLED-BINARY]}"
DEST="${2:-$HOME/.local/bin/pocket-ai}"
UNIT="${UNIT:-pocket-ai}"
PORT="${PORT:-8195}"

[ -x "$NEW" ] || { echo "not an executable: $NEW" >&2; exit 1; }
newv=$("$NEW" --version 2>/dev/null | awk '{print $NF}') || true
[ -n "$newv" ] || { echo "$NEW does not run here (wrong architecture?)" >&2; exit 1; }

if [ -e "$DEST" ] && ! [ "$NEW" -ef "$DEST" ]; then
  oldv=$("$DEST" --version 2>/dev/null | awk '{print $NF}') || oldv=unknown
  [ -e "$DEST.bak-$oldv" ] || cp -p "$DEST" "$DEST.bak-$oldv"
  echo "kept $oldv as $DEST.bak-$oldv"
fi
if ! [ "$NEW" -ef "$DEST" ]; then
  install -m 755 "$NEW" "$DEST.new"
  mv -f "$DEST.new" "$DEST" # the running server keeps its old file until it restarts
fi

pid=$(systemctl --user show -p MainPID --value "$UNIT")
working=$(curl -fsS -m 3 "http://127.0.0.1:$PORT/healthz" 2>/dev/null | grep -o '"agents_working":[0-9]*' | cut -d: -f2 || true)
if [ "${pid:-0}" != 0 ] && [ -n "$working" ] && [ "$working" -gt 0 ]; then
  # the main process only: `systemctl kill -s USR1` would signal the whole
  # unit, and Claude Code dies on USR1
  kill -USR1 "$pid"
  echo "Installed $newv. $working agent(s) working: Pocket AI restarts into it by itself once none is."
  echo "Follow it with: journalctl --user -u $UNIT -f"
  exit 0
fi
systemctl --user restart "$UNIT"
for _ in 1 2 3 4 5 6 7 8 9 10; do
  sleep 1
  if curl -fsS -m 3 "http://127.0.0.1:$PORT/healthz" 2>/dev/null; then echo; echo "Running $newv."; exit 0; fi
done
echo "Pocket AI did not answer on :$PORT after the restart: journalctl --user -u $UNIT -e" >&2
exit 1
