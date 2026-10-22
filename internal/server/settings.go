package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/notify"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
)

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	st, err := s.store.GetSettings(ctx, u.ID)
	if err != nil {
		s.fail(w, err, "settings")
		return
	}
	writeJSON(w, http.StatusOK, s.publicSettings(ctx, u, st))
}

// publicSettings is the settings object plus a few read-only facts.
func (s *Server) publicSettings(ctx context.Context, u *store.User, st store.Settings) map[string]any {
	out := map[string]any{}
	b, _ := json.Marshal(st)
	_ = json.Unmarshal(b, &out)
	out["notify_has_token"] = st.NotifyToken != ""
	out["notify_box_route"] = s.cfg.NotifyURL != "" && s.isOwner(ctx, u.ID)
	out["box_tz"] = s.cfg.Timezone
	out["home"] = s.cfg.Home
	out["core"] = s.coreStatus()
	return out
}

func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	st, err := s.store.GetSettings(ctx, u.ID)
	if err != nil {
		s.fail(w, err, "settings")
		return
	}
	var in struct {
		Title           *string              `json:"title"`
		AppModel        *string              `json:"app_model"`
		DefaultProvider *string              `json:"default_provider"`
		DefaultCwd      *string              `json:"default_cwd"`
		Claude          *store.AgentDefaults `json:"claude"`
		Codex           *store.AgentDefaults `json:"codex"`
		SystemPrompt    *string              `json:"system_prompt"`
		NotifyKind      *string              `json:"notify_kind"`
		NotifyURL       *string              `json:"notify_url"`
		NotifyToken     *string              `json:"notify_token"`
		NotifyFinished  *bool                `json:"notify_finished"`
		NotifyInput     *bool                `json:"notify_input"`
		NotifyFailed    *bool                `json:"notify_failed"`
		NotifyOnlyAway  *bool                `json:"notify_only_away"`
		EnterSends      *bool                `json:"enter_sends"`
		ShowThinking    *bool                `json:"show_thinking"`
		UsageWidget     *bool                `json:"usage_widget"`
		ThreadTabs      *bool                `json:"thread_tabs"`
		UsagePinned     *[]string            `json:"usage_pinned"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if in.Title != nil && u.Role == "admin" && strings.TrimSpace(*in.Title) != "" {
		box, _ := s.store.GetSettings(ctx, 0)
		box.Title = strings.TrimSpace(*in.Title)
		_ = s.store.SaveBoxSettings(ctx, box)
		st.Title = box.Title
	}
	if in.DefaultProvider != nil && (*in.DefaultProvider == "claude" || *in.DefaultProvider == "codex") {
		st.DefaultProvider = *in.DefaultProvider
	}
	if in.DefaultCwd != nil {
		p := strings.TrimSpace(*in.DefaultCwd)
		if p != "" {
			clean, err := s.checkDir(p)
			if err != nil {
				writeErr(w, http.StatusBadRequest, err.Error())
				return
			}
			p = clean
		}
		st.DefaultCwd = p
	}
	if in.Claude != nil {
		st.Claude = *in.Claude
	}
	if in.Codex != nil {
		st.Codex = *in.Codex
	}
	if in.AppModel != nil {
		model := *in.AppModel
		if !core.ValidInferenceModel(model) {
			writeErr(w, 400, "invalid app default model")
			return
		}
		if model != "" {
			models, err := s.personalInferenceModels(ctx, u, st)
			if err != nil {
				writeErr(w, 503, "Could not load models from your Claude account; try again shortly")
				return
			}
			found := false
			for _, m := range models {
				if m.ID == model {
					found = true
					break
				}
			}
			if !found {
				writeErr(w, 400, "Choose a model offered by your connected Claude account")
				return
			}
		}
		st.AppModel = model
	}
	if in.SystemPrompt != nil {
		st.SystemPrompt = strings.TrimSpace(*in.SystemPrompt)
	}
	if in.NotifyKind != nil {
		switch *in.NotifyKind {
		case "apprise", "ntfy", "webhook":
			st.NotifyKind = *in.NotifyKind
		}
	}
	if in.UsagePinned != nil {
		st.UsagePinned = []string{}
		for _, k := range *in.UsagePinned {
			if k = strings.TrimSpace(k); k != "" && !strings.Contains(k, ",") && len(k) < 100 && len(st.UsagePinned) < 30 {
				st.UsagePinned = append(st.UsagePinned, k)
			}
		}
	}
	if in.NotifyURL != nil {
		st.NotifyURL = strings.TrimSpace(*in.NotifyURL)
	}
	for _, b := range []struct {
		in  *bool
		out *bool
	}{{in.NotifyFinished, &st.NotifyFinished}, {in.NotifyInput, &st.NotifyInput}, {in.NotifyFailed, &st.NotifyFailed},
		{in.NotifyOnlyAway, &st.NotifyOnlyAway}, {in.EnterSends, &st.EnterSends}, {in.ShowThinking, &st.ShowThinking},
		{in.UsageWidget, &st.UsageWidget}, {in.ThreadTabs, &st.ThreadTabs}} {
		if b.in != nil {
			*b.out = *b.in
		}
	}
	if err := s.store.SaveSettings(ctx, u.ID, st); err != nil {
		s.fail(w, err, "save settings")
		return
	}
	if in.NotifyToken != nil {
		_ = s.store.SetUserSetting(ctx, u.ID, "notify_token", strings.TrimSpace(*in.NotifyToken))
	}
	s.touch()
	st, _ = s.store.GetSettings(ctx, u.ID)
	writeJSON(w, http.StatusOK, s.publicSettings(ctx, u, st))
}

// notifyRoute is where a person's notifications go: their own route, else
// (for the owner) the box route from the environment.
func (s *Server) notifyRoute(ctx context.Context, uid int64, st store.Settings) (kind, url, token string) {
	if st.NotifyURL != "" {
		return st.NotifyKind, st.NotifyURL, st.NotifyToken
	}
	if s.cfg.NotifyURL != "" && s.isOwner(ctx, uid) {
		return s.cfg.NotifyKind, s.cfg.NotifyURL, ""
	}
	return "", "", ""
}

// notifyUser sends one message to a person.
func (s *Server) notifyUser(ctx context.Context, uid int64, what, title, body, link string) {
	st, err := s.store.GetSettings(ctx, uid)
	if err != nil {
		return
	}
	kind, url, token := s.notifyRoute(ctx, uid, st)
	if url == "" {
		return
	}
	go func() {
		c, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := notify.Send(c, kind, url, token, title, body, link); err != nil {
			s.log.Warn("notification failed", "user", uid, "what", what, "err", err)
		}
	}()
}

func (s *Server) handleTestNotify(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	st, _ := s.store.GetSettings(ctx, u.ID)
	kind, url, token := s.notifyRoute(ctx, u.ID, st)
	if url == "" {
		writeErr(w, http.StatusBadRequest, "set a notification address first")
		return
	}
	if err := notify.Send(ctx, kind, url, token, "Pocket AI", "Notifications work. Finished turns and questions from your agents will look like this.", s.cfg.BaseURL); err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleSystemInfo(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	info := map[string]any{
		"version": Version, "go": runtime.Version(), "uptime": time.Since(s.started).Round(time.Second).String(),
		"counts": s.store.UserCounts(ctx, u.ID), "core": s.coreStatus(), "auth_mode": s.cfg.AuthMode,
		"base_url": s.cfg.BaseURL, "timezone": s.cfg.Timezone,
	}
	if u.MayRun() {
		info["roots"] = s.cfg.Roots
		info["claude_bin"], info["codex_bin"] = s.cfg.ClaudeBin, s.cfg.CodexBin
		info["live"] = s.rt.liveCount()
		info["providers"] = s.providers(ctx, false)
	}
	if u.Role == "admin" {
		if fi, err := os.Stat(s.cfg.DBPath); err == nil {
			info["db_size"] = fi.Size()
		}
		info["users"], _ = s.store.CountUsers(ctx)
	}
	writeJSON(w, http.StatusOK, info)
}

// handleBackup streams a consistent copy of the whole database; only an
// admin, and only while they are the only person on the box.
func (s *Server) handleBackup(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userFrom(ctx)
	if u.Role != "admin" {
		writeErr(w, http.StatusForbidden, "only an admin can download the database")
		return
	}
	if n, _ := s.store.CountUsers(ctx); n > 1 {
		writeErr(w, http.StatusForbidden, "the database holds other people's conversations; back it up on the server (pocket-ai backup)")
		return
	}
	tmp := fmt.Sprintf("%s/backups/download-%d.db", s.cfg.DataDir, time.Now().UnixNano())
	if err := s.store.Backup(ctx, tmp); err != nil {
		s.fail(w, err, "backup")
		return
	}
	defer os.Remove(tmp)
	w.Header().Set("Content-Type", "application/vnd.sqlite3")
	w.Header().Set("Content-Disposition", `attachment; filename="pocket-ai-`+time.Now().Format("20060102")+`.db"`)
	http.ServeFile(w, r, tmp)
}
