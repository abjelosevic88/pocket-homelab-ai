package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
)

// fitnessCore fakes Pocket Core (the person's links, link tokens) and Pocket
// Fitness behind it; status is what Fitness answers, url its own "url".
func fitnessCore(t *testing.T, s *Server, linkOn *bool, status *int, url *string) *[]string {
	t.Helper()
	auths := &[]string{}
	fitness := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/summary" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		*auths = append(*auths, r.Header.Get("Authorization"))
		if *status != http.StatusOK {
			w.WriteHeader(*status)
			json.NewEncoder(w).Encode(map[string]string{"error": "not today"})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"date": "2026-11-05", "week_done": 3, "week_goal": 5, "week_start": "2026-11-02", "streak_weeks": 25,
			"today":        map[string]any{"routine": "Park circuit", "routine_id": 3, "time": "07:30", "exercises": 5, "minutes": 45, "done": false},
			"last_workout": map[string]any{"id": 96, "name": "Pull day", "date": "2026-11-04", "duration_s": 4320, "set_count": 15, "volume": 5072.5, "records": 2},
			"records":      []any{map[string]any{"exercise": "Deadlift (Barbell)", "kinds": []string{"weight"}, "weight": 115, "reps": 5, "text": "Deadlift (Barbell): 115 kg × 5, heaviest yet", "date": "2026-11-04", "workout_id": 96}},
			"text":         "3 of 5 workouts this week.", "url": *url})
	}))
	t.Cleanup(fitness.Close)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/links/status":
			json.NewEncoder(w).Encode([]core.LinkState{{App: "fitness", Scope: fitnessScope, On: *linkOn, State: map[bool]string{true: "on", false: "off_person"}[*linkOn]}})
		case "/api/v1/links/token":
			json.NewEncoder(w).Encode(core.LinkToken{AccessToken: "pcx_fitness", URL: fitness.URL, PublicURL: "https://fitness.example.test", App: "fitness"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(upstream.Close)
	s.core = core.New(upstream.URL, core.Options{DB: s.store.DB, Token: "app"})
	return auths
}

func TestBriefingFitness(t *testing.T) {
	s, h, _ := testServer(t)
	_, alice := person(t, s, h, "fitness@example.test", "member", true)
	ctx := context.Background()
	brief := func() (map[string]any, []appSection) {
		t.Helper()
		b := s.briefing(ctx, alice.ID, "", map[string]askTool{}).(map[string]any)
		return b, b["sections"].([]appSection)
	}
	notConnected := func(b map[string]any) bool {
		for _, n := range b["unavailable"].([]string) {
			if n == fitnessTool {
				return true
			}
		}
		return false
	}
	find := func(secs []appSection) *appSection {
		for i := range secs {
			if secs[i].Tool == fitnessTool {
				return &secs[i]
			}
		}
		return nil
	}

	// no Core: no card, listed as not connected
	if b, secs := brief(); find(secs) != nil || !notConnected(b) {
		t.Fatalf("without core: %+v", b)
	}

	// the link off: still no card, and Fitness is never asked
	on, status, url := false, http.StatusOK, "https://fitness.bjelke.test/"
	auths := fitnessCore(t, s, &on, &status, &url)
	if b, secs := brief(); find(secs) != nil || !notConnected(b) || len(*auths) != 0 {
		t.Fatalf("link off: %+v %v", b, *auths)
	}

	// the link on: the card, read from Fitness as the person, with Fitness's own address
	on = true
	b, secs := brief()
	sec := find(secs)
	if sec == nil || notConnected(b) || sec.Error != "" {
		t.Fatalf("link on: %+v", b)
	}
	if len(*auths) != 1 || (*auths)[0] != "Bearer pcx_fitness" {
		t.Fatalf("fitness asked with %v", *auths)
	}
	data := sec.Data.(map[string]any)
	res := data["result"].(map[string]any)
	if data["source_url"] != "https://fitness.bjelke.test/" || res["week_done"] != float64(3) || res["today"].(map[string]any)["routine"] != "Park circuit" {
		t.Fatalf("card data: %+v", data)
	}

	// Fitness without its own address: the public one from Core
	url = ""
	_, secs = brief()
	if got := find(secs).Data.(map[string]any)["source_url"]; got != "https://fitness.example.test/" {
		t.Fatalf("source_url from core: %v", got)
	}

	// Fitness answers with an error: the card says so in plain words
	status = http.StatusInternalServerError
	_, secs = brief()
	if sec := find(secs); sec == nil || sec.Error == "" || sec.Data != nil {
		t.Fatalf("fitness error: %+v", sec)
	}
}
