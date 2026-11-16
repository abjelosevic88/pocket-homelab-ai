package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	core "github.com/abjelosevic88/pocket-homelab-core/sdk"
)

// The Core link Pocket AI → Pocket Booking (booking:read). While it is on,
// the briefing has a Booking card: today's bookings (when, who, which page)
// and the ones waiting for the person to confirm, read from Booking's
// summary as the person. While it is off (or there is no Booking) the card
// is left out and listed as not connected, like any other source.
const (
	bookingScope = "booking:read"
	bookingTool  = "pocket_booking_summary"
)

var bookingUse = core.Use{App: "booking", Scopes: []string{bookingScope},
	Reason: "Show your bookings in your briefing: who booked a time with you today and the bookings waiting for you to confirm"}

var bookingHTTP = &http.Client{Timeout: 10 * time.Second}

// bookingSection is the briefing's Booking card; ok is false when Core, Booking
// or the link isn't there for the person, so the card is left out.
func (s *Server) bookingSection(ctx context.Context, uid int64, tid string) (appSection, bool) {
	if !s.core.Configured() {
		return appSection{}, false
	}
	u, err := s.store.GetUser(ctx, uid)
	if err != nil || u.Email == "" {
		return appSection{}, false
	}
	if on, _ := s.core.LinkOn(ctx, u.Email, "booking", bookingScope); !on {
		return appSection{}, false
	}
	start := time.Now()
	sec := appSection{Tool: bookingTool, Checked: start.UTC().Format(time.RFC3339), Bounded: true}
	id := s.startAppCall(ctx, uid, tid, bookingTool, json.RawMessage(`{}`), false)
	var sum map[string]any
	public, err := s.bookingCall(ctx, u.Email, "/api/v1/summary", &sum)
	result := toolResult(sum, false)
	if err != nil {
		result = toolFailure(err.Error())
	}
	sec.Duration = time.Since(start).Milliseconds()
	status := "done"
	if err != nil {
		status = "error"
		sec.Error = "Pocket Booking could not be read. Open Booking or check the link in Pocket Core."
	} else {
		data := map[string]any{"result": sum}
		// the card's links open Booking's pages under this address
		url, _ := sum["url"].(string)
		if url == "" && public != "" {
			url = strings.TrimRight(public, "/") + "/"
		}
		if url != "" {
			data["source_url"] = url
		}
		sec.Data = boundData(data, 0)
	}
	s.finishAppCall(id, status, result, time.Since(start))
	return sec, true
}

// bookingCall makes a Booking API GET as the person and decodes the answer;
// it also gives Booking's public address from Core.
func (s *Server) bookingCall(ctx context.Context, email, path string, out any) (string, error) {
	tok, err := s.core.ActAs(ctx, email, "booking", bookingScope)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, onThisHost(strings.TrimRight(tok.URL, "/"))+path, nil)
	if err != nil {
		return tok.PublicURL, err
	}
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "PocketAI/"+Version)
	res, err := bookingHTTP.Do(req)
	if err != nil {
		return tok.PublicURL, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(http.MaxBytesReader(nil, res.Body, 64<<10)).Decode(&e)
		if e.Error == "" {
			e.Error = res.Status
		}
		return tok.PublicURL, fmt.Errorf("pocket booking: %s", e.Error)
	}
	return tok.PublicURL, json.NewDecoder(http.MaxBytesReader(nil, res.Body, 2<<20)).Decode(out)
}
