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
// no session is written. An answer without any window (the CLI had not
// fetched them yet) is asked again a few times, then is an error: an empty
// read must not pass for "no limits". r is another machine's login (nil =
// here; cwd is only used here).
func ClaudeLimits(ctx context.Context, r *Remote, bin, cwd string, env []string) ([]Window, string, error) {
	if bin == "" {
		bin = "claude"
	}
	ctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	args := []string{"-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose",
		"--permission-prompt-tool", "stdio", "--no-session-persistence"}
	var cmd *exec.Cmd
	var err error
	if r != nil {
		cmd, err = Command(r, "", env, bin, args...)
	} else {
		cmd, err = LocalCmd(ctx, cwd, env, bin, args...)
	}
	if err != nil {
		return nil, "", err
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		return nil, "", err
	}
	// ssh has no context of its own: the timeout ends it here
	stop := context.AfterFunc(ctx, func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) })
	defer stop()
	defer func() {
		_ = stdin.Close()
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		_ = cmd.Wait()
	}()
	ask := func(n int) {
		_, _ = fmt.Fprintf(stdin, `{"type":"control_request","request_id":"usage-%d","request":{"subtype":"get_usage"}}`+"\n", n)
	}
	_, _ = stdin.Write([]byte(`{"type":"control_request","request_id":"init","request":{"subtype":"initialize"}}` + "\n"))
	const tries = 4
	n := 1
	ask(n)
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 1<<20), 32<<20)
	for sc.Scan() {
		w, note, ok, err := parseClaudeUsage(sc.Bytes())
		if !ok {
			continue
		}
		if err != nil || len(w) > 0 {
			return w, note, err
		}
		if n >= tries {
			return nil, "", errors.New("Claude Code answered without any limits; try again in a minute")
		}
		select {
		case <-ctx.Done():
		case <-time.After(time.Duration(n) * 2 * time.Second):
			n++
			ask(n)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, "", fmt.Errorf("claude: no usage answer: %w", err)
	}
	return nil, "", errors.New("claude: no usage answer")
}

// claudeBucket is one of the fixed windows of a get_usage answer.
type claudeBucket struct {
	Utilization *float64 `json:"utilization"`
	ResetsAt    string   `json:"resets_at"`
}

