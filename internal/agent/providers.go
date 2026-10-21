package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

func cliVersion(ctx context.Context, bin string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "--version").Output()
	if err != nil {
		return "", err
	}
	v := strings.TrimSpace(string(out))
	v = strings.TrimSuffix(v, " (Claude Code)")
	v = strings.TrimPrefix(v, "codex-cli ")
	return v, nil
}

// ProbeClaude asks Claude Code for its version, models and account without
// sending a prompt (the initialize handshake only).
func ProbeClaude(ctx context.Context, bin, cwd string) Provider {
	if bin == "" {
		bin = "claude"
	}
	p := Provider{ID: "claude", Name: "Claude Code", Modes: ClaudeModes, DefaultMode: "default", DefaultEffort: "high",
		Efforts: []string{"low", "medium", "high", "xhigh", "max"}, DefaultModel: "default"}
	v, err := cliVersion(ctx, bin)
	if err != nil {
		p.Error = "claude is not installed or not on PATH (" + err.Error() + ")"
		return p
	}
	p.Version, p.Available = v, true
	p.Models = []Model{{ID: "default", Name: "Default", Efforts: p.Efforts}, {ID: "opus", Name: "Opus", Efforts: p.Efforts},
		{ID: "sonnet", Name: "Sonnet", Efforts: p.Efforts}, {ID: "haiku", Name: "Haiku", Efforts: p.Efforts}}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose", "--permission-prompt-tool", "stdio")
	cmd.Dir = cwd
	cmd.Env = os.Environ()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		return p
	}
	defer func() {
		_ = stdin.Close()
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		_ = cmd.Wait()
	}()
	_, _ = stdin.Write([]byte(`{"type":"control_request","request_id":"probe","request":{"subtype":"initialize"}}` + "\n"))
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 1<<20), 32<<20)
	for sc.Scan() {
		var m struct {
			Type     string `json:"type"`
			Response struct {
				RequestID string `json:"request_id"`
				Response  struct {
					Models []struct {
						Value                 string   `json:"value"`
						DisplayName           string   `json:"displayName"`
						Description           string   `json:"description"`
						SupportedEffortLevels []string `json:"supportedEffortLevels"`
					} `json:"models"`
					Account struct {
						Email            string `json:"email"`
						SubscriptionType string `json:"subscriptionType"`
					} `json:"account"`
				} `json:"response"`
			} `json:"response"`
		}
		if json.Unmarshal(sc.Bytes(), &m) != nil || m.Type != "control_response" || m.Response.RequestID != "probe" {
			continue
		}
		if len(m.Response.Response.Models) > 0 {
			p.Models = nil
			for _, md := range m.Response.Response.Models {
				p.Models = append(p.Models, Model{ID: md.Value, Name: md.DisplayName, Description: md.Description, Efforts: md.SupportedEffortLevels})
			}
		}
		a := m.Response.Response.Account
		p.Account = strings.Trim(strings.Join([]string{a.SubscriptionType, a.Email}, " · "), " ·")
		break
	}
	return p
}

// ProbeCodex asks the Codex app-server for its models and account.
func ProbeCodex(ctx context.Context, bin, cwd string) Provider {
	if bin == "" {
		bin = "codex"
	}
	p := Provider{ID: "codex", Name: "Codex", Modes: CodexModes, DefaultMode: "on-request", DefaultEffort: "high",
		Efforts: []string{"low", "medium", "high", "xhigh"}}
	v, err := cliVersion(ctx, bin)
	if err != nil {
		p.Error = "codex is not installed or not on PATH (" + err.Error() + ")"
		return p
	}
	p.Version, p.Available = v, true
	c, err := startRPC(bin, cwd, nil)
	if err != nil {
		p.Error = err.Error()
		return p
	}
	defer c.close()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := c.initialize(ctx, Version); err != nil {
		p.Error = err.Error()
		return p
	}
	if res, err := c.call(ctx, "model/list", map[string]any{}); err == nil {
		var r struct {
			Data []struct {
				ID                        string `json:"id"`
				DisplayName               string `json:"displayName"`
				Description               string `json:"description"`
				Hidden                    bool   `json:"hidden"`
				IsDefault                 bool   `json:"isDefault"`
				SupportedReasoningEfforts []struct {
					ReasoningEffort string `json:"reasoningEffort"`
				} `json:"supportedReasoningEfforts"`
			} `json:"data"`
		}
		if json.Unmarshal(res, &r) == nil {
			for _, m := range r.Data {
				if m.Hidden {
					continue
				}
				md := Model{ID: m.ID, Name: m.DisplayName, Description: m.Description}
				for _, e := range m.SupportedReasoningEfforts {
					md.Efforts = append(md.Efforts, e.ReasoningEffort)
				}
				if m.IsDefault {
					p.DefaultModel = m.ID
					if len(md.Efforts) > 0 {
						p.Efforts = md.Efforts
					}
				}
				p.Models = append(p.Models, md)
			}
		}
	}
	if res, err := c.call(ctx, "account/read", map[string]any{}); err == nil {
		var r struct {
			Account struct {
				Type     string `json:"type"`
				Email    string `json:"email"`
				PlanType string `json:"planType"`
			} `json:"account"`
		}
		if json.Unmarshal(res, &r) == nil {
			p.Account = strings.Trim(strings.Join([]string{strings.ToUpper(r.Account.PlanType), r.Account.Email}, " · "), " ·")
		}
	}
	return p
}
