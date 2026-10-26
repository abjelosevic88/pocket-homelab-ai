package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

// messageOnce provides at-most-once dispatch, including across disconnects and
// restarts. A crash after reservation requires manual reconciliation, never replay.
func (s *Server) messageOnce(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("Idempotency-Key")
		if key == "" {
			next(w, r)
			return
		}
		if len(key) > 128 || strings.ContainsAny(key, "\r\n\x00") {
			writeErr(w, 400, "invalid message ID")
			return
		}
		if !s.requireRun(w, r) {
			return
		}
		t := s.threadAs(w, r, accessChat)
		if t == nil {
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 2*1024*1024+1))
		if err != nil || len(body) > 2*1024*1024 {
			writeErr(w, 413, "message too large")
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		var value any
		if json.Unmarshal(body, &value) != nil {
			writeErr(w, 400, "invalid json")
			return
		}
		canonical, _ := json.Marshal(value)
		sum := sha256.Sum256(canonical)
		hash := hex.EncodeToString(sum[:])
		uid := me(r)
		// Use an independent bounded context: a phone losing its response must
		// not cancel receipt persistence after the message has been accepted.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 180*time.Second)
		defer cancel()
		result, err := s.store.DB.ExecContext(ctx, `INSERT INTO message_receipts (user_id, thread_id, request_key, request_hash) VALUES (?, ?, ?, ?) ON CONFLICT DO NOTHING`, uid, t.ID, key, hash)
		if err != nil {
			s.fail(w, err, "reserve message")
			return
		}
		inserted, _ := result.RowsAffected()
		if inserted == 0 {
			var previousHash string
			var status int
			var response []byte
			err = s.store.DB.QueryRowContext(ctx, `SELECT request_hash, status, body FROM message_receipts WHERE user_id=? AND thread_id=? AND request_key=?`, uid, t.ID, key).Scan(&previousHash, &status, &response)
			if err != nil {
				s.fail(w, err, "read message receipt")
				return
			}
			if previousHash != hash {
				writeErr(w, 409, "this message ID was used for different content")
				return
			}
			if status == 0 {
				writeErr(w, 409, "delivery is still in progress or was interrupted; check the conversation before sending a new message")
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Idempotency-Replayed", "true")
			w.WriteHeader(status)
			_, _ = w.Write(response)
			return
		}
		capture := &receiptWriter{header: make(http.Header)}
		next(capture, r.WithContext(ctx))
		if capture.status == 0 {
			capture.status = 200
		}
		if _, err := s.store.DB.ExecContext(ctx, `UPDATE message_receipts SET status=?, body=? WHERE user_id=? AND thread_id=? AND request_key=?`, capture.status, capture.body.Bytes(), uid, t.ID, key); err != nil {
			s.fail(w, err, "save message receipt")
			return
		}
		for k, values := range capture.header {
			for _, v := range values {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(capture.status)
		_, _ = w.Write(capture.body.Bytes())
	}
}

type receiptWriter struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (w *receiptWriter) Header() http.Header { return w.header }
func (w *receiptWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *receiptWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = 200
	}
	return w.body.Write(b)
}
