package sdk

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const InferenceScope = "inference:write"

// InferenceUse declares text generation without granting access to agent threads or tools.
func InferenceUse() Use {
	return Use{App: "ai", Scopes: []string{InferenceScope}, Reason: "Generate text and structured suggestions using Pocket AI"}
}

func ValidAIProvider(provider string) bool {
	return provider == "" || provider == "auto" || provider == "pocket" || provider == "openai"
}

type AIStatus struct {
	Provider string `json:"provider"`
	Selected string `json:"selected"`
	Ready    bool   `json:"ready"`
	Detail   string `json:"detail"`
}

// InferenceClient never holds an upstream key or a cached delegation token.
type InferenceClient struct {
	core  *Client
	email string
	err   error
}

// SelectAI returns nil only when direct OpenAI is selected. A Core outage is
// not evidence that a link is disabled: return an error client, never silently
// send private prompts to a different provider. Explicitly absent/disabled
// links preserve standalone behavior in automatic mode.
func (c *Client) SelectAI(ctx context.Context, email, provider string, directReady bool) (*InferenceClient, AIStatus) {
	if provider == "" {
		provider = "auto"
	}
	status := AIStatus{Provider: provider, Selected: "openai", Ready: directReady, Detail: "Using your OpenAI settings"}
	if !directReady {
		status.Detail = "Connect Pocket AI through Core or add your own OpenAI key"
	}
	failed := func(err error) (*InferenceClient, AIStatus) {
		status.Selected, status.Ready, status.Detail = "pocket", false, err.Error()
		return &InferenceClient{core: c, email: email, err: err}, status
	}
	if !ValidAIProvider(provider) {
		return failed(errors.New("choose automatic, Pocket AI or OpenAI"))
	}
	if provider == "openai" {
		return nil, status
	}
	if c == nil || !c.Configured() {
		if provider == "pocket" {
			return failed(errors.New("connect this app to Pocket Core to use Pocket AI"))
		}
		return nil, status
	}
	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	links, err := c.LinkStatus(checkCtx, email, "ai")
	if err != nil {
		return failed(fmt.Errorf("could not check the Pocket AI connection: %w", err))
	}
	for _, link := range links {
		if link.Scope == InferenceScope && link.On {
			status.Selected, status.Ready, status.Detail = "pocket", true, "Using Pocket AI through your enabled Core link"
			return &InferenceClient{core: c, email: email}, status
		}
	}
	if provider == "pocket" {
		return failed(errors.New("enable this app's Pocket AI text-generation link in Pocket Core"))
	}
	return nil, status
}

type InferenceRequest struct {
	System string `json:"system"`
	Prompt string `json:"prompt"`
	JSON   bool   `json:"json"`
}

type InferenceReply struct {
	Text         string `json:"text"`
	Model        string `json:"model"`
	InputTokens  int    `json:"input_tokens"`
	OutputTokens int    `json:"output_tokens"`
}

func (c *InferenceClient) Complete(ctx context.Context, system, prompt string, jsonMode bool) (InferenceReply, error) {
	var out InferenceReply
	if c.err != nil {
		return out, c.err
	}
	ctx, cancel := context.WithTimeout(ctx, 150*time.Second)
	defer cancel()
	// Recheck grants for every invocation, including background jobs.
	token, err := c.core.ActAs(ctx, c.email, "ai", InferenceScope)
	if err != nil {
		return out, err
	}
	raw, err := json.Marshal(InferenceRequest{System: system, Prompt: prompt, JSON: jsonMode})
	if err != nil {
		return out, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(token.URL, "/")+"/api/v1/inference", bytes.NewReader(raw))
	if err != nil {
		return out, err
	}
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 150 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		return out, fmt.Errorf("Pocket AI: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&e)
		return out, fmt.Errorf("Pocket AI HTTP %d: %s", res.StatusCode, e.Error)
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(&out); err != nil {
		return out, err
	}
	if strings.TrimSpace(out.Text) == "" {
		return out, errors.New("Pocket AI returned no answer")
	}
	return out, nil
}
