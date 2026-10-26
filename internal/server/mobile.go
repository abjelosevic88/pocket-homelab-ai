package server

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
	"net/http"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/notify"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
	"github.com/go-chi/chi/v5"
)

func (s *Server) mobileRoutes(r chi.Router) {
	r.Get("/mobile", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]bool{"idempotency": true, "push": s.apns.Configured() && s.cfg.BaseURL != ""})
	})
	r.Get("/mobile/attention", s.handleAttention)
	r.Put("/mobile/devices", s.handleMobileDevice)
	r.Delete("/mobile/devices/{device}", s.handleDeleteMobileDevice)
	r.Post("/mobile/push/test", s.handleMobilePushTest)
}

type mobileDevice struct{ ID, Token, Environment string }

// Native registrations are tied to a revocable local API token, not browser
// sessions. Revoking that token deletes its devices through the foreign key.
func (s *Server) mobileTokenID(r *http.Request) (int64, error) {
	authorization := r.Header.Get("Authorization")
	var id int64
	if len(authorization) < 8 || !strings.EqualFold(authorization[:7], "bearer ") {
		return 0, errors.New("API token required")
	}
	err := s.store.DB.QueryRowContext(r.Context(), `SELECT id FROM api_tokens WHERE token_hash=? AND user_id=?`, store.HashToken(strings.TrimSpace(authorization[7:])), me(r)).Scan(&id)
	return id, err
}

func (s *Server) handleMobileDevice(w http.ResponseWriter, r *http.Request) {
	var in struct {
		DeviceID    string `json:"device_id"`
		Token       string `json:"token"`
		Environment string `json:"environment"`
	}
	if decode(r, &in) != nil || len(in.DeviceID) < 8 || len(in.DeviceID) > 128 || len(in.Token) < 32 || len(in.Token) > 512 || (in.Environment != "sandbox" && in.Environment != "production") {
		writeErr(w, 400, "invalid device registration")
		return
	}
	if _, err := hex.DecodeString(in.Token); err != nil {
		writeErr(w, 400, "invalid device token")
		return
	}
	tokenID, err := s.mobileTokenID(r)
	if err != nil {
		writeErr(w, 403, "register using this device's API token")
		return
	}
	in.Token = strings.ToLower(in.Token)
	// Enforce a finite device count; token rotation of an existing device is free.
	var count int
	if err := s.store.DB.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM mobile_devices WHERE user_id=? AND device_id<>?`, me(r), in.DeviceID).Scan(&count); err != nil {
		s.fail(w, err, "count devices")
		return
	}
	if count >= 20 {
		writeErr(w, 409, "too many registered devices")
		return
	}
	tx, err := s.store.DB.BeginTx(r.Context(), nil)
	if err != nil {
		s.fail(w, err, "register device")
		return
	}
	defer tx.Rollback()
	// A phone can change accounts. Remove the old token's registration to avoid
	// exposing the previous person's notifications to its new signed-in user.
	if _, err = tx.ExecContext(r.Context(), `DELETE FROM mobile_devices WHERE token=? AND environment=?`, in.Token, in.Environment); err == nil {
		_, err = tx.ExecContext(r.Context(), `INSERT INTO mobile_devices (user_id, device_id, token_id, token, environment) VALUES (?, ?, ?, ?, ?)
            ON CONFLICT(user_id, device_id) DO UPDATE SET token_id=excluded.token_id, token=excluded.token, environment=excluded.environment, updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')`, me(r), in.DeviceID, tokenID, in.Token, in.Environment)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		s.fail(w, err, "register device")
		return
	}
	w.WriteHeader(204)
}

func (s *Server) handleDeleteMobileDevice(w http.ResponseWriter, r *http.Request) {
	_, err := s.store.DB.ExecContext(r.Context(), `DELETE FROM mobile_devices WHERE user_id=? AND device_id=?`, me(r), chi.URLParam(r, "device"))
	if err != nil {
		s.fail(w, err, "remove device")
		return
	}
	w.WriteHeader(204)
}

