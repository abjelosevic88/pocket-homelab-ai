package server

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// The passwords and OAuth tokens of external accounts are sealed with
// AES-256-GCM under DATA_DIR/secret.key (32 random bytes, made on first
// start). Losing the key means typing the passwords again; it is never
// returned by the API and never stored in the database.

var secretKeys sync.Map // *Server → []byte

const sealPrefix = "v1:"

func (s *Server) loadSecretKey() error {
	dir := s.cfg.DataDir
	if dir == "" {
		dir = "."
	}
	p := filepath.Join(dir, "secret.key")
	b, err := os.ReadFile(p)
	switch {
	case err == nil:
		if len(b) != 32 {
			return fmt.Errorf("%s must hold exactly 32 bytes (it has %d); move it away to make a new one (stored account passwords are lost then)", p, len(b))
		}
	case errors.Is(err, os.ErrNotExist):
		b = make([]byte, 32)
		if _, err := io.ReadFull(rand.Reader, b); err != nil {
			return err
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, b, 0o600); err != nil {
			return fmt.Errorf("write %s: %w", p, err)
		}
		s.log.Info("created the key that seals account passwords", "path", p)
	default:
		return err
	}
	secretKeys.Store(s, b)
	return nil
}

func (s *Server) secretKey() ([]byte, error) {
	if v, ok := secretKeys.Load(s); ok {
		return v.([]byte), nil
	}
	return nil, errors.New("the secret key is not loaded")
}

// seal encrypts a secret for the database ("" stays "").
func (s *Server) seal(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	key, err := s.secretKey()
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	out := gcm.Seal(nonce, nonce, []byte(plain), nil)
	return sealPrefix + base64.StdEncoding.EncodeToString(out), nil
}

// open decrypts what seal made ("" stays "").
func (s *Server) open(sealed string) (string, error) {
	if sealed == "" {
		return "", nil
	}
	if !strings.HasPrefix(sealed, sealPrefix) {
		return "", errors.New("the stored secret is not sealed")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(sealed, sealPrefix))
	if err != nil {
		return "", err
	}
	key, err := s.secretKey()
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", errors.New("the stored secret is damaged")
	}
	plain, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], nil)
	if err != nil {
		return "", errors.New("the stored secret cannot be opened (was DATA_DIR/secret.key replaced?) — enter the password again")
	}
	return string(plain), nil
}

// sign makes an HMAC of a value with the secret key (OAuth state cookie).
func (s *Server) sign(v string) string {
	key, err := s.secretKey()
	if err != nil {
		return ""
	}
	m := hmac.New(sha256.New, key)
	m.Write([]byte("pocket-ai:" + v))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}
