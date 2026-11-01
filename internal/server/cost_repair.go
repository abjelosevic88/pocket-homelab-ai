package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/config"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

// Before 0.48.0 every new Claude process counted the session's cost from
// zero, while Claude Code restores the total it had saved (runtime
// costBase): the first turn after a resume, a fork or a restart was charged
// everything the conversation had cost before. The repair works out each
// turn's own share again, once, at start (the setting below remembers it).
const costRepairKey = "cost_repair_v1"

// CostRepairReport is what the repair changes (or would).
type CostRepairReport struct {
	Turns   int     `json:"turns"`   // turns whose cost changes
	Threads int     `json:"threads"` // conversations they are in
	Before  float64 `json:"before_usd"`
	After   float64 `json:"after_usd"`
	Checked int     `json:"checked"` // conversations checked against their session file
	Backup  string  `json:"backup,omitempty"`
	At      string  `json:"at,omitempty"`
	Days    []CostRepairDay
}

// CostRepairDay is one day's Claude cost before and after.
type CostRepairDay struct {
	Date          string
	Before, After float64
}

type repairTurn struct {
	id       int64
	thread   string
	at       time.Time
	old, new float64
}

// repairSession recomputes the turns of one conversation, in order. What
// Claude Code reported was the session's running total minus what this
// process had counted, so a turn that started counting again (a resume, a
// fork, a held process taken back) shows up as at least the running total
// so far. The session file says which total each process began from and,
// once it ended, what it spent: within such a process a turn is only taken
// for a restart while its turns add up to more than that.
func repairSession(turns []repairTurn, restores []agent.Restore, end *float64) {
	tol := func(x float64) float64 { return math.Max(0.01, 0.005*x) }
	proc := func(t time.Time) int { // the last process begun by t (-1: the first)
		k := -1
		for i, x := range restores {
			if !x.At.After(t) {
				k = i
			}
		}
		return k
	}
	running := 0.0
	for i := 0; i < len(turns); {
		k := proc(turns[i].at)
		j := i // this process's turns: [i, j)
		for j < len(turns) && proc(turns[j].at) == k {
			j++
		}
		base := 0.0
		if k >= 0 {
			base = restores[k].USD
		}
		budget := math.Inf(1) // still running, or never saved: unknown
		if k+1 < len(restores) {
			budget = restores[k+1].USD - base
		} else if end != nil {
			budget = *end - base
		}
		left := 0.0
		for _, t := range turns[i:j] {
			left += t.old
		}
		spent := 0.0
		for ; i < j; i++ {
			r := turns[i].old
			over := math.IsInf(budget, 1) || left-(budget-spent) > tol(budget)
			left -= r
			d := r
			switch {
			case !over:
			case running > 0.01 && r >= running-tol(running):
				d = r - running
			case base > 0.01 && r >= base-tol(base):
				d, running = r-base, base
			}
			d = math.Max(d, 0)
			running += d
			spent += d
			turns[i].new = d
		}
	}
}