func (s *Server) mobileDevices(ctx context.Context, uid int64) ([]mobileDevice, error) {
	rows, err := s.store.DB.QueryContext(ctx, `SELECT device_id, token, environment FROM mobile_devices WHERE user_id=? ORDER BY updated_at DESC LIMIT 20`, uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	devices := []mobileDevice{}
	for rows.Next() {
		var d mobileDevice
		if err := rows.Scan(&d.ID, &d.Token, &d.Environment); err != nil {
			return nil, err
		}
		devices = append(devices, d)
	}
	return devices, rows.Err()
}

func (s *Server) sendMobilePush(ctx context.Context, uid int64, d mobileDevice, title, body, threadID string) error {
	err := s.apns.Send(ctx, d.Token, d.Environment, title, body, s.cfg.BaseURL, threadID)
	var failure *notify.APNSError
	if errors.As(err, &failure) && failure.InvalidDevice() {
		_, _ = s.store.DB.ExecContext(ctx, `DELETE FROM mobile_devices WHERE user_id=? AND device_id=? AND token=? AND environment=?`, uid, d.ID, d.Token, d.Environment)
	}
	return err
}

func (s *Server) notifyMobile(uid int64, title, body, threadID string) {
	if !s.apns.Configured() {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		devices, err := s.mobileDevices(ctx, uid)
		if err != nil {
			s.log.Warn("load native push devices", "err", err)
			return
		}
		for _, d := range devices {
			if err := s.sendMobilePush(ctx, uid, d, title, body, threadID); err != nil {
				s.log.Warn("native push failed", "user", uid, "err", err)
			}
		}
	}()
}

func (s *Server) handleMobilePushTest(w http.ResponseWriter, r *http.Request) {
	if !s.apns.Configured() {
		writeErr(w, 503, "native push is not configured on this server")
		return
	}
	var in struct {
		DeviceID string `json:"device_id"`
	}
	if decode(r, &in) != nil || in.DeviceID == "" {
		writeErr(w, 400, "select a device")
		return
	}
	devices, err := s.mobileDevices(r.Context(), me(r))
	if err != nil {
		s.fail(w, err, "load devices")
		return
	}
	for _, d := range devices {
		if d.ID != in.DeviceID {
			continue
		}
		if err := s.sendMobilePush(r.Context(), me(r), d, "Pocket AI", "Native notifications are ready.", ""); err != nil {
			s.fail(w, err, "send native test")
			return
		}
		w.WriteHeader(204)
		return
	}
	writeErr(w, 404, "this device is not registered")
}

// The inbox returns only pending requests, not full conversation histories.
func (s *Server) handleAttention(w http.ResponseWriter, r *http.Request) {
	threads, err := s.store.ListThreads(r.Context(), me(r), store.ThreadFilter{})
	if err != nil {
		s.fail(w, err, "load attention inbox")
		return
	}
	threads = append(threads, s.sharedThreads(r.Context(), me(r))...)
	type attention struct {
		Thread   *store.Thread    `json:"thread"`
		Requests []*agent.Request `json:"requests"`
	}
	result := []attention{}
	for _, t := range threads {
		s.rt.decorate(t)
		if t.Status != "waiting" && t.Status != "error" && t.Pending == 0 {
			continue
		}
		rows, err := s.store.ListItems(r.Context(), t.ID)
		if err != nil {
			s.fail(w, err, "load pending requests")
			return
		}
		entry := attention{Thread: t, Requests: []*agent.Request{}}
		live := s.rt.liveItems(t.ID)
		for _, row := range rows {
			var item agent.Item
			if current, ok := live[row.Key]; ok {
				item = current
			} else {
				_ = json.Unmarshal(row.Data, &item)
			}
			if item.Request != nil && item.Request.Status == "pending" {
				entry.Requests = append(entry.Requests, item.Request)
			}
		}
		result = append(result, entry)
	}
	writeJSON(w, 200, result)
}
