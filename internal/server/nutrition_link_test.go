package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
)

// nutritionCore fakes Pocket Core (the person's links, link tokens) and Pocket
// Nutrition behind it; status is what Nutrition answers, url its own "url".
func nutritionCore(t *testing.T, s *Server, linkOn *bool, status *int, url *string) *[]string {
	t.Helper()
	auths := &[]string{}
	nutrition := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		json.NewEncoder(w).Encode(map[string]any{"date": "2026-11-05", "entries": 4, "water_ml": 1250, "streak": 12,
			"totals":    map[string]any{"kcal": 1480.4, "protein": 96.5, "carbs": 150, "fat": 52, "fiber": 18, "sugar": 40, "sat_fat": 14, "sodium": 1900},
			"goals":     map[string]any{"kcal": 2100, "protein": 140, "carbs": 230, "fat": 70, "fiber": 30, "water": 2500, "burned": 300, "budget": 2400},
			"remaining": map[string]any{"kcal": 920, "protein": 44, "carbs": 80, "fat": 18, "water": 1250},
			"week": map[string]any{"start": "2026-11-02", "end": "2026-11-08", "days": []any{}, "logged": 4, "on_goal": 2,
				"avg_kcal": 2050, "avg_protein": 120.5, "avg_carbs": 210, "avg_fat": 68, "goal": 2100},
			"text": "1,480 kcal of 2,400; protein 96.5 g, carbs 150 g, fat 52 g, 1.25 l of water.", "url": *url})
	}))
	t.Cleanup(nutrition.Close)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/links/status":
			json.NewEncoder(w).Encode([]core.LinkState{{App: "nutrition", Scope: nutritionScope, On: *linkOn, State: map[bool]string{true: "on", false: "off_person"}[*linkOn]}})
		case "/api/v1/links/token":
			json.NewEncoder(w).Encode(core.LinkToken{AccessToken: "pcx_nutrition", URL: nutrition.URL, PublicURL: "https://nutrition.example.test", App: "nutrition"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(upstream.Close)
	s.core = core.New(upstream.URL, core.Options{DB: s.store.DB, Token: "app"})
	return auths
}

func TestBriefingNutrition(t *testing.T) {
	s, h, _ := testServer(t)
	_, alice := person(t, s, h, "nutrition@example.test", "member", true)
	ctx := context.Background()
	brief := func() (map[string]any, []appSection) {
		t.Helper()
		b := s.briefing(ctx, alice.ID, "", map[string]askTool{}).(map[string]any)
		return b, b["sections"].([]appSection)
	}
	notConnected := func(b map[string]any) bool {
		for _, n := range b["unavailable"].([]string) {
			if n == nutritionTool {
				return true
			}
		}
		return false
	}
	find := func(secs []appSection) *appSection {
		for i := range secs {
			if secs[i].Tool == nutritionTool {
				return &secs[i]
			}
		}
		return nil
	}

	// no Core: no card, listed as not connected
	if b, secs := brief(); find(secs) != nil || !notConnected(b) {
		t.Fatalf("without core: %+v", b)
	}

	// the link off: still no card, and Nutrition is never asked
	on, status, url := false, http.StatusOK, "https://nutrition.bjelke.test/"
	auths := nutritionCore(t, s, &on, &status, &url)
	if b, secs := brief(); find(secs) != nil || !notConnected(b) || len(*auths) != 0 {
		t.Fatalf("link off: %+v %v", b, *auths)
	}

	// the link on: the card, read from Nutrition as the person, with Nutrition's own address
	on = true
	b, secs := brief()
	sec := find(secs)
	if sec == nil || notConnected(b) || sec.Error != "" {
		t.Fatalf("link on: %+v", b)
	}
	if len(*auths) != 1 || (*auths)[0] != "Bearer pcx_nutrition" {
		t.Fatalf("nutrition asked with %v", *auths)
	}
	data := sec.Data.(map[string]any)
	res := data["result"].(map[string]any)
	if data["source_url"] != "https://nutrition.bjelke.test/" || res["streak"] != float64(12) || res["goals"].(map[string]any)["budget"] != float64(2400) || res["week"].(map[string]any)["on_goal"] != float64(2) {
		t.Fatalf("card data: %+v", data)
	}

	// Nutrition without its own address: the public one from Core
	url = ""
	_, secs = brief()
	if got := find(secs).Data.(map[string]any)["source_url"]; got != "https://nutrition.example.test/" {
		t.Fatalf("source_url from core: %v", got)
	}

	// Nutrition answers with an error: the card says so in plain words
	status = http.StatusInternalServerError
	_, secs = brief()
	if sec := find(secs); sec == nil || sec.Error == "" || sec.Data != nil {
		t.Fatalf("nutrition error: %+v", sec)
	}
}
