package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
)

// bookingCore fakes Pocket Core (the person's links, link tokens) and Pocket
// Booking behind it; status is what Booking answers, url its own "url".
func bookingCore(t *testing.T, s *Server, linkOn *bool, status *int, url *string) *[]string {
	t.Helper()
	auths := &[]string{}
	booking := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/summary" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		*auths = append(*auths, r.Header.Get("Authorization"))
		if *status != http.StatusOK {
			w.WriteHeader(*status)
			json.NewEncoder(w).Encode(map[string]string{"error": "not now"})
			return
		}
		ana := map[string]any{"id": 41, "type_id": 3, "title": "Intro call", "emoji": "📞", "status": "confirmed",
			"start": "2026-11-18T10:00:00+01:00", "end": "2026-11-18T10:30:00+01:00", "minutes": 30, "date": "2026-11-18", "time": "10:00", "end_time": "10:30",
			"name": "Ana Marić", "email": "ana@example.com", "location_kind": "video", "meeting_url": "https://meet.jit.si/pocket-x", "url": "https://booking.bjelke.test/bookings/41"}
		marko := map[string]any{"id": 42, "type_id": 3, "title": "Intro call", "status": "pending", "start": "2026-11-19T09:00:00+01:00", "date": "2026-11-19", "time": "09:00",
			"name": "Marko", "email": "marko@example.com", "url": "https://booking.bjelke.test/bookings/42"}
		json.NewEncoder(w).Encode(map[string]any{"upcoming": 6, "pending": 1, "today": 1, "this_week": 5, "types_open": 3, "next": ana,
			"today_list": []any{ana}, "pending_list": []any{marko}, "line": "1 booking today, 1 waits for you to confirm", "url": *url})
	}))
	t.Cleanup(booking.Close)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/links/status":
			json.NewEncoder(w).Encode([]core.LinkState{{App: "booking", Scope: bookingScope, On: *linkOn, State: map[bool]string{true: "on", false: "off_person"}[*linkOn]}})
		case "/api/v1/links/token":
			json.NewEncoder(w).Encode(core.LinkToken{AccessToken: "pcx_booking", URL: booking.URL, PublicURL: "https://booking.example.test", App: "booking"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(upstream.Close)
	s.core = core.New(upstream.URL, core.Options{DB: s.store.DB, Token: "app"})
	return auths
}

func TestBriefingBooking(t *testing.T) {
	s, h, _ := testServer(t)
	_, alice := person(t, s, h, "booking@example.test", "member", true)
	ctx := context.Background()
	brief := func() (map[string]any, []appSection) {
		t.Helper()
		b := s.briefing(ctx, alice.ID, "", map[string]askTool{}).(map[string]any)
		return b, b["sections"].([]appSection)
	}
	notConnected := func(b map[string]any) bool {
		for _, n := range b["unavailable"].([]string) {
			if n == bookingTool {
				return true
			}
		}
		return false
	}
	find := func(secs []appSection) *appSection {
		for i := range secs {
			if secs[i].Tool == bookingTool {
				return &secs[i]
			}
		}
		return nil
	}

	// no Core: no card, listed as not connected
	if b, secs := brief(); find(secs) != nil || !notConnected(b) {
		t.Fatalf("without core: %+v", b)
	}

	// the link off: still no card, and Booking is never asked
	on, status, url := false, http.StatusOK, "https://booking.bjelke.test/"
	auths := bookingCore(t, s, &on, &status, &url)
	if b, secs := brief(); find(secs) != nil || !notConnected(b) || len(*auths) != 0 {
		t.Fatalf("link off: %+v %v", b, *auths)
	}

	// the link on: the card, read from Booking as the person, with Booking's own address
	on = true
	b, secs := brief()
	sec := find(secs)
	if sec == nil || notConnected(b) || sec.Error != "" {
		t.Fatalf("link on: %+v", b)
	}
	if len(*auths) != 1 || (*auths)[0] != "Bearer pcx_booking" {
		t.Fatalf("booking asked with %v", *auths)
	}
	data := sec.Data.(map[string]any)
	res := data["result"].(map[string]any)
	if data["source_url"] != "https://booking.bjelke.test/" || res["pending"] != float64(1) || len(res["today_list"].([]any)) != 1 || res["pending_list"].([]any)[0].(map[string]any)["name"] != "Marko" {
		t.Fatalf("card data: %+v", data)
	}

	// Booking without its own address: the public one from Core
	url = ""
	_, secs = brief()
	if got := find(secs).Data.(map[string]any)["source_url"]; got != "https://booking.example.test/" {
		t.Fatalf("source_url from core: %v", got)
	}

	// Booking answers with an error: the card says so in plain words
	status = http.StatusInternalServerError
	_, secs = brief()
	if sec := find(secs); sec == nil || sec.Error == "" || sec.Data != nil {
		t.Fatalf("booking error: %+v", sec)
	}
}
