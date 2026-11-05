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

// The Core link Pocket AI → Pocket Weather (weather:read). While it is on, the
// briefing has a Weather card: the weather at the person's home place now,
// today and tomorrow, and the warnings for those two days, read from Weather
// as the person. While it is off (or there is no Weather) the card is left out
// and listed as not connected, like any other source.
const (
	weatherScope = "weather:read"
	weatherTool  = "pocket_weather_summary"
)

var weatherUse = core.Use{App: "weather", Scopes: []string{weatherScope},
	Reason: "Show the weather at home in your briefing: now, today and tomorrow, and any warnings"}

var weatherHTTP = &http.Client{Timeout: 10 * time.Second}

// weatherSection is the briefing's Weather card; ok is false when Core, Weather
// or the link isn't there for the person, so the card is left out.
func (s *Server) weatherSection(ctx context.Context, uid int64, tid string) (appSection, bool) {
	if !s.core.Configured() {
		return appSection{}, false
	}
	u, err := s.store.GetUser(ctx, uid)
	if err != nil || u.Email == "" {
		return appSection{}, false
	}
	if on, _ := s.core.LinkOn(ctx, u.Email, "weather", weatherScope); !on {
		return appSection{}, false
	}
	start := time.Now()
	sec := appSection{Tool: weatherTool, Checked: start.UTC().Format(time.RFC3339), Bounded: true}
	id := s.startAppCall(ctx, uid, tid, weatherTool, json.RawMessage(`{}`), false)
	var sum map[string]any
	err = s.weatherCall(ctx, u.Email, "/api/v1/summary", &sum)
	result := toolResult(sum, false)
	if err != nil {
		result = toolFailure(err.Error())
	}
	sec.Duration = time.Since(start).Milliseconds()
	status := "done"
	if err != nil {
		status = "error"
		sec.Error = "Pocket Weather could not be read. Open Weather or check the link in Pocket Core."
	} else {
		data := map[string]any{"result": sum}
		if url, _ := sum["url"].(string); url != "" {
			data["source_url"] = url
		}
		sec.Data = boundData(data, 0)
	}
	s.finishAppCall(id, status, result, time.Since(start))
	return sec, true
}

// weatherCall makes a Weather API GET as the person and decodes the answer.
func (s *Server) weatherCall(ctx context.Context, email, path string, out any) error {
	tok, err := s.core.ActAs(ctx, email, "weather", weatherScope)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, onThisHost(strings.TrimRight(tok.URL, "/"))+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "PocketAI/"+Version)
	res, err := weatherHTTP.Do(req)
	if err != nil {
		return err
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
		return fmt.Errorf("pocket weather: %s", e.Error)
	}
	return json.NewDecoder(http.MaxBytesReader(nil, res.Body, 2<<20)).Decode(out)
}
