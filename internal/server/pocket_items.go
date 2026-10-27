package server

import (
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

// pocketTextMax keeps an attached item within what an agent can read in one go.
const pocketTextMax = 512 << 10

var textFileExt = map[string]bool{
	".md": true, ".markdown": true, ".txt": true, ".json": true, ".yaml": true, ".yml": true, ".toml": true,
	".csv": true, ".log": true, ".xml": true, ".html": true, ".css": true, ".sql": true, ".sh": true,
	".go": true, ".py": true, ".swift": true, ".ts": true, ".tsx": true, ".js": true, ".rs": true, ".java": true, ".kt": true,
}

func (s *Server) handlePocketItems(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" || len(q) > 200 {
		writeErr(w, 400, "Write a search query up to 200 characters")
		return
	}
	cat, err := s.catalog(r.Context(), me(r))
	if err != nil {
		writeErr(w, 409, err.Error())
		return
	}
	res, _ := s.appSearch(r.Context(), me(r), "", cat, searchInput{Query: q, Apps: mentionApps}).(map[string]any)
	sections, _ := res["sections"].([]appSection)
	writeJSON(w, 200, map[string]any{"items": pocketItems(sections)})
}

func pocketItems(sections []appSection) []pocketItem {
	byApp := map[string][]pocketItem{}
	for _, sec := range sections {
		parts := strings.SplitN(sec.Tool, "_", 3)
		if sec.Error != "" || len(parts) < 3 {
			continue
		}
		app := parts[1]
		for _, row := range resultRows(sec.Data) {
			if item, ok := toPocketItem(app, row); ok && len(byApp[app]) < pocketItemsPerApp {
				byApp[app] = append(byApp[app], item)
			}
		}
	}
	out := []pocketItem{}
	for _, app := range mentionApps {
		out = append(out, byApp[app]...)
	}
	return out
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
	app, id := r.URL.Query().Get("app"), strings.TrimSpace(r.URL.Query().Get("id"))
	var tool string
	var args map[string]any
	switch app {
	case "notes", "documents":
		n, err := strconv.ParseInt(id, 10, 64)
		if err != nil || n < 1 {
			writeErr(w, 400, "Choose a note or document")
			return
		}
		tool, args = map[string]string{"notes": "pocket_notes_get", "documents": "pocket_documents_text"}[app], map[string]any{"id": n}
	case "files":
		if id == "" {
			writeErr(w, 400, "Choose a file")
			return
		}
		tool, args = "pocket_files_read_text", map[string]any{"query": map[string]any{"path": id}}
	default:
		writeErr(w, 400, "Only notes, documents and text files can be attached")
		return
	}
	cat, err := s.catalog(r.Context(), me(r))
	if err != nil {
		writeErr(w, 409, err.Error())
		return
	}
	if t, ok := cat[tool]; !ok || !t.Annotations.ReadOnly {
		writeErr(w, 409, fmt.Sprintf("Pocket %s is not connected for your account", app))
		return
	}
	raw := s.readApp(r.Context(), me(r), "", tool, args)
	if toolFailed(raw) {
		writeErr(w, 502, "Could not read it from Pocket "+app+". Open the app or reconnect Ask.")
		return
	}
	env, _ := toolData(raw).(map[string]any)
	text := findText(env["result"])
	if strings.TrimSpace(text) == "" {
		writeErr(w, 422, "There is no readable text in it")
		return
	}
	if len(text) > pocketTextMax {
		text = text[:pocketTextMax] + "\n\n[Shortened: the rest did not fit in an attachment]"
	}
	writeJSON(w, 200, map[string]any{"text": text})
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
