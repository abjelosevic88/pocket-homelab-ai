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

// The Core link Pocket AI → Pocket Fitness (fitness:read). While it is on, the
// briefing has a Fitness card: today's planned workout (or the one going on
// now), the week so far against the goal, the last workout and new records,
// read from Fitness as the person. While it is off (or there is no Fitness)
// the card is left out and listed as not connected, like any other source.
const (
	fitnessScope = "fitness:read"
	fitnessTool  = "pocket_fitness_summary"
)

var fitnessUse = core.Use{App: "fitness", Scopes: []string{fitnessScope},
	Reason: "Show your training in your briefing: today's planned workout, the week so far, your last workout and new records"}

var fitnessHTTP = &http.Client{Timeout: 10 * time.Second}

// fitnessSection is the briefing's Fitness card; ok is false when Core, Fitness
// or the link isn't there for the person, so the card is left out.
func (s *Server) fitnessSection(ctx context.Context, uid int64, tid string) (appSection, bool) {
	if !s.core.Configured() {
		return appSection{}, false
	}
	u, err := s.store.GetUser(ctx, uid)
	if err != nil || u.Email == "" {
		return appSection{}, false
	}
	if on, _ := s.core.LinkOn(ctx, u.Email, "fitness", fitnessScope); !on {
		return appSection{}, false
	}
	start := time.Now()
	sec := appSection{Tool: fitnessTool, Checked: start.UTC().Format(time.RFC3339), Bounded: true}
	id := s.startAppCall(ctx, uid, tid, fitnessTool, json.RawMessage(`{}`), false)
	var sum map[string]any
	public, err := s.fitnessCall(ctx, u.Email, "/api/v1/summary", &sum)
	result := toolResult(sum, false)
	if err != nil {
		result = toolFailure(err.Error())
	}
	sec.Duration = time.Since(start).Milliseconds()
	status := "done"
	if err != nil {
		status = "error"
		sec.Error = "Pocket Fitness could not be read. Open Fitness or check the link in Pocket Core."
	} else {
		data := map[string]any{"result": sum}
		// the card's links open Fitness's pages under this address
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

// fitnessCall makes a Fitness API GET as the person and decodes the answer;
// it also gives Fitness's public address from Core.
func (s *Server) fitnessCall(ctx context.Context, email, path string, out any) (string, error) {
	tok, err := s.core.ActAs(ctx, email, "fitness", fitnessScope)
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
	res, err := fitnessHTTP.Do(req)
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
		return tok.PublicURL, fmt.Errorf("pocket fitness: %s", e.Error)
	}
	return tok.PublicURL, json.NewDecoder(http.MaxBytesReader(nil, res.Body, 2<<20)).Decode(out)
}