// RepairCosts works out the repair (apply false: changes nothing) or
// applies it after a backup next to the database.
func RepairCosts(ctx context.Context, cfg *config.Config, st *store.Store, apply bool) (*CostRepairReport, error) {
	homes := map[int64]string{0: cfg.ClaudeHome}
	if list, err := st.ListAccounts(ctx); err == nil {
		for _, a := range list {
			if a.Provider == "claude" {
				homes[a.ID] = a.Home
			}
		}
	}
	type th struct {
		session string
		account int64
		host    int64
	}
	threads := map[string]th{}
	rows, err := st.DB.QueryContext(ctx, `SELECT id, provider_session_id, account_id, host_id FROM threads WHERE provider = 'claude'`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		var t th
		if rows.Scan(&id, &t.session, &t.account, &t.host) == nil {
			threads[id] = t
		}
	}
	rows.Close()
	rows, err = st.DB.QueryContext(ctx, `SELECT id, thread_id, created_at, cost_usd FROM turns WHERE provider = 'claude' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	byThread := map[string][]repairTurn{}
	var order []string
	for rows.Next() {
		var t repairTurn
		var at string
		if rows.Scan(&t.id, &t.thread, &at, &t.old) != nil {
			continue
		}
		t.at, _ = time.Parse(time.RFC3339Nano, at)
		if _, ok := byThread[t.thread]; !ok {
			order = append(order, t.thread)
		}
		byThread[t.thread] = append(byThread[t.thread], t)
	}
	rows.Close()

	rep := &CostRepairReport{}
	days := map[string]*CostRepairDay{}
	var changed []repairTurn
	for _, id := range order {
		turns := byThread[id]
		var restores []agent.Restore
		var end *float64
		// a deleted conversation or one on another machine: no session file
		// here, its own running total only
		if t, ok := threads[id]; ok && t.host == 0 && t.session != "" && homes[t.account] != "" {
			if path, err := agent.FindClaude(homes[t.account], t.session); err == nil {
				if restores, end, err = agent.ClaudeRestores(path); err == nil {
					rep.Checked++
				}
			}
		}
		repairSession(turns, restores, end)
		touched := false
		for _, t := range turns {
			rep.Before += t.old
			rep.After += t.new
			date := t.at.Local().Format("2006-01-02")
			if days[date] == nil {
				days[date] = &CostRepairDay{Date: date}
			}
			days[date].Before += t.old
			days[date].After += t.new
			if math.Abs(t.new-t.old) > 1e-9 {
				changed = append(changed, t)
				touched = true
			}
		}
		if touched {
			rep.Threads++
		}
	}
	rep.Turns = len(changed)
	for _, d := range days {
		rep.Days = append(rep.Days, *d)
	}
	sort.Slice(rep.Days, func(i, j int) bool { return rep.Days[i].Date < rep.Days[j].Date })
	if !apply || len(changed) == 0 {
		return rep, nil
	}

	fixes := make([]costFix, len(changed))
	for i, t := range changed {
		fixes[i] = costFix{id: t.id, thread: t.thread, cur: t.old, new: t.new}
	}
	rep.At = store.Now()
	if rep.Backup, err = applyCostFixes(ctx, cfg, st, "costs", fixes, costRepairKey, rep); err != nil {
		return nil, err
	}
	return rep, nil
}

// costFix is one turn's cost now and what it becomes, or (drop) a
// replayed copy of an earlier turn to remove.
type costFix struct {
	id       int64
	thread   string
	cur, new float64
	drop     bool
	tok      tokens // a dropped copy's token counts
}

// applyCostFixes backs the database up next to it, then changes the turns,
// their conversations' totals and turn footers, and saves the report under
// key, all in one transaction. It returns the backup's path.
func applyCostFixes(ctx context.Context, cfg *config.Config, st *store.Store, tag string, fixes []costFix, key string, report any) (string, error) {
	backup := filepath.Join(filepath.Dir(cfg.DBPath), "pocket-ai.db.bak-"+tag+"-"+time.Now().Format("20060102-150405"))
	if err := st.Backup(ctx, backup); err != nil {
		return "", fmt.Errorf("backup before the repair: %w", err)
	}
	tx, err := st.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	less := map[string]float64{}
	fix := map[string]map[float64][]float64{} // thread → cost now → new costs (the turn footers)
	drops := map[string]map[tokens]int{}      // thread → a dropped copy's tokens → how many
	for _, t := range fixes {
		if t.drop {
			if _, err := tx.ExecContext(ctx, `DELETE FROM turns WHERE id = ?`, t.id); err != nil {
				return "", err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE threads SET usage = json_set(usage,
				'$.turns', max(0, coalesce(json_extract(usage, '$.turns'), 0) - 1),
				'$.input_tokens', max(0, coalesce(json_extract(usage, '$.input_tokens'), 0) - ?),
				'$.output_tokens', max(0, coalesce(json_extract(usage, '$.output_tokens'), 0) - ?),
				'$.cached_tokens', max(0, coalesce(json_extract(usage, '$.cached_tokens'), 0) - ?)) WHERE id = ?`,
				t.tok.in, t.tok.out, t.tok.cached, t.thread); err != nil {
				return "", err
			}
			less[t.thread] += t.cur
			if drops[t.thread] == nil {
				drops[t.thread] = map[tokens]int{}
			}
			drops[t.thread][t.tok]++
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE turns SET cost_usd = ? WHERE id = ?`, t.new, t.id); err != nil {
			return "", err
		}
		less[t.thread] += t.cur - t.new
		if fix[t.thread] == nil {
			fix[t.thread] = map[float64][]float64{}
		}
		fix[t.thread][t.cur] = append(fix[t.thread][t.cur], t.new)
	}
	for id, d := range less {
		if _, err := tx.ExecContext(ctx, `UPDATE threads SET usage = json_set(usage, '$.cost_usd', max(0, coalesce(json_extract(usage, '$.cost_usd'), 0) - ?)) WHERE id = ?`, d, id); err != nil {
			return "", err
		}
		if err := repairFooters(ctx, tx, id, fix[id], drops[id]); err != nil {
			return "", err
		}
	}
	if report != nil {
		if r, ok := report.(*CostRepairReport); ok {
			r.Backup = backup
		}
		b, _ := json.Marshal(report)
		if _, err := tx.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, string(b)); err != nil {
			return "", err
		}
	}
	return backup, tx.Commit()
}

// repairFooters gives the turn footers on a conversation's timeline the
// same costs (a footer is the turn whose recorded cost it shows) and
// removes those of dropped copies (all but the first with their tokens).
func repairFooters(ctx context.Context, tx *sql.Tx, thread string, fix map[float64][]float64, drops map[tokens]int) error {
	rows, err := tx.QueryContext(ctx, `SELECT id, data FROM items WHERE thread_id = ? AND kind = 'turn' ORDER BY id`, thread)
	if err != nil {
		return err
	}
	type upd struct {
		id   int64
		data string
	}
	var ups []upd
	var gone []int64
	first := map[tokens]bool{}
	for rows.Next() {
		var id int64
		var data string
		if rows.Scan(&id, &data) != nil {
			continue
		}
		var it map[string]json.RawMessage
		var turn map[string]any
		if json.Unmarshal([]byte(data), &it) != nil || json.Unmarshal(it["turn"], &turn) != nil {
			continue
		}
		num := func(k string) int64 { v, _ := turn[k].(float64); return int64(v) }
		if k := (tokens{num("input_tokens"), num("output_tokens"), num("cached_tokens")}); k.in > 0 {
			if first[k] && drops[k] > 0 {
				drops[k]--
				gone = append(gone, id)
				continue
			}
			first[k] = true
		}
		old, _ := turn["cost_usd"].(float64)
		news := fix[old]
		if len(news) == 0 {
			continue
		}
		turn["cost_usd"], fix[old] = news[0], news[1:]
		it["turn"], _ = json.Marshal(turn)
		b, _ := json.Marshal(it)
		ups = append(ups, upd{id, string(b)})
	}
	rows.Close()
	for _, u := range ups {
		if _, err := tx.ExecContext(ctx, `UPDATE items SET data = ? WHERE id = ?`, u.data, u.id); err != nil {
			return err
		}
	}
	for _, id := range gone {
		if _, err := tx.ExecContext(ctx, `DELETE FROM items WHERE id = ?`, id); err != nil {
			return err
		}
	}
	return nil
}

// repairCostsOnce runs the repair the first time this version starts.
func (s *Server) repairCostsOnce(ctx context.Context) {
	if s.store.BoxSetting(ctx, costRepairKey) != "" {
		return
	}
	rep, err := RepairCosts(ctx, s.cfg, s.store, true)
	if err != nil {
		s.log.Error("repairing Claude turn costs", "err", err)
		return
	}
	if rep.Turns == 0 {
		_ = s.store.SetBoxSetting(ctx, costRepairKey, `{"turns":0}`)
		return
	}
	s.log.Info("repaired Claude turn costs counted twice before 0.48.0", "turns", rep.Turns, "threads", rep.Threads,
		"before_usd", fmt.Sprintf("%.2f", rep.Before), "after_usd", fmt.Sprintf("%.2f", rep.After), "backup", rep.Backup)
}

// String is the report the repair-costs command prints.
func (r *CostRepairReport) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Claude turn costs: $%.2f recorded → $%.2f repaired (%d turns in %d conversations change; %d checked against their session file)\n",
		r.Before, r.After, r.Turns, r.Threads, r.Checked)
	if len(r.Days) > 0 {
		b.WriteString("\nday          recorded    repaired\n")
		for _, d := range r.Days {
			if d.Before < 0.005 && d.After < 0.005 {
				continue
			}
			fmt.Fprintf(&b, "%s  %10.2f  %10.2f\n", d.Date, d.Before, d.After)
		}
	}
	if r.Backup != "" {
		fmt.Fprintf(&b, "\nBackup: %s\n", r.Backup)
	}
	return b.String()
}
