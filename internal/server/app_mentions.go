package server

import (
	"context"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// "@money" in a message points the agent at one of the person's Pocket apps.
// The composer offers the apps from their Core tool catalog; on send the
// mentioned ones are spelled out for the agent with their operations.

type mentionApp struct {
	Slug  string   `json:"slug"`
	Name  string   `json:"name"`
	Tools []string `json:"tools"`
	Reads int      `json:"reads"`
}

type mentionKey struct {
	s   *Server
	uid int64
}

var mentionCache = struct {
	sync.Mutex
	m map[mentionKey]mentionEntry
}{m: map[mentionKey]mentionEntry{}}

type mentionEntry struct {
	at   time.Time
	apps []mentionApp
}

// mentionApps are the apps in the person's catalog, by name (cached 2 min).
func (s *Server) mentionApps(ctx context.Context, uid int64) ([]mentionApp, error) {
	k := mentionKey{s, uid}
	mentionCache.Lock()
	hit, ok := mentionCache.m[k]
	mentionCache.Unlock()
	if ok && time.Since(hit.at) < 2*time.Minute {
		return hit.apps, nil
	}
	catalog, err := s.catalog(ctx, uid)
	if err != nil {
		return nil, err
	}
	bySlug := map[string]*mentionApp{}
	for _, t := range catalog {
		slug := t.Meta.App
		if slug == "" || !appSlugRe.MatchString(slug) {
			continue
		}
		a := bySlug[slug]
		if a == nil {
			a = &mentionApp{Slug: slug, Name: "Pocket " + appTitle(slug)}
			bySlug[slug] = a
		}
		a.Tools = append(a.Tools, t.Name)
		if t.Annotations.ReadOnly || t.Meta.Kind == "read" {
			a.Reads++
		}
	}
	apps := make([]mentionApp, 0, len(bySlug))
	for _, a := range bySlug {
		sort.Strings(a.Tools)
		apps = append(apps, *a)
	}
	sort.Slice(apps, func(i, j int) bool { return apps[i].Name < apps[j].Name })
	mentionCache.Lock()
	mentionCache.m[k] = mentionEntry{time.Now(), apps}
	mentionCache.Unlock()
	return apps, nil
}

// GET /pocket-tools/apps: the apps you can @-mention in a message.
func (s *Server) handleMentionApps(w http.ResponseWriter, r *http.Request) {
	apps, err := s.mentionApps(r.Context(), me(r))
	if err != nil {
		writeJSON(w, 200, map[string]any{"apps": []mentionApp{}, "error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]any{"apps": apps})
}

var appMentionRe = regexp.MustCompile(`(?:^|\s)@([A-Za-z][A-Za-z0-9-]{0,40})`)

const mentionToolsMax = 40

// appMentionNote tells the agent which apps the message @-mentions, with
// their operations; "" when it mentions none of the sender's apps.
func (s *Server) appMentionNote(ctx context.Context, uid int64, text string) string {
	found := appMentionRe.FindAllStringSubmatch(text, -1)
	if len(found) == 0 {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	apps, err := s.mentionApps(ctx, uid)
	if err != nil || len(apps) == 0 {
		return ""
	}
	seen := map[string]bool{}
	var lines []string
	for _, m := range found {
		slug := strings.ToLower(m[1])
		if seen[slug] {
			continue
		}
		seen[slug] = true
		for _, a := range apps {
			if a.Slug != slug {
				continue
			}
			tools := a.Tools
			more := ""
			if len(tools) > mentionToolsMax {
				more = ", …"
				tools = tools[:mentionToolsMax]
			}
			lines = append(lines, "- "+a.Name+" (@"+a.Slug+"): "+strings.Join(tools, ", ")+more)
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return "\n\n[Pocket AI: with @ the person picked these Pocket apps for this message. Work in them through your pocket tools; an operation that is not loaded yet: get its schema with pocket_find_tools, then run it with pocket_call_tool.\n" +
		strings.Join(lines, "\n") + "]\n"
}
