package agent

import (
	"context"
	"encoding/json"
	"os"
	"time"
)

// CodexReader keeps one `codex app-server` open to re-read a thread while
// someone follows it.
type CodexReader struct {
	c *rpc
}

// OpenCodexReader starts the app-server.
func OpenCodexReader(ctx context.Context, bin string) (*CodexReader, error) {
	c, err := startRPC(bin, os.TempDir(), nil)
	if err != nil {
		return nil, err
	}
	ictx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := c.initialize(ictx, Version); err != nil {
		c.close()
		return nil, err
	}
	return &CodexReader{c: c}, nil
}

// Close stops the app-server.
func (r *CodexReader) Close() { go r.c.close() }

// Read returns the thread's items in order and the path of its log file.
func (r *CodexReader) Read(ctx context.Context, threadID string) ([]Item, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	res, err := r.c.call(ctx, "thread/read", map[string]any{"threadId": threadID, "includeTurns": true})
	if err != nil {
		return nil, "", err
	}
	var t struct {
		Thread struct {
			Path  string `json:"path"`
			Turns []struct {
				Items []json.RawMessage `json:"items"`
			} `json:"turns"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(res, &t); err != nil {
		return nil, "", err
	}
	var items []Item
	for _, turn := range t.Thread.Turns {
		for _, raw := range turn.Items {
			if it, ok := CodexItem(raw); ok {
				items = append(items, it)
			}
		}
	}
	return items, t.Thread.Path, nil
}
