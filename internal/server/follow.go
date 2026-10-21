package server

import (
	"bytes"
	"context"
	"encoding/json"
	"time"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

// Following: while anyone has a thread open, Pocket AI reads the CLI's own
// session log and turns what was appended into timeline items, so a session
// driven from a terminal, another app or another Pocket AI process shows up
// live on every device that has it open. While Pocket AI drives the session
// itself the follower only moves its position forward (the live process
// already produced those items).

type follower struct {
	n      int
	cancel context.CancelFunc
}

const externalQuiet = 2 * time.Minute // no new lines for this long: the other side stopped

// watch is called for each open SSE stream of a thread; the returned func
// is called when it closes. The first watcher starts the follower.
func (rt *engine) watch(t *store.Thread) func() {
	if t.ProviderSessionID == "" {
		return func() {}
	}
	rt.mu.Lock()
	f := rt.followers[t.ID]
	if f == nil {
		ctx, cancel := context.WithCancel(rt.ctx)
		f = &follower{cancel: cancel}
		rt.followers[t.ID] = f
		switch t.Provider {
		case "claude":
			go rt.followClaude(ctx, t.ID)
		case "codex":
			go rt.followCodex(ctx, t.ID)
		}
	}
	f.n++
	rt.mu.Unlock()
	return func() {
		rt.mu.Lock()
		defer rt.mu.Unlock()
		if f.n--; f.n <= 0 {
			f.cancel()
			delete(rt.followers, t.ID)
		}
	}
}

// external marks a thread as running elsewhere (or not) and tells everyone.
func (rt *engine) setExternal(threadID string, on bool) {
	rt.mu.Lock()
	was := rt.external[threadID]
	if on {
		rt.external[threadID] = true
	} else {
		delete(rt.external, threadID)
	}
	rt.mu.Unlock()
	if was == on {
		return
	}
	status := "idle"
	if on {
		status = "running"
	}
	rt.setStatus(threadID, status, "")
}

func (rt *engine) isExternal(threadID string) bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.external[threadID]
}

