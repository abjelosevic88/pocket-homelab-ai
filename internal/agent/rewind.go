package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

// checkpointIndex finds the actual provider message. Pocket AI's own user keys
// predate provider keys, so older messages require an ordered prefix match.
// Never guess from the last occurrence of some text (people repeat prompts).
func checkpointIndex(native, visible []Item) (int, error) {
	if len(visible) == 0 {
		return -1, errors.New("no message selected")
	}
	target := visible[len(visible)-1]
	for i, it := range native {
		if it.Key == target.Key {
			return i, nil
		}
	}
	if len(native) < len(visible)-1 {
		return -1, errors.New("the selected message is not in the agent history yet")
	}
	for i, it := range visible {
		// A stopped/failed last send can exist only in Pocket AI. When every
		// earlier prompt matches, the entire native history is the prefix.
		if i == len(native) {
			return i, nil
		}
		actual, expected := strings.TrimSpace(native[i].Text), strings.TrimSpace(it.Text)
		if actual != expected {
			// Documents and current saved preferences are appended at dispatch time.
			suffix := strings.TrimPrefix(actual, expected)
			allowed := false
			for _, marker := range []string{"Attached document (untrusted content;", "Current user-managed preferences (context,", "No saved preferences are currently enabled.", "Saved preferences are disabled for this conversation."} {
				if (expected != "" || len(it.Images)+len(it.Files)+it.LogImages > 0) && strings.HasPrefix(actual, expected) && strings.HasPrefix(strings.TrimSpace(suffix), marker) {
					allowed = true
				}
			}
			if !allowed {
				return -1, errors.New("the conversation and agent history do not match; reopen the thread before rewinding")
			}
		}
	}
	return len(visible) - 1, nil
}

// RewindClaude copies only the log prefix BEFORE the selected user message to
// a new session. The original log is never modified; future messages and even
// future compaction summaries cannot leak into the continuation.
func RewindClaude(home, sessionID string, visible []Item) (id, path string, err error) {
	src, err := FindClaude(home, sessionID)
	if err != nil {
		return "", "", err
	}
	f, err := os.Open(src)
	if err != nil {
		return "", "", err
	}
	defer f.Close()
	var native []Item
	var offsets []int64
	log := &ClaudeLog{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 64<<20)
	var offset int64
	for sc.Scan() {
		for _, op := range log.Feed(sc.Bytes()) {
			if op.Item != nil && op.Item.Kind == "user" {
				native = append(native, *op.Item)
				offsets = append(offsets, offset)
			}
		}
		offset += int64(len(sc.Bytes()) + 1)
	}
	if err := sc.Err(); err != nil {
		return "", "", err
	}
	index, err := checkpointIndex(native, visible)
	if err != nil {
		return "", "", err
	}
	offsets = append(offsets, offset)
	if index == 0 {
		return "", "", nil
	} // no earlier context: start a fresh session
	return copyClaudeSession(f, src, offsets[index])
}

// ForkClaude freezes the log now, rather than deferring --fork-session until
// the first prompt (which would include later changes to the original).
func ForkClaude(home, sessionID string) (string, string, error) {
	src, err := FindClaude(home, sessionID)
	if err != nil {
		return "", "", err
	}
	f, err := os.Open(src)
	if err != nil {
		return "", "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", "", err
	}
	return copyClaudeSession(f, src, info.Size())
}

func copyClaudeSession(f *os.File, src string, limit int64) (id, path string, err error) {
	id = uuid.NewString()
	path = filepath.Join(filepath.Dir(src), id+".jsonl")
	out, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", "", err
	}
	defer func() {
		out.Close()
		if err != nil {
			os.Remove(path)
		}
	}()
	if _, err = f.Seek(0, 0); err != nil {
		return "", path, err
	}
	sc := bufio.NewScanner(io.LimitReader(f, limit))
	sc.Buffer(make([]byte, 64<<10), 64<<20)
	for sc.Scan() {
		line := sc.Bytes()
		var row map[string]json.RawMessage
		if err = json.Unmarshal(line, &row); err != nil {
			return "", path, err
		}
		// Keep message UUIDs and parent links intact for resume and embedded images.
		row["sessionId"], _ = json.Marshal(id)
		var data []byte
		if data, err = json.Marshal(row); err != nil {
			return "", path, err
		}
		if _, err = out.Write(append(data, '\n')); err != nil {
			return "", path, err
		}
	}
	if err = sc.Err(); err != nil {
		return "", path, err
	}
	if err = out.Close(); err != nil {
		return "", path, err
	}
	return id, path, nil
}

// ForkCodex creates the provider session immediately and starts no inference.
// Goal continuation must be deferred so only the new chat's own prompt runs.
func ForkCodex(ctx context.Context, o Options) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	c, err := startRPC(o.Bin, o.Cwd, o.Env, o.Remote)
	if err != nil {
		return "", err
	}
	defer c.close()
	if err := c.initializeWithCapabilities(ctx, Version, map[string]any{"experimentalApi": true}); err != nil {
		return "", err
	}
	approval, sandbox := codexPolicy(o.Mode)
	params := map[string]any{"threadId": o.ResumeID, "cwd": o.Cwd, "approvalPolicy": approval,
		"sandbox": sandbox, "excludeTurns": true, "deferGoalContinuation": true}
	if o.Model != "" && o.Model != "default" {
		params["model"] = o.Model
	}
	if o.Speed != "" {
		params["serviceTier"] = o.Speed
	}
	if o.Effort != "" {
		params["config"] = map[string]any{"model_reasoning_effort": o.Effort}
	}
	raw, err := c.call(ctx, "thread/fork", params)
	if err != nil {
		return "", err
	}
	var response struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return "", err
	}
	if response.Thread.ID == "" || response.Thread.ID == o.ResumeID {
		return "", errors.New("the agent did not create a separate session")
	}
	return response.Thread.ID, nil
}

