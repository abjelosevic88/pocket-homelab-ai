package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	sc = bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 64<<20)
	offset = 0
	for offset < offsets[index] && sc.Scan() {
		line := sc.Bytes()
		offset += int64(len(line) + 1)
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

// RewindCodex uses the CLI's native turn boundary fork, without sending a turn.
func RewindCodex(ctx context.Context, bin, cwd string, env []string, sessionID string, visible []Item) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	c, err := startRPC(bin, cwd, env)
	if err != nil {
		return "", err
	}
	defer c.close()
	if err := c.initialize(ctx, Version); err != nil {
		return "", err
	}
	raw, err := c.call(ctx, "thread/read", map[string]any{"threadId": sessionID, "includeTurns": true})
	if err != nil {
		return "", err
	}
	var response struct {
		Thread struct {
			ID    string      `json:"id"`
			Turns []codexTurn `json:"turns"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return "", err
	}
	var native []Item
	var turns []int
	for ti, turn := range response.Thread.Turns {
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
	turns = append(turns, len(response.Thread.Turns))
	ti := turns[index]
	if index > 0 && turns[index-1] == ti {
		return "", errors.New("this message was sent during a turn; rewind to the first message of that turn")
	}
	if ti == 0 {
		return "", nil
	}
	last := response.Thread.Turns[ti-1]
	if last.Status == "inProgress" || last.ID == "" {
		return "", errors.New("wait for the previous turn to finish")
	}
	raw, err = c.call(ctx, "thread/fork", map[string]any{"threadId": sessionID, "lastTurnId": last.ID, "cwd": cwd})
	if err != nil {
		return "", err
	}
	response.Thread.ID = ""
	response.Thread.Turns = nil
	if err := json.Unmarshal(raw, &response); err != nil {
		return "", err
	}
	if response.Thread.ID == "" || response.Thread.ID == sessionID {
		return "", errors.New("the agent did not create a separate session")
	}
	// Older CLIs may ignore lastTurnId. Fail closed rather than resume future context.
	if len(response.Thread.Turns) != ti || response.Thread.Turns[ti-1].ID != last.ID {
		return "", fmt.Errorf("this Codex version does not support rewinding to that turn")
	}
	return response.Thread.ID, nil
}
