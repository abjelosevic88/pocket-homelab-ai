package notify

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAPNSHTTP2PayloadJWTAndErrors(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	path := filepath.Join(t.TempDir(), "AuthKey.p8")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("ab", 32)
	previousJWT := ""
	sendCount := 0
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sendCount++
		if r.ProtoMajor != 2 || r.Method != "POST" || r.URL.Path != "/3/device/"+token || r.Header.Get("apns-topic") != "org.bjelke.pocketai" || r.Header.Get("apns-push-type") != "alert" {
			t.Error("wrong APNs request")
		}
		jwt := strings.TrimPrefix(r.Header.Get("Authorization"), "bearer ")
		parts := strings.Split(jwt, ".")
		if len(parts) != 3 {
			t.Error("invalid JWT")
			return
		}
		var header struct{ Alg, Kid string }
		var claims struct {
			Iss string
			Iat int64
		}
		head, _ := base64.RawURLEncoding.DecodeString(parts[0])
		_ = json.Unmarshal(head, &header)
		data, _ := base64.RawURLEncoding.DecodeString(parts[1])
		_ = json.Unmarshal(data, &claims)
		if header.Alg != "ES256" || header.Kid != "KEY" || claims.Iss != "TEAM" || time.Since(time.Unix(claims.Iat, 0)) > time.Minute {
			t.Error("wrong JWT claims")
		}
		sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
		sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
		if len(sig) != 64 || !ecdsa.Verify(&key.PublicKey, sum[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
			t.Error("invalid signature")
		}
		if previousJWT != "" && previousJWT != jwt {
			t.Error("JWT was needlessly regenerated")
		}
		previousJWT = jwt
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if sendCount == 3 {
			aps := payload["aps"].(map[string]any)
			if aps["badge"] != float64(0) || aps["alert"] != nil || aps["sound"] != nil || payload["url"] != nil || r.Header.Get("apns-collapse-id") != "completion-badge" {
				t.Error("badge reset must be silent and must explicitly include zero")
			}
			w.WriteHeader(200)
			return
		}
		if payload["thread_id"] != "thread-1" || payload["server"] != "https://ai.example" {
			t.Error("missing deep-link destination")
		}
		aps := payload["aps"].(map[string]any)
		if aps["badge"] != float64(3) || payload["url"] != "pocketai://thread/thread-1?server=https%3A%2F%2Fai.example" {
			t.Error("missing badge or app link")
		}
		if sendCount == 2 {
			w.WriteHeader(410)
			_, _ = w.Write([]byte(`{"reason":"Unregistered"}`))
			return
		}
		w.WriteHeader(200)
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()
	apns := &APNS{TeamID: "TEAM", KeyID: "KEY", Topic: "org.bjelke.pocketai", KeyFile: path, endpoint: server.URL, client: server.Client()}
	if err := apns.Send(context.Background(), token, "sandbox", "Done", "Finished", "https://ai.example", "thread-1", 3); err != nil {
		t.Fatal(err)
	}
	err := apns.Send(context.Background(), token, "sandbox", "Done", "Finished", "https://ai.example", "thread-1", 3)
	var failure *APNSError
	if !errors.As(err, &failure) || !failure.InvalidDevice() {
		t.Fatalf("invalid token not identified: %v", err)
	}
	if err := apns.Send(context.Background(), token, "sandbox", "", "", "https://ai.example", "", 0); err != nil {
		t.Fatal(err)
	}
	if err := apns.Send(context.Background(), token, "other", "Done", "body", "server", "thread-1", 3); err == nil {
		t.Fatal("invalid environment accepted")
	}
}