// RewindCodex uses the CLI's native turn boundary fork, without sending a turn.
func RewindCodex(ctx context.Context, o Options, visible []Item, afterTurnID ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	c, err := startRPC(o.Bin, o.Cwd, o.Env, o.Remote)
	if err != nil {
		return "", err
	}
	defer c.close()
	if err := c.initializeWithCapabilities(ctx, Version, map[string]any{"experimentalApi": true}); err != nil {
		return "", err
	}
	history, err := codexRewindHistory(ctx, c, o.ResumeID)
	if err != nil {
		return "", err
	}
	ti := -1
	if len(afterTurnID) > 0 && afterTurnID[0] != "" {
		// Pocket's completed reply already records the provider's real turn ID.
		// Use that boundary directly: failed sends, compaction and imports can
		// make Pocket's old user-message count differ from Codex's history.
		for i, turn := range history {
			if turn.ID == afterTurnID[0] {
				ti = i + 1
				break
			}
		}
		if ti < 0 {
			return "", errors.New("the selected reply's Codex checkpoint is unavailable")
		}
	} else {
		var native []Item
		var turns []int
		for ti, turn := range history {
			for _, raw := range turn.Items {
				if it, ok := CodexItem(raw); ok && it.Kind == "user" {
					native = append(native, it)
					turns = append(turns, ti)
				}
			}
		}
		index, err := checkpointIndex(native, visible)
		if err != nil {
			return "", err
		}
		turns = append(turns, len(history))
		ti = turns[index]
		if index > 0 && turns[index-1] == ti {
			return "", errors.New("this message was sent during a turn; rewind to the first message of that turn")
		}
	}
	if ti == 0 {
		return "", nil
	}
	last := history[ti-1]
	if last.Status == "inProgress" || last.ID == "" {
		return "", errors.New("wait for the previous turn to finish")
	}
	approval, sandbox := codexPolicy(o.Mode)
	params := map[string]any{"threadId": o.ResumeID, "lastTurnId": last.ID, "cwd": o.Cwd,
		"approvalPolicy": approval, "sandbox": sandbox, "excludeTurns": true,
		// A rewind must not automatically continue an inherited active goal.
		"deferGoalContinuation": true}
	if o.Model != "" && o.Model != "default" {
		params["model"] = o.Model
	}
	if o.Speed != "" {
		params["serviceTier"] = o.Speed
	}
	if o.Effort != "" {
		params["config"] = map[string]any{"model_reasoning_effort": o.Effort}
	}
	raw, err := c.call(ctx, "thread/fork", params)
	if err != nil {
		return "", err
	}
	var response struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return "", err
	}
	if response.Thread.ID == "" || response.Thread.ID == o.ResumeID {
		return "", errors.New("the agent did not create a separate session")
	}
	// Re-read the saved copy, including paginated history. Older CLIs may
	// ignore lastTurnId; never open a continuation containing future context.
	retained, err := codexRewindHistory(ctx, c, response.Thread.ID)
	if err != nil {
		return "", fmt.Errorf("could not verify the rewound history: %w", err)
	}
	if len(retained) != ti {
		return "", fmt.Errorf("this Codex version does not support rewinding to that turn")
	}
	for i := range retained {
		if retained[i].ID == "" || retained[i].ID != history[i].ID {
			return "", errors.New("the rewound history does not match the selected checkpoint")
		}
	}
	return response.Thread.ID, nil
}

// Codex now stores new sessions with paginated history. A metadata-only read
// followed by full, ascending turn pages avoids treating an empty/deprecated
// thread.turns field as an empty conversation.
func codexRewindHistory(ctx context.Context, c *rpc, id string) ([]codexTurn, error) {
	raw, err := c.call(ctx, "thread/read", map[string]any{"threadId": id, "includeTurns": false})
	if err != nil {
		return nil, err
	}
	var response struct {
		Thread struct {
			HistoryMode string      `json:"historyMode"`
			Turns       []codexTurn `json:"turns"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, err
	}
	if response.Thread.HistoryMode == "" || response.Thread.HistoryMode == "legacy" {
		raw, err = c.call(ctx, "thread/read", map[string]any{"threadId": id, "includeTurns": true})
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &response); err != nil {
			return nil, err
		}
		return response.Thread.Turns, nil
	}
	if response.Thread.HistoryMode != "paginated" {
		return nil, errors.New("this Codex history format is not supported")
	}
	var turns []codexTurn
	cursor := ""
	seen := map[string]bool{}
	for {
		params := map[string]any{"threadId": id, "limit": 100, "sortDirection": "asc", "itemsView": "full"}
		if cursor != "" {
			params["cursor"] = cursor
		}
		raw, err = c.call(ctx, "thread/turns/list", params)
		if err != nil {
			return nil, err
		}
		var page struct {
			Data       []codexTurn `json:"data"`
			NextCursor *string     `json:"nextCursor"`
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			return nil, err
		}
		turns = append(turns, page.Data...)
		if page.NextCursor == nil || *page.NextCursor == "" {
			return turns, nil
		}
		cursor = *page.NextCursor
		if seen[cursor] {
			return nil, errors.New("Codex returned a repeated history cursor")
		}
		seen[cursor] = true
	}
}