func (rt *engine) followClaude(ctx context.Context, threadID string) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	defer rt.setExternal(threadID, false)
	var path, sid string
	var lastLine time.Time
	log := &agent.ClaudeLog{}
	n := 0
	defer rt.setExtSubs(threadID, nil)
	for {
		t, err := rt.s.store.GetThreadAny(ctx, threadID)
		if err != nil {
			return
		}
		if t.ProviderSessionID != sid { // a fork or the first message gave it a new session
			sid, path = t.ProviderSessionID, ""
		}
		if path == "" && sid != "" {
			path, _ = agent.FindClaude(rt.s.cfg.ClaudeHome, sid)
		}
		if path != "" {
			rt.claudeTick(ctx, t, path, log, &lastLine)
			n++
			rt.claudeElsewhere(t, path, n%3 == 1)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// claudeElsewhere: for a session another program drives, Claude Code's own
// process registry says whether it is working — also while it only waits for
// background sub-agents, when its log stays quiet. Those sub-agents are read
// from their own transcripts (every few seconds: scan).
func (rt *engine) claudeElsewhere(t *store.Thread, path string, scan bool) {
	if rt.get(t.ID) != nil {
		rt.setExtSubs(t.ID, nil) // Pocket AI drives it: its own events say it all
		return
	}
	procs, ok := agent.ClaudeProcs(rt.s.cfg.ClaudeHome)
	if !ok {
		return // no registry (older Claude Code): the log-based guess stands
	}
	p, found := procs[t.ProviderSessionID]
	busy := found && p.Busy()
	if !busy {
		rt.setExtSubs(t.ID, nil)
	} else if scan {
		rt.setExtSubs(t.ID, agent.ExternalSubagents(path))
	}
	rt.setExternal(t.ID, busy)
}

// setExtSubs stores the sub-agents of a session run elsewhere and tells the
// people watching when they change.
func (rt *engine) setExtSubs(threadID string, subs []agent.Subagent) {
	rt.mu.Lock()
	old := rt.extSubs[threadID]
	sameSet := len(old) == len(subs)
	sameSteps := sameSet
	for i := 0; sameSet && i < len(subs); i++ {
		sameSet = old[i].ID == subs[i].ID
		sameSteps = sameSteps && old[i].Activity == subs[i].Activity
	}
	// sub-agents coming or going go out at once; only their current step
	// changing, at most every 15 s (each update re-renders open pages)
	publish := !sameSet || (!sameSteps && time.Since(rt.extSubsAt[threadID]) > 15*time.Second)
	if publish || len(subs) == 0 {
		if len(subs) == 0 {
			delete(rt.extSubs, threadID)
		} else {
			rt.extSubs[threadID] = subs
		}
		rt.extSubsAt[threadID] = time.Now()
	}
	rt.mu.Unlock()
	if publish {
		if t, err := rt.s.store.GetThreadAny(rt.ctx, threadID); err == nil {
			rt.publishThread(t)
		}
	}
}

func (rt *engine) claudeTick(ctx context.Context, t *store.Thread, path string, log *agent.ClaudeLog, lastLine *time.Time) {
	size := agent.FileSize(path)
	if size < 0 {
		return
	}
	if rt.get(t.ID) != nil || t.LogOffset < 0 {
		// Pocket AI drives it (its own items are already there), or we have
		// never read it: continue from the end.
		if t.LogOffset != size {
			rt.saveOffset(ctx, t.ID, size)
		}
		rt.setExternal(t.ID, false)
		return
	}
	lines, next, err := agent.ClaudeTail(path, t.LogOffset)
	if err != nil || next == t.LogOffset {
		if rt.isExternal(t.ID) && time.Since(*lastLine) > externalQuiet {
			rt.setExternal(t.ID, false)
		}
		return
	}
	*lastLine = time.Now()
	ended := false
	for _, line := range lines {
		for _, op := range log.Feed(line) {
			switch {
			case op.Item != nil:
				rt.saveItem(t.ID, *op.Item)
				if op.Item.Kind == "assistant" {
					rt.setPreview(ctx, t.ID, op.Item.Text)
				}
			case op.Result != nil:
				if st, err := rt.s.store.GetItem(ctx, t.ID, op.Result.ToolUseID); err == nil {
					var it agent.Item
					_ = json.Unmarshal(st.Data, &it)
					if it.Tool != nil {
						op.Result.Apply(it.Tool)
						rt.saveItem(t.ID, it)
					}
				}
			}
			if op.TurnStarted {
				ended = false
				if op.Item != nil {
					_, _ = rt.s.store.DB.ExecContext(ctx, `UPDATE threads SET last_prompt = ?, prompt_at = ? WHERE id = ?`, promptLine(op.Item.Text, 0), store.Now(), t.ID)
				}
				rt.setExternal(t.ID, true)
			}
			if op.TurnEnded {
				ended = true
			}
		}
	}
	rt.saveOffset(ctx, t.ID, next)
	if ended {
		rt.setExternal(t.ID, false)
	} else if len(lines) > 0 && !rt.isExternal(t.ID) {
		rt.setExternal(t.ID, true) // tool calls and replies of a turn started before we looked
	}
}

func (rt *engine) followCodex(ctx context.Context, threadID string) {
	defer rt.setExternal(threadID, false)
	var reader *agent.CodexReader
	defer func() {
		if reader != nil {
			reader.Close()
		}
	}()
	var path string
	var lastSize int64 = -2
	var lastChange time.Time
	tick := time.NewTicker(1500 * time.Millisecond)
	defer tick.Stop()
	first := true
	for {
		t, err := rt.s.store.GetThreadAny(ctx, threadID)
		if err != nil {
			return
		}
		size := int64(-1)
		if path != "" {
			size = agent.FileSize(path)
		}
		live := rt.get(threadID) != nil
		if !live && (first || size != lastSize) {
			if reader == nil {
				if reader, err = agent.OpenCodexReader(ctx, rt.s.cfg.CodexBin); err != nil {
					rt.s.log.Debug("follow codex", "thread", threadID, "err", err)
					reader = nil
				}
			}
			if reader != nil {
				if items, p, err := reader.Read(ctx, t.ProviderSessionID); err == nil {
					path = p
					size = agent.FileSize(path)
					rt.reconcileCodex(ctx, t, items, !first)
				}
			}
			if !first && size != lastSize {
				lastChange = time.Now()
				rt.codexActivity(threadID, path, lastSize)
			}
			first = false
		}
		if live {
			rt.setExternal(threadID, false)
		} else if rt.isExternal(threadID) && time.Since(lastChange) > externalQuiet {
			rt.setExternal(threadID, false)
		}
		lastSize = size
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// codexActivity looks at what was appended to the rollout file to tell a
// running turn from a finished one.
func (rt *engine) codexActivity(threadID, path string, from int64) {
	if from < 0 {
		return
	}
	lines, _, err := agent.ClaudeTail(path, from) // same newline-delimited reading
	if err != nil {
		return
	}
	state := 0
	for _, l := range lines {
		switch {
		case bytes.Contains(l, []byte(`"type":"task_complete"`)), bytes.Contains(l, []byte(`"type":"turn_aborted"`)):
			state = -1
		case bytes.Contains(l, []byte(`"type":"task_started"`)):
			state = 1
		}
	}
	switch state {
	case 1:
		rt.setExternal(threadID, true)
	case -1:
		rt.setExternal(threadID, false)
	}
}

// reconcileCodex writes the items that are new or changed. Items before the
// last one Pocket AI already has are left alone (a thread Pocket AI drove
// stores the user's messages under its own keys).
func (rt *engine) reconcileCodex(ctx context.Context, t *store.Thread, items []agent.Item, announce bool) {
	have, err := rt.s.store.ListItems(ctx, t.ID)
	if err != nil {
		return
	}
	stored := map[string]string{}
	for _, it := range have {
		stored[it.Key] = string(it.Data)
	}
	last := -1
	for i, it := range items {
		if _, ok := stored[it.Key]; ok {
			last = i
		}
	}
	for i, it := range items {
		if it.Tool != nil && it.Tool.Status == "running" && !announce {
			t := *it.Tool
			t.Status = "done"
			it.Tool = &t
		}
		data, _ := json.Marshal(it)
		old, ok := stored[it.Key]
		if !ok && i < last {
			continue
		}
		if ok && it.Kind == "turn" {
			continue // Pocket AI's own footer has cost and tokens; the log has only time
		}
		if ok && old == string(data) {
			continue
		}
		rt.saveItem(t.ID, it)
		if it.Kind == "assistant" {
			rt.setPreview(ctx, t.ID, it.Text)
		}
	}
}

func (rt *engine) saveOffset(ctx context.Context, threadID string, off int64) {
	_, _ = rt.s.store.DB.ExecContext(ctx, `UPDATE threads SET log_offset = ? WHERE id = ?`, off, threadID)
}

func (rt *engine) setPreview(ctx context.Context, threadID, text string) {
	if text == "" {
		return
	}
	_, _ = rt.s.store.DB.ExecContext(ctx, `UPDATE threads SET preview = ?, activity_at = ? WHERE id = ?`, oneLine(text, 200), store.Now(), threadID)
}

// endOffset moves a Claude thread's log position to the end of its file
// (after Pocket AI's own process wrote to it, or right after an import).
func (rt *engine) endOffset(t *store.Thread) {
	if t.Provider != "claude" || t.ProviderSessionID == "" {
		return
	}
	if path, err := agent.FindClaude(rt.s.cfg.ClaudeHome, t.ProviderSessionID); err == nil {
		if size := agent.FileSize(path); size >= 0 {
			rt.saveOffset(rt.ctx, t.ID, size)
		}
	}
}
