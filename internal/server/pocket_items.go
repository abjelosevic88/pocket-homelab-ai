package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
)

// Mentions and attachments from other Pocket apps, for the phone composer:
// one search across the apps people reference most, flattened into a single
// list, and the text of a picked item so it can go in as a file.

// pocketItem is one search result, the same shape whichever app it came from.
type pocketItem struct {
	App        string `json:"app"`
	ID         string `json:"id"`
	Title      string `json:"title"`
	Subtitle   string `json:"subtitle,omitempty"`
	URL        string `json:"url,omitempty"`
	Attachable bool   `json:"attachable"`
}

// mentionApps are searched in this order, and the results keep it.
var mentionApps = []string{"notes", "tasks", "projects", "documents", "files", "contacts"}

const pocketItemsPerApp = 8

// pocketItemsOneApp: results when only one app is searched.
const pocketItemsOneApp = 15

// pocketTextMax keeps an attached item within what an agent can read in one go.
const pocketTextMax = 512 << 10

var textFileExt = map[string]bool{
	".md": true, ".markdown": true, ".txt": true, ".json": true, ".yaml": true, ".yml": true, ".toml": true,
	".csv": true, ".log": true, ".xml": true, ".html": true, ".css": true, ".sql": true, ".sh": true,
	".go": true, ".py": true, ".swift": true, ".ts": true, ".tsx": true, ".js": true, ".rs": true, ".java": true, ".kt": true,
}

// GET /pocket/items?q=: one search across mentionApps. With app= ("@documents
// #tax" in the composer) only that app is searched, and an empty q lists what
// its search returns without one.
func (s *Server) handlePocketItems(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	app := r.URL.Query().Get("app")
	if app != "" && !appSlugRe.MatchString(app) {
		writeErr(w, 400, "Unknown app")
		return
	}
	if (q == "" && app == "") || len(q) > 200 {
		writeErr(w, 400, "Write a search query up to 200 characters")
		return
	}
	cat, err := s.catalog(r.Context(), me(r))
	if err != nil {
		writeErr(w, 409, err.Error())
		return
	}
	apps, per := mentionApps, pocketItemsPerApp
	if app != "" {
		apps, per = []string{app}, pocketItemsOneApp
	}
	res, _ := s.appSearch(r.Context(), me(r), "", cat, searchInput{Query: q, Apps: apps}).(map[string]any)
	sections, _ := res["sections"].([]appSection)
	writeJSON(w, 200, map[string]any{"items": pocketItems(sections, apps, per)})
}

func pocketItems(sections []appSection, order []string, per int) []pocketItem {
	byApp := map[string][]pocketItem{}
	for _, sec := range sections {
		parts := strings.SplitN(sec.Tool, "_", 3)
		if sec.Error != "" || len(parts) < 3 {
			continue
		}
		app := parts[1]
		for _, row := range resultRows(sec.Data) {
			if item, ok := toPocketItem(app, row); ok && len(byApp[app]) < per && !hasItem(byApp[app], item.ID) {
				byApp[app] = append(byApp[app], item)
			}
		}
	}
	out := []pocketItem{}
	for _, app := range order {
		out = append(out, byApp[app]...)
	}
	return out
}

// hasItem: an app with two search operations can find the same item twice.
func hasItem(items []pocketItem, id string) bool {
	for _, it := range items {
		if it.ID == id {
			return true
		}
	}
	return false
}

// resultRows finds the list in a tool result: a bare array, or one under a
// collection key ({"results": [...]}, {"tasks": [...]}, {"entries": [...]}, …).
func resultRows(data any) []map[string]any {
	env, _ := data.(map[string]any)
	var lists [][]any
	switch v := env["result"].(type) {
	case []any:
		lists = append(lists, v)
	case map[string]any:
		for _, key := range []string{"results", "items", "tasks", "entries", "issues", "projects", "contacts", "notes", "documents"} {
			if a, ok := v[key].([]any); ok {
				lists = append(lists, a)
			}
		}
	}
	rows := []map[string]any{}
	for _, list := range lists {
		for _, x := range list {
			if row, ok := x.(map[string]any); ok {
				rows = append(rows, row)
			}
		}
	}
	return rows
}

