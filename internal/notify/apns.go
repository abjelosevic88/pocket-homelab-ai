package notify

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"sync"
	"time"
)

// APNS uses Apple's HTTP/2 provider API and an ES256 signing key.
// One cached JWT is reused for 45 minutes, across both APNs environments.
type APNS struct {
	TeamID, KeyID, Topic, KeyFile string
	mu                            sync.Mutex
	jwt                           string
	issued                        time.Time
	client                        *http.Client
	endpoint                      string // test-only override
}

func (a *APNS) Configured() bool {
	return a != nil && a.TeamID != "" && a.KeyID != "" && a.Topic != "" && a.KeyFile != ""
}

func (a *APNS) providerToken() (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.jwt != "" && time.Since(a.issued) < 45*time.Minute {
		return a.jwt, nil
	}
	data, err := os.ReadFile(a.KeyFile)
	if err != nil {
		return "", fmt.Errorf("read APNs signing key: %w", err)
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return "", fmt.Errorf("invalid APNs signing key PEM")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return "", fmt.Errorf("parse APNs signing key: %w", err)
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok || key.Curve != elliptic.P256() {
		return "", fmt.Errorf("APNs signing key must be EC P-256")
	}
	header, _ := json.Marshal(map[string]string{"alg": "ES256", "kid": a.KeyID})
	now := time.Now()
	claims, _ := json.Marshal(map[string]any{"iss": a.TeamID, "iat": now.Unix()})
	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	hash := sha256.Sum256([]byte(unsigned))
	r, s, err := ecdsa.Sign(rand.Reader, key, hash[:])
	if err != nil {
		return "", err
	}
	signature := make([]byte, 64)
	r.FillBytes(signature[:32])
	s.FillBytes(signature[32:])
	a.jwt = unsigned + "." + base64.RawURLEncoding.EncodeToString(signature)
	a.issued = now
	return a.jwt, nil
}

type APNSError struct {
	Status int
	Reason string
}

func (e *APNSError) Error() string { return fmt.Sprintf("APNs HTTP %d: %s", e.Status, e.Reason) }
func (e *APNSError) InvalidDevice() bool {
	return e.Status == 410 || e.Reason == "BadDeviceToken" || e.Reason == "DeviceTokenNotForTopic"
}

func (a *APNS) Send(ctx context.Context, token, environment, title, body, server, threadID string, badge int) error {
	if !a.Configured() {
		return fmt.Errorf("native push is not configured on this server")
	}
	if environment != "sandbox" && environment != "production" {
		return fmt.Errorf("invalid APNs environment")
	}
	jwt, err := a.providerToken()
	if err != nil {
		return err
	}
	host := "https://api.push.apple.com"
	if environment == "sandbox" {
		host = "https://api.sandbox.push.apple.com"
	}
	if a.endpoint != "" {
		host = a.endpoint
	}
	// Keep the payload under Apple's 4 KB limit even with multibyte text.
	title = truncateUTF8(title, 180)
	body = truncateUTF8(body, 900)
	if badge < 0 {
		badge = 0
	}
	aps := map[string]any{"badge": badge}
	if title != "" || body != "" {
		aps["alert"] = map[string]string{"title": title, "body": body}
		aps["sound"] = "default"
		aps["thread-id"] = threadID
	}
	payloadData := map[string]any{"aps": aps, "server": server, "thread_id": threadID}
	if threadID != "" {
		link := url.URL{Scheme: "pocketai", Host: "thread", Path: "/" + threadID}
		query := url.Values{"server": {server}}
		link.RawQuery = query.Encode()
		payloadData["url"] = link.String()
	}
	payload, err := json.Marshal(payloadData)
	if err != nil {
		return err
	}
	if len(payload) > 4096 {
		return fmt.Errorf("APNs payload too large")
	}
	req, err := http.NewRequestWithContext(ctx, "POST", host+"/3/device/"+token, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("authorization", "bearer "+jwt)
	req.Header.Set("apns-topic", a.Topic)
	req.Header.Set("apns-push-type", "alert")
	req.Header.Set("apns-priority", "10")
	if title == "" && body == "" {
		req.Header.Set("apns-collapse-id", "completion-badge")
	}
	req.Header.Set("apns-expiration", strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10))
	req.Header.Set("Content-Type", "application/json")
	a.mu.Lock()
	if a.client == nil {
		a.client = &http.Client{Timeout: 15 * time.Second}
	}
	client := a.client
	a.mu.Unlock()
	response, err := client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusOK {
		return nil
	}
	var failure struct {
		Reason string `json:"reason"`
	}
	_ = json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&failure)
	return &APNSError{Status: response.StatusCode, Reason: failure.Reason}
}

func truncateUTF8(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	for limit > 0 && (text[limit]&0xc0) == 0x80 {
		limit--
	}
	return text[:limit] + "…"
}
