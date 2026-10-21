package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"syscall"
	"time"
)

// ClaudeLimits asks Claude Code for the account's plan limits — the same
// numbers its /usage screen shows, per-model weekly limits (Fable, …)
// included — through the get_usage control request. No prompt is sent and
// no session is written.
func ClaudeLimits(ctx context.Context, bin, cwd string) ([]Window, string, error) {
	if bin == "" {
		bin = "claude"
	}
	ctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose",
		"--permission-prompt-tool", "stdio", "--no-session-persistence")
	cmd.Dir = cwd
	cmd.Env = os.Environ()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		return nil, "", err
	}
	defer func() {
		_ = stdin.Close()
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		_ = cmd.Wait()
	}()
	_, _ = stdin.Write([]byte(`{"type":"control_request","request_id":"init","request":{"subtype":"initialize"}}` + "\n" +
		`{"type":"control_request","request_id":"usage","request":{"subtype":"get_usage"}}` + "\n"))
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 1<<20), 32<<20)
	for sc.Scan() {
		var m struct {
			Type     string `json:"type"`
			Response struct {
				Subtype   string `json:"subtype"`
				RequestID string `json:"request_id"`
				Error     string `json:"error"`
				Response  struct {
					RateLimitsAvailable bool `json:"rate_limits_available"`
					RateLimits          struct {
						Limits []struct {
							Kind     string  `json:"kind"`
							Percent  float64 `json:"percent"`
							ResetsAt string  `json:"resets_at"`
							Scope    *struct {
								Model *struct {
									ID          *string `json:"id"`
									DisplayName string  `json:"display_name"`
								} `json:"model"`
								Surface *struct {
									DisplayName string `json:"display_name"`
								} `json:"surface"`
							} `json:"scope"`
						} `json:"limits"`
						ExtraUsage *struct {
							IsEnabled bool `json:"is_enabled"`
						} `json:"extra_usage"`
					} `json:"rate_limits"`
				} `json:"response"`
			} `json:"response"`
		}
		if json.Unmarshal(sc.Bytes(), &m) != nil || m.Type != "control_response" || m.Response.RequestID != "usage" {
			continue
		}
		if m.Response.Subtype != "success" {
			return nil, "", errors.New(firstNonEmpty(m.Response.Error, "claude: get_usage failed"))
		}
		r := m.Response.Response
		if !r.RateLimitsAvailable {
			return nil, "", errors.New("Claude Code has no plan limits for this account")
		}
		var out []Window
		for _, l := range r.RateLimits.Limits {
			w := Window{Used: l.Percent / 100, ResetsAt: l.ResetsAt}
			switch l.Kind {
			case "session":
				w.ID, w.Name = "five_hour", "5 hours"
			case "weekly_all":
				w.ID, w.Name = "seven_day", "7 days"
			default:
				scope := ""
				if l.Scope != nil && l.Scope.Model != nil {
					scope = l.Scope.Model.DisplayName
				} else if l.Scope != nil && l.Scope.Surface != nil {
					scope = l.Scope.Surface.DisplayName
				}
				period := "7 days"
				if strings.HasPrefix(l.Kind, "session") {
					period = "5 hours"
				}
				w.ID = l.Kind + ":" + strings.ToLower(scope)
				w.Name = strings.TrimSuffix(period+" · "+scope, " · ")
			}
			out = append(out, w)
		}
		note := ""
		if r.RateLimits.ExtraUsage != nil && r.RateLimits.ExtraUsage.IsEnabled {
			note = "Extra usage is on: past the limits, usage is billed."
		}
		return out, note, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, "", fmt.Errorf("claude: no usage answer: %w", err)
	}
	return nil, "", errors.New("claude: no usage answer")
}

// CodexLimits reads every Codex rate-limit bucket of the account.
// Resets are the free "reset my limits" credits Codex grants now and then;
// each one expires.
type Resets struct {
	Available int           `json:"available"`
	Credits   []ResetCredit `json:"credits,omitempty"` // soonest to expire first
}

// ResetCredit is one free reset.
type ResetCredit struct {
	Title     string `json:"title"`
	GrantedAt string `json:"granted_at,omitempty"`
	ExpiresAt string `json:"expires_at,omitempty"`
}

// CodexLimits reads the plan limits and the free resets Codex has granted.
func CodexLimits(ctx context.Context, bin string) ([]Window, *Resets, error) {
	c, err := startRPC(bin, os.TempDir(), nil)
	if err != nil {
		return nil, nil, err
	}
	defer c.close()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := c.initialize(ctx, Version); err != nil {
		return nil, nil, err
	}
	res, err := c.call(ctx, "account/rateLimits/read", nil)
	if err != nil {
		return nil, nil, err
	}
	type snap struct {
		LimitID   string       `json:"limitId"`
		LimitName *string      `json:"limitName"`
		Primary   *codexWindow `json:"primary"`
		Secondary *codexWindow `json:"secondary"`
	}
	var r struct {
		RateLimits            snap            `json:"rateLimits"`
		RateLimitsByLimitID   map[string]snap `json:"rateLimitsByLimitId"`
		RateLimitResetCredits *struct {
			AvailableCount int `json:"availableCount"`
			Credits        []struct {
				Status    string `json:"status"`
				Title     string `json:"title"`
				GrantedAt int64  `json:"grantedAt"`
				ExpiresAt *int64 `json:"expiresAt"`
			} `json:"credits"`
		} `json:"rateLimitResetCredits"`
	}
	if err := json.Unmarshal(res, &r); err != nil {
		return nil, nil, err
	}
	buckets := r.RateLimitsByLimitID
	if len(buckets) == 0 {
		buckets = map[string]snap{r.RateLimits.LimitID: r.RateLimits}
	}
	ids := make([]string, 0, len(buckets))
	for id := range buckets {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] == "codex" || (ids[j] != "codex" && ids[i] < ids[j]) })
	var out []Window
	for _, id := range ids {
		b := buckets[id]
		label := ""
		if id != "codex" && id != "" {
			label = " · " + id
			if b.LimitName != nil && *b.LimitName != "" {
				label = " · " + *b.LimitName
			}
		}
		for _, w := range []*codexWindow{b.Primary, b.Secondary} {
			if w != nil {
				out = append(out, Window{ID: codexWindowID(id, w.WindowDurationMins), Name: windowName(w.WindowDurationMins) + label,
					Used: float64(w.UsedPercent) / 100, ResetsAt: unixISO(w.ResetsAt)})
			}
		}
	}
	var rs *Resets
	if c := r.RateLimitResetCredits; c != nil {
		rs = &Resets{Available: c.AvailableCount}
		for _, x := range c.Credits {
			if x.Status != "" && x.Status != "available" {
				continue
			}
			rc := ResetCredit{Title: firstNonEmpty(x.Title, "Free reset"), GrantedAt: unixISO(x.GrantedAt)}
			if x.ExpiresAt != nil {
				rc.ExpiresAt = unixISO(*x.ExpiresAt)
			}
			rs.Credits = append(rs.Credits, rc)
		}
		sort.SliceStable(rs.Credits, func(i, j int) bool {
			a, b := rs.Credits[i].ExpiresAt, rs.Credits[j].ExpiresAt
			return a != "" && (b == "" || a < b)
		})
	}
	return out, rs, nil
}

func codexWindowID(bucket string, mins int64) string {
	if bucket == "" {
		bucket = "codex"
	}
	return fmt.Sprintf("%s:%d", bucket, mins)
}