func toPocketItem(app string, row map[string]any) (pocketItem, bool) {
	item := pocketItem{App: app, Title: firstString(row, "title", "name", "display_name", "full_name", "subject"), URL: firstString(row, "item_url")}
	if key := firstString(row, "identifier", "key"); key != "" && !strings.HasPrefix(item.Title, key) {
		item.Title = strings.TrimSpace(key + " " + item.Title)
	}
	switch id := row["id"].(type) {
	case float64:
		item.ID = strconv.FormatFloat(id, 'f', -1, 64)
	case string:
		item.ID = id
	}
	switch app {
	case "notes":
		item.Subtitle = firstString(row, "category", "excerpt")
		item.Attachable = item.ID != ""
	case "tasks":
		if list, ok := row["list"].(map[string]any); ok {
			item.Subtitle = firstString(list, "name")
		}
		if due := firstString(row, "due"); due != "" {
			item.Subtitle = strings.TrimPrefix(item.Subtitle+" · due "+due, " · ")
		}
	case "documents":
		item.Subtitle = strings.Trim(firstString(row, "correspondent_name")+" · "+firstString(row, "created"), " ·")
		item.Attachable = item.ID != ""
	case "files":
		if dir, _ := row["is_dir"].(bool); dir {
			return item, false
		}
		item.ID = firstString(row, "path")
		item.Subtitle = item.ID
		size, _ := row["size"].(float64)
		mime := firstString(row, "mime")
		item.Attachable = size <= pocketTextMax && (strings.HasPrefix(mime, "text/") || textFileExt[strings.ToLower(filepath.Ext(item.ID))])
	case "contacts":
		item.Subtitle = firstString(row, "email", "company", "organization", "phone")
	default:
		item.Subtitle = firstString(row, "status", "excerpt")
	}
	return item, item.ID != "" && item.Title != ""
}

func firstString(row map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := row[k].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// handlePocketText returns the readable text of one note, document or text file.
func (s *Server) handlePocketText(w http.ResponseWriter, r *http.Request) {
	text, status, err := s.pocketText(r.Context(), me(r), r.URL.Query().Get("app"), r.URL.Query().Get("id"), pocketTextMax)
	if err != nil {
		writeErr(w, status, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"text": text})
}

// pocketText reads one note, document or text file from the person's Pocket
// apps (status: the HTTP status for a failure), cut at max bytes.
func (s *Server) pocketText(ctx context.Context, uid int64, app, id string, max int) (string, int, error) {
	id = strings.TrimSpace(id)
	var tool string
	var args map[string]any
	switch app {
	case "notes", "documents":
		n, err := strconv.ParseInt(id, 10, 64)
		if err != nil || n < 1 {
			return "", 400, errors.New("Choose a note or document")
		}
		tool, args = map[string]string{"notes": "pocket_notes_get", "documents": "pocket_documents_text"}[app], map[string]any{"id": n}
	case "files":
		if id == "" {
			return "", 400, errors.New("Choose a file")
		}
		tool, args = "pocket_files_read_text", map[string]any{"query": map[string]any{"path": id}}
	default:
		return "", 400, errors.New("Only notes, documents and text files can be attached")
	}
	cat, err := s.catalog(ctx, uid)
	if err != nil {
		return "", 409, err
	}
	if t, ok := cat[tool]; !ok || !t.Annotations.ReadOnly {
		return "", 409, fmt.Errorf("Pocket %s is not connected for your account", app)
	}
	raw := s.readApp(ctx, uid, "", tool, args)
	if toolFailed(raw) {
		return "", 502, errors.New("Could not read it from Pocket " + app + ". Open the app or reconnect Ask.")
	}
	env, _ := toolData(raw).(map[string]any)
	text := findText(env["result"])
	if strings.TrimSpace(text) == "" {
		return "", 422, errors.New("There is no readable text in it")
	}
	if len(text) > max {
		text = text[:max] + "\n\n[Shortened: the rest did not fit]"
	}
	return text, 200, nil
}

// findText takes the body out of a read result: the string itself, or its
// content/text/body field.
func findText(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case map[string]any:
		for _, k := range []string{"content", "text", "body", "markdown"} {
			if s, ok := x[k].(string); ok {
				return s
			}
		}
		for _, k := range []string{"document", "file", "note"} {
			if s := findText(x[k]); s != "" {
				return s
			}
		}
	}
	return ""
}
