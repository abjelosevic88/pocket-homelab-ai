package server

import (
	"net/http"
	"strings"
	"time"
)

type latestClip struct {
	ID       any    `json:"id"`
	Kind     string `json:"kind"`
	Content  string `json:"content"`
	Title    string `json:"title,omitempty"`
	Device   string `json:"source_device,omitempty"`
	App      string `json:"source_app,omitempty"`
	CopiedAt string `json:"copied_at"`
}

// handleLatestClip returns the newest text, link or colour in Pocket Clips, so the phone can
// paste what was just copied on the Mac. Images and files are skipped: Core's read tools
// return their file names, not their bytes. Clips lists pinned items first, so the newest
// copy is picked here by its time.
func (s *Server) handleLatestClip(w http.ResponseWriter, r *http.Request) {
	token, err := s.askToken(r.Context(), me(r))
	if err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	raw, err := s.askReadRPC(r.Context(), token, "tools/call", map[string]any{
		"name": "pocket_clips_list", "arguments": map[string]any{"query": map[string]any{"limit": 100}},
	})
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	if toolFailed(raw) {
		writeErr(w, http.StatusBadGateway, "Pocket Clips is unavailable")
		return
	}
	v, _ := toolData(raw).(map[string]any)
	result, _ := v["result"].(map[string]any)
	rows, _ := result["clips"].([]any)
	var best *latestClip
	var bestAt time.Time
	for _, row := range rows {
		c, _ := row.(map[string]any)
		kind, _ := c["kind"].(string)
		content, _ := c["content"].(string)
		at, _ := c["last_copied_at"].(string)
		copied, _ := time.Parse(time.RFC3339Nano, at)
		if kind == "image" || kind == "file" || strings.TrimSpace(content) == "" {
			continue
		}
		if best == nil || copied.After(bestAt) {
			bestAt = copied
			title, _ := c["title"].(string)
			device, _ := c["source_device"].(string)
			app, _ := c["source_app"].(string)
			best = &latestClip{ID: c["id"], Kind: kind, Content: content, Title: title, Device: device, App: app, CopiedAt: at}
		}
	}
	if best == nil {
		writeErr(w, http.StatusNotFound, "Nothing copied in Pocket Clips yet")
		return
	}
	writeJSON(w, http.StatusOK, best)
}