// parseClaudeUsage reads one stream-json line; ok is false for any line
// that is not the answer to get_usage.
func parseClaudeUsage(line []byte) (out []Window, note string, ok bool, err error) {
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
								DisplayName string `json:"display_name"`
							} `json:"model"`
							Surface *struct {
								DisplayName string `json:"display_name"`
							} `json:"surface"`
						} `json:"scope"`
					} `json:"limits"`
					// The fixed windows, for an answer without "limits".
					FiveHour     *claudeBucket `json:"five_hour"`
					SevenDay     *claudeBucket `json:"seven_day"`
					SevenDayOpus *claudeBucket `json:"seven_day_opus"`
					SevenDaySonn *claudeBucket `json:"seven_day_sonnet"`
					ModelScoped  []struct {
						DisplayName string   `json:"display_name"`
						Utilization *float64 `json:"utilization"`
						ResetsAt    string   `json:"resets_at"`
					} `json:"model_scoped"`
					ExtraUsage *struct {
						IsEnabled bool `json:"is_enabled"`
					} `json:"extra_usage"`
				} `json:"rate_limits"`
			} `json:"response"`
		} `json:"response"`
	}
	if json.Unmarshal(line, &m) != nil || m.Type != "control_response" || !strings.HasPrefix(m.Response.RequestID, "usage") {
		return nil, "", false, nil
	}
	if m.Response.Subtype != "success" {
		return nil, "", true, errors.New(firstNonEmpty(m.Response.Error, "claude: get_usage failed"))
	}
	r := m.Response.Response
	if !r.RateLimitsAvailable {
		return nil, "", true, errors.New("Claude Code has no plan limits for this account")
	}
	rl := r.RateLimits
	for _, l := range rl.Limits {
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
	if len(out) == 0 {
		add := func(id, name string, b *claudeBucket) {
			if b != nil && b.Utilization != nil {
				out = append(out, Window{ID: id, Name: name, Used: *b.Utilization / 100, ResetsAt: b.ResetsAt})
			}
		}
		add("five_hour", "5 hours", rl.FiveHour)
		add("seven_day", "7 days", rl.SevenDay)
		models := map[string]bool{}
		for _, s := range rl.ModelScoped {
			if s.Utilization != nil && s.DisplayName != "" {
				models[strings.ToLower(s.DisplayName)] = true
				add("weekly_scoped:"+strings.ToLower(s.DisplayName), "7 days · "+s.DisplayName, &claudeBucket{s.Utilization, s.ResetsAt})
			}
		}
		if !models["opus"] {
			add("weekly_scoped:opus", "7 days · Opus", rl.SevenDayOpus)
		}
		if !models["sonnet"] {
			add("weekly_scoped:sonnet", "7 days · Sonnet", rl.SevenDaySonn)
		}
	}
	if rl.ExtraUsage != nil && rl.ExtraUsage.IsEnabled {
		note = "Extra usage is on: past the limits, usage is billed."
	}
	return out, note, true, nil
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

// CodexLimits reads the plan limits and the free resets Codex has granted;
// remote is another machine's login (nil = here).
func CodexLimits(ctx context.Context, remote *Remote, bin string, env []string) ([]Window, *Resets, error) {
	dir := ""
	if remote == nil {
		dir = os.TempDir()
	}
	c, err := startRPC(bin, dir, env, remote)
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

// antigravityUsageScript prints `agy -p /usage` as JSON: $1 the CLI, $2 a
// login folder ("" = its default). The print mode knows no --gemini-dir, so
// another login folder is shown to it as ~/.gemini of a throwaway home.
const antigravityUsageScript = `p=$(command -v "$1" 2>/dev/null) || { echo "$1 is not on the PATH" >&2; exit 127; }
if [ -n "$2" ]; then
  t=$(mktemp -d) || exit 3
  trap 'rm -rf "$t"' EXIT
  ln -s "$2" "$t/.gemini" || exit 3
  HOME="$t"; export HOME
fi
cd "${TMPDIR:-/tmp}" 2>/dev/null
"$p" -p "/usage" --output-format json </dev/null`

// AntigravityLimits asks Antigravity CLI for the plan limits of a login: the
// numbers of its /usage panel (a 5-hour and a weekly limit per model group),
// from its print mode. No prompt reaches a model and no conversation is
// written. remote is another machine's login (nil = here); dir its login
// folder ("" = the default).
func AntigravityLimits(ctx context.Context, remote *Remote, bin, dir string) ([]Window, error) {
	if bin == "" {
		bin = "agy"
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	var out, errOut string
	var err error
	if remote != nil {
		out, errOut, err = remote.Run(Repeatable(ctx), 40*time.Second, antigravityUsageScript, bin, dir)
	} else {
		cmd := exec.CommandContext(ctx, "sh", "-c", antigravityUsageScript, "sh", bin, dir)
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
		var eb strings.Builder
		cmd.Stderr = &eb
		var b []byte
		b, err = cmd.Output()
		out, errOut = string(b), eb.String()
	}
	w, perr := parseAntigravityUsage(out)
	if perr == nil {
		return w, nil
	}
	if strings.Contains(out+errOut, "Authentication required") {
		return nil, errors.New("not signed in")
	}
	if err != nil {
		return nil, errors.New(oneLine(firstNonEmpty(lastLines(errOut, 2), lastLines(out, 2), err.Error()), 200))
	}
	return nil, perr
}

// parseAntigravityUsage turns `agy -p /usage --output-format json` into
// windows: used = what is gone of each limit. The first group (Gemini
// models) always shows; other groups (Claude and GPT models) only once
// something of them is used.
func parseAntigravityUsage(raw string) ([]Window, error) {
	i := strings.IndexByte(raw, '{')
	if i < 0 {
		return nil, errors.New("it did not say")
	}
	var r struct {
		Status  string `json:"status"`
		Command struct {
			Data struct {
				Groups []struct {
					Name    string `json:"name"`
					Buckets []struct {
						ID        string   `json:"id"`
						Window    string   `json:"window"`
						Remaining *float64 `json:"remaining_fraction"`
						ResetTime string   `json:"reset_time"`
					} `json:"buckets"`
				} `json:"groups"`
			} `json:"data"`
		} `json:"command"`
	}
	if err := json.Unmarshal([]byte(raw[i:]), &r); err != nil || len(r.Command.Data.Groups) == 0 {
		return nil, errors.New("it did not say")
	}
	var out []Window
	for gi, g := range r.Command.Data.Groups {
		var ws []Window
		used := false
		for _, b := range g.Buckets {
			if b.Remaining == nil {
				continue
			}
			w := Window{ID: b.ID, Used: 1 - *b.Remaining}
			if w.Used < 0 {
				w.Used = 0
			}
			switch b.Window {
			case "5h":
				w.Name = "5 hours"
			case "weekly":
				w.Name = "7 days"
			default:
				w.Name = firstNonEmpty(b.Window, "limit")
			}
			if gi > 0 { // "Claude and GPT models" → "7 days · Claude and GPT"
				w.Name += " · " + strings.TrimSuffix(strings.TrimSuffix(g.Name, " models"), " Models")
			}
			if t, err := time.Parse(time.RFC3339, b.ResetTime); err == nil && t.Year() > 2000 {
				w.ResetsAt = t.UTC().Format(time.RFC3339)
			}
			used = used || w.Used > 0.0005
			ws = append(ws, w)
		}
		if gi > 0 && !used {
			continue
		}
		// the short window first, like the other CLIs' cards
		sort.SliceStable(ws, func(a, b int) bool {
			return strings.HasPrefix(ws[a].Name, "5 hours") && !strings.HasPrefix(ws[b].Name, "5 hours")
		})
		out = append(out, ws...)
	}
	if len(out) == 0 {
		return nil, errors.New("it named no limits")
	}
	return out, nil
}
