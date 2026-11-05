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

// The Core link Pocket AI → Pocket Nutrition (nutrition:read). While it is on,
// the briefing has a Nutrition card: today's calories against the budget and
// what's left, protein and water against their goals, this week's days logged
// and on goal with the average, and the days in a row, read from Nutrition as
// the person. While it is off (or there is no Nutrition) the card is left out
// and listed as not connected, like any other source.
const (
	nutritionScope = "nutrition:read"
	nutritionTool  = "pocket_nutrition_summary"
)

var nutritionUse = core.Use{App: "nutrition", Scopes: []string{nutritionScope},
	Reason: "Show what you ate in your briefing: today's calories and what's left, protein and water against your goals, and the week so far"}

var nutritionHTTP = &http.Client{Timeout: 10 * time.Second}

// nutritionSection is the briefing's Nutrition card; ok is false when Core, Nutrition
// or the link isn't there for the person, so the card is left out.
func (s *Server) nutritionSection(ctx context.Context, uid int64, tid string) (appSection, bool) {
	if !s.core.Configured() {
		return appSection{}, false
	}
	u, err := s.store.GetUser(ctx, uid)
	if err != nil || u.Email == "" {
		return appSection{}, false
	}
	if on, _ := s.core.LinkOn(ctx, u.Email, "nutrition", nutritionScope); !on {
		return appSection{}, false
	}
	start := time.Now()
	sec := appSection{Tool: nutritionTool, Checked: start.UTC().Format(time.RFC3339), Bounded: true}
	id := s.startAppCall(ctx, uid, tid, nutritionTool, json.RawMessage(`{}`), false)
	var sum map[string]any
	public, err := s.nutritionCall(ctx, u.Email, "/api/v1/summary", &sum)
	result := toolResult(sum, false)
	if err != nil {
		result = toolFailure(err.Error())
	}
	sec.Duration = time.Since(start).Milliseconds()
	status := "done"
	if err != nil {
		status = "error"
		sec.Error = "Pocket Nutrition could not be read. Open Nutrition or check the link in Pocket Core."
	} else {
		data := map[string]any{"result": sum}
		// the card's links open Nutrition's pages under this address
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

// nutritionCall makes a Nutrition API GET as the person and decodes the answer;
// it also gives Nutrition's public address from Core.
func (s *Server) nutritionCall(ctx context.Context, email, path string, out any) (string, error) {
	tok, err := s.core.ActAs(ctx, email, "nutrition", nutritionScope)
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
	res, err := nutritionHTTP.Do(req)
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
		return tok.PublicURL, fmt.Errorf("pocket nutrition: %s", e.Error)
	}
	return tok.PublicURL, json.NewDecoder(http.MaxBytesReader(nil, res.Body, 2<<20)).Decode(out)
}
