package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sync"
	"time"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

// The full repair, after the first (cost_repair.go): that one had no
// session file for conversations on other machines, and turns a held agent
// replayed when it was taken back were recorded again (the same token
// counts, runtime TurnDone). This one drops those copies, then works every
// Claude conversation out again from its costs as first recorded (the first
// repair's backup) and its session file's saved totals: here from disk,
// over there over ssh with the key its agents use. A machine out of reach
// is tried again at the next start.
const costRepairFullKey = "cost_repair_v3"

type costRepairFull struct {
	LocalDone bool     `json:"local_done"`
	HostsDone []int64  `json:"hosts_done"`
	Dropped   int      `json:"dropped"` // replayed copies removed
	Turns     int      `json:"turns"`   // turns whose cost changed
	Before    float64  `json:"before_usd"`
	After     float64  `json:"after_usd"`
	Backups   []string `json:"backups,omitempty"`
	At        string   `json:"at,omitempty"`
}

type tokens struct{ in, out, cached int64 }

func (s *Server) repairAllCostsOnce(ctx context.Context) {
	var prog costRepairFull
	if v := s.store.BoxSetting(ctx, costRepairFullKey); v != "" {
		_ = json.Unmarshal([]byte(v), &prog)
	}
	hostDone := map[int64]bool{}
	for _, id := range prog.HostsDone {
		hostDone[id] = true
	}

	// replayed copies: a later turn of a thread with the same token counts
	var fixes []costFix
	seen := map[string]bool{}
	dropped := map[int64]bool{}
	rows, err := s.store.DB.QueryContext(ctx, `SELECT id, thread_id, input_tokens, output_tokens, cached_tokens, cost_usd FROM turns WHERE input_tokens > 0 ORDER BY id`)
	if err != nil {
		s.log.Error("repairing turn costs", "err", err)
		return
	}
	for rows.Next() {
		var f costFix
		if rows.Scan(&f.id, &f.thread, &f.tok.in, &f.tok.out, &f.tok.cached, &f.cur) != nil {
			continue
		}
		k := fmt.Sprintf("%s/%d/%d/%d", f.thread, f.tok.in, f.tok.out, f.tok.cached)
		if seen[k] {
			f.drop = true
			fixes = append(fixes, f)
			dropped[f.id] = true
		}
		seen[k] = true
	}
	rows.Close()

	type th struct {
		id, session, home string
		account           int64
	}
	byHost := map[int64][]th{}
	rows, err = s.store.DB.QueryContext(ctx, `SELECT id, provider_session_id, host_id, host_home, account_id FROM threads
		WHERE provider = 'claude' AND provider_session_id != '' AND EXISTS (SELECT 1 FROM turns WHERE thread_id = threads.id)`)
	if err != nil {
		s.log.Error("repairing turn costs", "err", err)
		return
	}
	for rows.Next() {
		var t th
		var host int64
		if rows.Scan(&t.id, &t.session, &host, &t.home, &t.account) != nil {
			continue
		}
		if (host == 0 && !prog.LocalDone) || (host != 0 && !hostDone[host]) {
			byHost[host] = append(byHost[host], t)
		}
	}
	rows.Close()

	orig, fromBackup, ok := s.firstRecordedCosts(ctx)
	if !ok {
		return
	}

	// saved totals: from disk here, every other machine at once in one ssh call each
	restores := map[int64]map[string]*agent.RemoteRestores{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for host, ths := range byHost {
		if host == 0 {
			res := map[string]*agent.RemoteRestores{}
			for _, t := range ths {
				dir, _ := s.accountHome(ctx, "claude", t.account)
				if path, err := agent.FindClaude(dir, t.session); err == nil {
					if rs, end, err := agent.ClaudeRestores(path); err == nil {
						res[t.session] = &agent.RemoteRestores{Restores: rs, End: end}
					}
				}
			}
			restores[0] = res
			continue
		}
		h, err := s.host(ctx, host) // reached as its agents are: with its person's own key
		if err != nil {             // the machine was removed: nothing to fetch, ever
			restores[host] = map[string]*agent.RemoteRestores{}
			continue
		}
		args := make([]string, 0, 2*len(ths))
		for _, t := range ths {
			args = append(args, t.home, t.session)
		}
		wg.Add(1)
		go func(host int64, h *store.Host) {
			defer wg.Done()
			out, errOut, err := remoteOf(h).Run(ctx, 45*time.Second, agent.RemoteClaudeRestoresSh, args...)
			if err != nil {
				s.log.Info("repairing turn costs: machine not reached, trying at the next start", "host", h.Name, "err", sshError(errOut, err))
				return
			}
			mu.Lock()
			restores[host] = agent.ParseRemoteRestores(out)
			mu.Unlock()
		}(host, h)
	}
	wg.Wait()

	changed := 0
	before, after := 0.0, 0.0
	for host, ths := range byHost {
		res, reached := restores[host]
		if !reached {
			continue
		}
		if host == 0 {
			prog.LocalDone = true
		} else {
			prog.HostsDone = append(prog.HostsDone, host)
		}
		for _, t := range ths {
			rr := res[t.session]
			if rr == nil {
				continue // no session file (any more): the first repair stands
			}
			turns, cur, err := s.originalTurns(ctx, t.id, orig, fromBackup, dropped)
			if err != nil || len(turns) == 0 {
				continue
			}
			repairSession(turns, rr.Restores, rr.End)
			for _, x := range turns {
				before += cur[x.id]
				after += x.new
				if math.Abs(x.new-cur[x.id]) > 1e-9 {
					fixes = append(fixes, costFix{id: x.id, thread: x.thread, cur: cur[x.id], new: x.new})
					changed++
				}
			}
		}
	}
	prog.Dropped += len(dropped)
	prog.Turns += changed
	prog.Before += before
	prog.After += after
	prog.At = store.Now()
	if len(fixes) > 0 {
		backup, err := applyCostFixes(ctx, s.cfg, s.store, "costs-full", fixes, "", nil)
		if err != nil {
			s.log.Error("repairing turn costs", "err", err)
			return
		}
		prog.Backups = append(prog.Backups, backup)
		s.log.Info("repaired turn costs: replayed copies dropped, conversations worked out from their session files",
			"dropped", len(dropped), "turns", changed, "before_usd", fmt.Sprintf("%.2f", before), "after_usd", fmt.Sprintf("%.2f", after), "backup", backup)
	}
	b, _ := json.Marshal(prog)
	_ = s.store.SetBoxSetting(ctx, costRepairFullKey, string(b))
}

// firstRecordedCosts is the Claude turn costs the first repair started from
// (its backup), when it changed any; fromBackup false: the costs now are.
func (s *Server) firstRecordedCosts(ctx context.Context) (map[int64]float64, bool, bool) {
	orig := map[int64]float64{}
	var first struct {
		Backup string `json:"backup"`
	}
	_ = json.Unmarshal([]byte(s.store.BoxSetting(ctx, costRepairKey)), &first)
	if first.Backup == "" {
		return orig, false, true
	}
	if _, err := os.Stat(first.Backup); err != nil {
		s.log.Warn("repairing turn costs: the first repair's backup is gone, keeping the costs as they are", "backup", first.Backup)
		return nil, false, false
	}
	db, err := sql.Open("sqlite", "file:"+first.Backup+"?mode=ro")
	if err != nil {
		return nil, false, false
	}
	defer db.Close()
	r, err := db.QueryContext(ctx, `SELECT id, cost_usd FROM turns WHERE provider = 'claude'`)
	if err != nil {
		return nil, false, false
	}
	defer r.Close()
	for r.Next() {
		var id int64
		var c float64
		if r.Scan(&id, &c) == nil {
			orig[id] = c
		}
	}
	return orig, true, true
}

// originalTurns is a conversation's Claude turns, replayed copies left out,
// with their costs as first recorded (old: the first repair's backup, or as
// recorded since) and as they are now (cur).
func (s *Server) originalTurns(ctx context.Context, thread string, orig map[int64]float64, fromBackup bool, dropped map[int64]bool) ([]repairTurn, map[int64]float64, error) {
	rows, err := s.store.DB.QueryContext(ctx, `SELECT id, created_at, cost_usd FROM turns WHERE thread_id = ? AND provider = 'claude' ORDER BY id`, thread)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var out []repairTurn
	cur := map[int64]float64{}
	for rows.Next() {
		var t repairTurn
		var at string
		var c float64
		if rows.Scan(&t.id, &at, &c) != nil || dropped[t.id] {
			continue
		}
		t.thread = thread
		t.at, _ = time.Parse(time.RFC3339Nano, at)
		t.old = c
		if o, ok := orig[t.id]; ok && fromBackup {
			t.old = o
		}
		cur[t.id] = c
		out = append(out, t)
	}
	return out, cur, rows.Err()
}
