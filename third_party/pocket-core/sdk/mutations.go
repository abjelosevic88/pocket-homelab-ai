package sdk

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// CommitHook runs after domain writes, while their transaction is still open.
// Hooks must use this transaction for SQL; querying the host DB can deadlock a
// single-connection SQLite store. An enqueue error aborts the domain mutation.
type CommitHook = func(*sql.Tx) error

func Commit(tx *sql.Tx, hooks ...CommitHook) error {
	for _, hook := range hooks {
		if hook != nil {
			if err := hook(tx); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

var ErrOperationConflict = errors.New("idempotency key was already used for a different request")

// Operation stores a replayable response in the same transaction as the write.
// Scope must include the authenticated person, calling app, and endpoint/resource.
// Receivers must recheck current access before returning a saved response.
type Operation struct{ key, hash string }

func NewOperation(scope, key string, request any) (*Operation, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, nil
	}
	if scope == "" || len(key) > 200 || strings.ContainsAny(key, "\r\n\x00") {
		return nil, errors.New("invalid idempotency key")
	}
	b, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(b)
	compound, _ := json.Marshal([]string{scope, key})
	return &Operation{key: string(compound), hash: hex.EncodeToString(hash[:])}, nil
}

func (o *Operation) Replay(ctx context.Context, tx interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, response any) (bool, error) {
	if o == nil {
		return false, nil
	}
	var hash string
	var body []byte
	err := tx.QueryRowContext(ctx, `SELECT request_hash,response FROM pocket_operations WHERE operation_key=?`, o.key).Scan(&hash, &body)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if hash != o.hash {
		return false, ErrOperationConflict
	}
	if err = json.Unmarshal(body, response); err != nil {
		return false, fmt.Errorf("read operation receipt: %w", err)
	}
	return true, nil
}

func (o *Operation) Save(ctx context.Context, tx *sql.Tx, response any) error {
	if o == nil {
		return nil
	}
	body, err := json.Marshal(response)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO pocket_operations(operation_key,request_hash,response) VALUES(?,?,?)`, o.key, o.hash, body)
	return err
}
