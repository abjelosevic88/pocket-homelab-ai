// Package notify sends a short message through Apprise, ntfy or a webhook.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

var client = &http.Client{Timeout: 15 * time.Second}

// Send delivers title/body (and an optional link) to url using kind
// (apprise | ntfy | webhook). token is ntfy's bearer token or Apprise's.
func Send(ctx context.Context, kind, url, token, title, body, link string) error {
	url = strings.TrimSpace(url)
	if url == "" {
		return fmt.Errorf("no notification address configured")
	}
	var req *http.Request
	var err error
	switch kind {
	case "ntfy":
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Title", title)
		req.Header.Set("Tags", "white_check_mark")
		if link != "" {
			req.Header.Set("Click", link)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	case "webhook":
		b, _ := json.Marshal(map[string]string{"title": title, "body": body, "url": link})
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	default: // apprise
		msg := body
		if link != "" {
			msg += "\n" + link
		}
		b, _ := json.Marshal(map[string]string{"title": title, "body": msg, "type": "info", "format": "text"})
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(res.Body, 300))
		return fmt.Errorf("HTTP %d: %s", res.StatusCode, strings.TrimSpace(string(msg)))
	}
	return nil
}
