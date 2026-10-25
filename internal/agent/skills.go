package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"sort"
	"strings"
	"syscall"
	"time"
)

// Skill is a skill (or custom command) the CLI offers in a folder. Typing
// Invoke at the start of a prompt runs it.
type Skill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	ArgHint     string `json:"argument_hint,omitempty"`
	Scope       string `json:"scope,omitempty"` // codex: user | repo | system …
	Invoke      string `json:"invoke"`          // "/name" (Claude) or "$name" (Codex)
}

// ListClaudeSkills asks Claude Code which skills and custom commands it loads
// in cwd (user, project and plugin ones; built-in commands are left out). The
// initialize handshake only: no prompt, no session file.
func ListClaudeSkills(ctx context.Context, bin, cwd string, env []string) ([]Skill, error) {
	if bin == "" {
		bin = "claude"
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose",
		"--no-session-persistence", "--permission-prompt-tool", "stdio", "--settings", `{"disableAllHooks":true}`)
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	defer func() {
		_ = stdin.Close()
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		_ = cmd.Wait()
	}()
	_, _ = stdin.Write([]byte(`{"type":"control_request","request_id":"skills","request":{"subtype":"initialize"}}` + "\n"))
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 1<<20), 32<<20)
	for sc.Scan() {
		var m struct {
			Type     string `json:"type"`
			Response struct {
				RequestID string `json:"request_id"`
				Response  struct {
					Commands []struct {
						Name         string `json:"name"`
						Description  string `json:"description"`
						ArgumentHint string `json:"argumentHint"`
						Builtin      bool   `json:"builtin"`
					} `json:"commands"`
				} `json:"response"`
			} `json:"response"`
		}
		if json.Unmarshal(sc.Bytes(), &m) != nil || m.Type != "control_response" || m.Response.RequestID != "skills" {
			continue
		}
		out := []Skill{}
		for _, c := range m.Response.Response.Commands {
			if c.Builtin || c.Name == "" || strings.HasPrefix(c.Name, "_") {
				continue
			}
			out = append(out, Skill{Name: c.Name, Description: c.Description, ArgHint: c.ArgumentHint, Invoke: "/" + c.Name})
		}
		sortSkills(out)
		return out, nil
	}
	if ctx.Err() != nil {
		return nil, errors.New("Claude Code did not answer in time")
	}
	return nil, errors.New("Claude Code did not list its skills")
}

// ListCodexSkills asks the Codex app-server for the enabled skills in cwd.
func ListCodexSkills(ctx context.Context, bin, cwd string, env []string) ([]Skill, error) {
	c, err := startRPC(bin, cwd, env)
	if err != nil {
		return nil, err
	}
	defer c.close()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := c.initialize(ctx, Version); err != nil {
		return nil, err
	}
	res, err := c.call(ctx, "skills/list", map[string]any{"cwds": []string{cwd}})
	if err != nil {
		return nil, err
	}
	var r struct {
		Data []struct {
			Skills []struct {
				Name             string `json:"name"`
				Description      string `json:"description"`
				ShortDescription string `json:"shortDescription"`
				Scope            string `json:"scope"`
				Enabled          bool   `json:"enabled"`
				Interface        struct {
					ShortDescription string `json:"shortDescription"`
				} `json:"interface"`
			} `json:"skills"`
		} `json:"data"`
	}
	if err := json.Unmarshal(res, &r); err != nil {
		return nil, err
	}
	out := []Skill{}
	seen := map[string]bool{}
	for _, d := range r.Data {
		for _, sk := range d.Skills {
			if !sk.Enabled || sk.Name == "" || seen[sk.Name] {
				continue
			}
			seen[sk.Name] = true
			desc := sk.Description
			if s := firstNonEmpty(sk.Interface.ShortDescription, sk.ShortDescription); s != "" {
				desc = s
			}
			out = append(out, Skill{Name: sk.Name, Description: desc, Scope: sk.Scope, Invoke: "$" + sk.Name})
		}
	}
	sortSkills(out)
	return out, nil
}

func sortSkills(s []Skill) {
	sort.SliceStable(s, func(i, j int) bool { return strings.ToLower(s[i].Name) < strings.ToLower(s[j].Name) })
}
