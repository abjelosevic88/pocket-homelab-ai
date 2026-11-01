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

// The first repair (cost_repair.go) had no session file for conversations
// on other machines, so it went by their running totals alone. This one
// fetches those files' saved totals over ssh and works those conversations
// out again from the costs as first recorded (the first repair's backup),
// one machine at a time: a machine out of reach is tried at the next start.
const costRepairRemoteKey = "cost_repair_v2"

type costRepairRemote struct {
	HostsDone []int64  `json:"hosts_done"`
	Turns     int      `json:"turns"`
	Before    float64  `json:"before_usd"` // those conversations' costs before this repair
	After     float64  `json:"after_usd"`
	Backups   []string `json:"backups,omitempty"`
	At        string   `json:"at,omitempty"`
}

func (s *Server) repairRemoteCostsOnce(ctx context.Context) {
	var prog costRepairRemote
	if v := s.store.BoxSetting(ctx, costRepairRemoteKey); v != "" {
		_ = json.Unmarshal([]byte(v), &prog)
	}
	done := map[int64]bool{}
	for _, id := range prog.HostsDone {
		done[id] = true
	}
	type th struct{ id, session, home string }
	byHost := map[int64][]th{}
	rows, err := s.store.DB.QueryContext(ctx, `SELECT id, provider_session_id, host_id, host_home FROM threads
		WHERE provider = 'claude' AND host_id != 0 AND provider_session_id != '' AND EXISTS (SELECT 1 FROM turns WHERE thread_id = threads.id)`)
	if err != nil {
		s.log.Error("repairing remote Claude turn costs", "err", err)
		return
	}
	for rows.Next() {
		var t th
		var host int64
		if rows.Scan(&t.id, &t.session, &host, &t.home) == nil && !done[host] {
			byHost[host] = append(byHost[host], t)
		}
	}
	rows.Close()
	if len(byHost) == 0 {
		if prog.At == "" && len(prog.HostsDone) == 0 {
			prog.At = store.Now()
			b, _ := json.Marshal(prog)
			_ = s.store.SetBoxSetting(ctx, costRepairRemoteKey, string(b))
		}
		return
	}

	// the costs as first recorded: the first repair's backup, when it changed any
	orig := map[int64]float64{}
	var first struct {
		Backup string `json:"backup"`
		At     string `json:"at"`
	}
	_ = json.Unmarshal([]byte(s.store.BoxSetting(ctx, costRepairKey)), &first)
	if first.Backup != "" {
		if _, err := os.Stat(first.Backup); err != nil {
			s.log.Warn("repairing remote Claude turn costs: the first repair's backup is gone, keeping its costs", "backup", first.Backup)
			return
		}
		db, err := sql.Open("sqlite", "file:"+first.Backup+"?mode=ro")
		if err != nil {
			return
		}
		r, err := db.QueryContext(ctx, `SELECT id, cost_usd FROM turns WHERE provider = 'claude'`)
		if err == nil {
			for r.Next() {
				var id int64
				var c float64
				if r.Scan(&id, &c) == nil {
					orig[id] = c
				}
			}
			r.Close()
		}
		db.Close()
	}

	// every machine at once, each in one ssh call
	type fetched struct {
		host int64
		res  map[string]*agent.RemoteRestores
		ok   bool
	}
	var mu sync.Mutex
	var got []fetched
	var wg sync.WaitGroup
	for host, ths := range byHost {
		h, err := s.host(ctx, host) // reached as its agents are: with its person's own key
		if err != nil { // the machine was removed: nothing to fetch, ever
			got = append(got, fetched{host: host, ok: true})
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
			f := fetched{host: host}
			if err != nil {
				s.log.Info("repairing remote Claude turn costs: machine not reached, trying at the next start", "host", h.Name, "err", sshError(errOut, err))
			} else {
				f.res, f.ok = agent.ParseRemoteRestores(out), true
			}
			mu.Lock()
			got = append(got, f)
			mu.Unlock()
		}(host, h)
	}
	wg.Wait()

	var fixes []costFix
	before, after := 0.0, 0.0
	for _, f := range got {
		if !f.ok {
			continue
		}
		prog.HostsDone = append(prog.HostsDone, f.host)
		for _, t := range byHost[f.host] {
			rr := f.res[t.session]
			if rr == nil {
				continue // its file is not there (any more): the first repair stands
			}
			turns, cur, err := s.originalTurns(ctx, t.id, orig, first.Backup != "")
			if err != nil || len(turns) == 0 {
				continue
			}
			repairSession(turns, rr.Restores, rr.End)
			for _, x := range turns {
				before += cur[x.id]
				after += x.new
				if math.Abs(x.new-cur[x.id]) > 1e-9 {
					fixes = append(fixes, costFix{id: x.id, thread: x.thread, cur: cur[x.id], new: x.new})
				}
			}
		}
	}
	prog.Turns += len(fixes)
	prog.Before += before
	prog.After += after
	prog.At = store.Now()
	if len(fixes) == 0 {
		b, _ := json.Marshal(prog)
		_ = s.store.SetBoxSetting(ctx, costRepairRemoteKey, string(b))
		return
	}
	// the backup lands in prog after the fact: save it once more with it
	backup, err := applyCostFixes(ctx, s.cfg, s.store, "costs-remote", fixes, costRepairRemoteKey, prog)
	if err != nil {
		s.log.Error("repairing remote Claude turn costs", "err", err)
		return
	}
	prog.Backups = append(prog.Backups, backup)
	b, _ := json.Marshal(prog)
	_ = s.store.SetBoxSetting(ctx, costRepairRemoteKey, string(b))
	s.log.Info("repaired remote Claude turn costs from their session files", "turns", len(fixes),
		"before_usd", fmt.Sprintf("%.2f", before), "after_usd", fmt.Sprintf("%.2f", after), "backup", backup)
}

// originalTurns is a conversation's Claude turns with their costs as first
// recorded (old) and as they are now (cur). With a first repair, only the
// turns it saw: later ones were counted right from the start.
func (s *Server) originalTurns(ctx context.Context, thread string, orig map[int64]float64, fromBackup bool) ([]repairTurn, map[int64]float64, error) {
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
		if rows.Scan(&t.id, &at, &c) != nil {
			continue
		}
		t.thread = thread
		t.at, _ = time.Parse(time.RFC3339Nano, at)
		t.old = c
		if fromBackup {
			o, ok := orig[t.id]
			if !ok {
				break
			}
			t.old = o
		}
		cur[t.id] = c
		out = append(out, t)
	}
	return out, cur, rows.Err()
}
