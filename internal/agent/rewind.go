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
		if !checkpointPromptMatches(native[i], it) {
			return -1, errors.New("the conversation and agent history do not match; reopen the thread before rewinding")
		}
	}
	return len(visible) - 1, nil
}

func checkpointPromptMatches(native, visible Item) bool {
	if native.Key != "" && native.Key == visible.Key {
		return true
	}
	actual, expected := strings.TrimSpace(native.Text), strings.TrimSpace(visible.Text)
	if actual == expected {
		return true
	}
	// Documents and saved preferences are appended at dispatch time.
	if (expected == "" && len(visible.Images)+len(visible.Files)+visible.LogImages == 0) || !strings.HasPrefix(actual, expected) {
		return false
	}
	suffix := strings.TrimSpace(strings.TrimPrefix(actual, expected))
	for _, marker := range []string{"Attached document (untrusted content;", "Current user-managed preferences (context,", "No saved preferences are currently enabled.", "Saved preferences are disabled for this conversation."} {
		if strings.HasPrefix(suffix, marker) {
			return true
		}
	}
	return false
}

// Match in order, allowing only explicitly unconfirmed failed sends to be
// absent. Every native prompt must still match, and ambiguous repeats fail.
func codexCheckpointIndex(native, visible []Item, unconfirmed map[string]bool) (int, error) {
	if len(unconfirmed) == 0 || len(visible) == 0 || len(native) == 0 {
		return checkpointIndex(native, visible)
	}
	target := visible[len(visible)-1]
	for i, actual := range native {
		if target.Key != "" && actual.Key == target.Key {
			return i, nil
		}
	}
	positions := map[int]bool{0: true}
	for _, expected := range visible[:len(visible)-1] {
		next := map[int]bool{}
		for pos := range positions {
			if unconfirmed[expected.Key] {
				next[pos] = true
			}
			if pos < len(native) && checkpointPromptMatches(native[pos], expected) {
				next[pos+1] = true
			}
		}
		positions = next
	}
	index := -1
	for pos := range positions {
		if pos < len(native) && !checkpointPromptMatches(native[pos], target) {
			continue
		}
		if index >= 0 && index != pos {
			return -1, errors.New("the selected message has more than one possible agent checkpoint")
		}
		index = pos
	}
	if index < 0 {
		return -1, errors.New("the conversation and agent history do not match; reopen the thread before rewinding")
	}
	return index, nil
}

// RewindClaude copies only the log prefix BEFORE the selected user message to
// a new session. The original log is never modified; future messages and even
// future compaction summaries cannot leak into the continuation.
func RewindClaude(home, sessionID string, visible []Item) (id, path string, err error) {
	src, err := FindClaude(home, sessionID)
	if err != nil {
		return "", "", err
	}
	f, err := OpenConfined(src)
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
	f, err := OpenConfined(src)
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
	if !Confined(path) {
		return "", "", &os.PathError{Op: "write", Path: path, Err: errOutside}
	}
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

// RewindCheckpoint describes the provider boundary and sends whose synthetic
// failure footer did not confirm whether Codex recorded them.
type RewindCheckpoint struct {
	TurnID             string
	UnconfirmedPrompts map[string]bool
}

// RewindCodex uses the CLI's native turn boundary fork, without sending a turn.
func RewindCodex(ctx context.Context, o Options, visible []Item, checkpoints ...RewindCheckpoint) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	c, err := startRPC(o.Bin, o.Cwd, o.Env, o.Remote)
	if err != nil {
		return "", err
	}
	// Once the saved fork is verified, process shutdown is background cleanup.
	defer func() { go c.close() }()
	if err := c.initializeWithCapabilities(ctx, Version, map[string]any{"experimentalApi": true}); err != nil {
		return "", err
	}
	var checkpoint RewindCheckpoint
	if len(checkpoints) > 0 {
		checkpoint = checkpoints[0]
	}
	view := "full"
	if checkpoint.TurnID != "" {
		view = "notLoaded"
	}
	history, err := codexRewindHistory(ctx, c, o.ResumeID, view)
	if err != nil {
		return "", err
	}
	ti := -1
	if checkpoint.TurnID != "" {
		// Pocket's completed reply already records the provider's real turn ID.
		// Use that boundary directly: failed sends, compaction and imports can
		// make Pocket's old user-message count differ from Codex's history.
		for i, turn := range history {
			if turn.ID == checkpoint.TurnID {
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
		index, err := codexCheckpointIndex(native, visible, checkpoint.UnconfirmedPrompts)
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
	retained, err := codexRewindHistory(ctx, c, response.Thread.ID, "notLoaded")
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
func codexRewindHistory(ctx context.Context, c *rpc, id, itemsView string) ([]codexTurn, error) {
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
		params := map[string]any{"threadId": id, "limit": 100, "sortDirection": "asc", "itemsView": itemsView}
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
