package server

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

func (s *Server) scheduleRoutes(r chi.Router) {
	r.Get("/schedules", s.handleListSchedules)
	r.Get("/schedules/preview", s.handlePreviewSchedule)
	r.Post("/schedules", s.handleSaveSchedule)
	r.Put("/schedules/{id}", s.handleSaveSchedule)
	r.Delete("/schedules/{id}", s.handleDeleteSchedule)
	r.Post("/schedules/{id}/run", s.handleRunSchedule)
}

// scheduleSuffix asks the agent for a verdict line we can read.
const scheduleSuffix = "\n\n---\nThis is a scheduled run and nobody is watching. Do not wait for answers; if something needs a decision, say so. " +
	"Begin your final answer with exactly one line: `OK: <one sentence>` when everything is fine, or `ATTENTION: <one sentence>` when " +
	"something needs a human, then the details."

// ---------- spec ----------

// Spec is a parsed schedule: an interval, or a cron-like matcher.
type Spec struct {
	Every   time.Duration
	minutes [60]bool
	hours   [24]bool
	doms    [32]bool
	months  [13]bool
	dows    [7]bool
	domAny  bool
	dowAny  bool
}

var weekdays = map[string]int{"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6}

// ParseSpec reads "every 30m", "hourly", "daily 08:00", "weekdays 08:00",
// "weekly mon,thu 08:00" or a five-field cron line.
func ParseSpec(spec string) (*Spec, error) {
	f := strings.Fields(strings.ToLower(strings.TrimSpace(spec)))
	if len(f) == 0 {
		return nil, fmt.Errorf("empty schedule")
	}
	hm := func(v string) (int, int, error) {
		parts := strings.Split(v, ":")
		if len(parts) != 2 {
			return 0, 0, fmt.Errorf("time %q must look like 08:00", v)
		}
		h, err1 := strconv.Atoi(parts[0])
		m, err2 := strconv.Atoi(parts[1])
		if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
			return 0, 0, fmt.Errorf("time %q must look like 08:00", v)
		}
		return h, m, nil
	}
	sp := &Spec{}
	at := func(v string) error {
		h, m, err := hm(v)
		if err != nil {
			return err
		}
		sp.minutes[m], sp.hours[h] = true, true
		for i := 1; i <= 31; i++ {
			sp.doms[i] = true
		}
		for i := 1; i <= 12; i++ {
			sp.months[i] = true
		}
		sp.domAny = true
		return nil
	}
	switch f[0] {
	case "every":
		if len(f) != 2 {
			return nil, fmt.Errorf("write it like: every 30m, every 6h, every 2d")
		}
		v := f[1]
		mult := time.Minute
		switch {
		case strings.HasSuffix(v, "d"):
			mult, v = 24*time.Hour, strings.TrimSuffix(v, "d")
		case strings.HasSuffix(v, "h"):
			mult, v = time.Hour, strings.TrimSuffix(v, "h")
		case strings.HasSuffix(v, "m"):
			v = strings.TrimSuffix(v, "m")
		}
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("write it like: every 30m, every 6h, every 2d")
		}
		sp.Every = time.Duration(n) * mult
		if sp.Every < 5*time.Minute {
			return nil, fmt.Errorf("the shortest interval is 5 minutes")
		}
		return sp, nil
	case "hourly":
		sp.Every = time.Hour
		return sp, nil
	case "daily":
		if len(f) != 2 {
			return nil, fmt.Errorf("write it like: daily 08:00")
		}
		if err := at(f[1]); err != nil {
			return nil, err
		}
		for i := range sp.dows {
			sp.dows[i] = true
		}
		sp.dowAny = true
		return sp, nil
	case "weekdays":
		if len(f) != 2 {
			return nil, fmt.Errorf("write it like: weekdays 08:00")
		}
		if err := at(f[1]); err != nil {
			return nil, err
		}
		for i := 1; i <= 5; i++ {
			sp.dows[i] = true
		}
		return sp, nil
	case "weekly":
		if len(f) != 3 {
			return nil, fmt.Errorf("write it like: weekly mon 08:00 (or mon,thu)")
		}
		for _, d := range strings.Split(f[1], ",") {
			n, ok := weekdays[d[:min(3, len(d))]]
			if !ok {
				return nil, fmt.Errorf("unknown day %q", d)
			}
			sp.dows[n] = true
		}
		if err := at(f[2]); err != nil {
			return nil, err
		}
		return sp, nil
	}
	if len(f) != 5 {
		return nil, fmt.Errorf("use every 30m, hourly, daily 08:00, weekdays 08:00, weekly mon 08:00, or a 5-field cron line")
	}
	fields := []struct {
		set      []bool
		min, max int
		name     string
	}{{sp.minutes[:], 0, 59, "minute"}, {sp.hours[:], 0, 23, "hour"}, {sp.doms[:], 1, 31, "day of month"}, {sp.months[:], 1, 12, "month"}, {sp.dows[:], 0, 6, "day of week"}}
	for i, fl := range fields {
		v := f[i]
		if i == 4 {
			for name, n := range weekdays {
				v = strings.ReplaceAll(v, name, strconv.Itoa(n))
			}
			v = strings.ReplaceAll(v, "7", "0")
		}
		if err := cronField(v, fl.set, fl.min, fl.max); err != nil {
			return nil, fmt.Errorf("%s: %v", fl.name, err)
		}
	}
	sp.domAny = f[2] == "*"
	sp.dowAny = f[4] == "*"
	return sp, nil
}

func cronField(v string, set []bool, lo, hi int) error {
	for _, part := range strings.Split(v, ",") {
		step := 1
		if i := strings.Index(part, "/"); i >= 0 {
			n, err := strconv.Atoi(part[i+1:])
			if err != nil || n <= 0 {
				return fmt.Errorf("bad step in %q", part)
			}
			step, part = n, part[:i]
		}
		from, to := lo, hi
		switch {
		case part == "*":
		case strings.Contains(part, "-"):
			ab := strings.SplitN(part, "-", 2)
			a, err1 := strconv.Atoi(ab[0])
			b, err2 := strconv.Atoi(ab[1])
			if err1 != nil || err2 != nil {
				return fmt.Errorf("bad range %q", part)
			}
			from, to = a, b
		default:
			n, err := strconv.Atoi(part)
			if err != nil {
				return fmt.Errorf("bad value %q", part)
			}
			from, to = n, n
		}
		if from < lo || to > hi || from > to {
			return fmt.Errorf("%q is out of range %d-%d", part, lo, hi)
		}
		for x := from; x <= to; x += step {
			set[x] = true
		}
	}
	return nil
}

// Next returns the first run strictly after t (in loc).
func (sp *Spec) Next(t time.Time, loc *time.Location) time.Time {
	if sp.Every > 0 {
		return t.Add(sp.Every).Truncate(time.Minute)
	}
	t = t.In(loc).Truncate(time.Minute).Add(time.Minute)
	for i := 0; i < 366*24*60; i++ {
		dayOK := sp.doms[t.Day()]
		dowOK := sp.dows[int(t.Weekday())]
		var day bool
		switch {
		case sp.domAny && sp.dowAny:
			day = true
		case sp.domAny:
			day = dowOK
		case sp.dowAny:
			day = dayOK
		default:
			day = dayOK || dowOK
		}
		if !sp.months[int(t.Month())] || !day {
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, loc)
			continue
		}
		if !sp.hours[t.Hour()] {
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, loc)
			continue
		}
		if sp.minutes[t.Minute()] {
			return t
		}
		t = t.Add(time.Minute)
	}
	return time.Time{}
}

// ---------- loop ----------

func (s *Server) scheduleLoop(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		due, err := s.store.DueSchedules(ctx, store.FormatTime(time.Now()))
		if err != nil {
			continue
		}
		for _, sc := range due {
			s.runSchedule(ctx, sc)
		}
	}
}

// runSchedule starts one run and moves next_run_at forward.
func (s *Server) runSchedule(ctx context.Context, sc *store.Schedule) (*store.Thread, error) {
	loc := s.loc(ctx, sc.UserID)
	if sp, err := ParseSpec(sc.Spec); err == nil {
		sc.NextRunAt = store.FormatTime(sp.Next(time.Now(), loc))
	} else {
		sc.Enabled = false
	}
	u, err := s.store.GetUser(ctx, sc.UserID)
	if err != nil || !u.MayRun() {
		sc.LastStatus, sc.LastSummary = "failed", "the owner may no longer run agents"
		_ = s.store.SaveSchedule(ctx, sc)
		return nil, errForbidden(sc.LastSummary)
	}
	sc.LastRunAt = store.Now()
	sc.LastStatus, sc.LastSummary = "running", ""
	title := sc.Name + " · " + time.Now().In(loc).Format("Jan 2 15:04")
	t, err := s.createThread(ctx, u, newThread{Provider: sc.Provider, Model: sc.Model, Effort: sc.Effort, Mode: sc.Mode, Speed: optSpeed(sc.Speed), Cwd: sc.Cwd,
		Text: sc.Prompt + scheduleSuffix, Title: title}, "schedule", strconv.FormatInt(sc.ID, 10))
	if t != nil {
		sc.LastThreadID = t.ID
	}
	if err != nil {
		sc.LastStatus, sc.LastSummary = "failed", err.Error()
		if t != nil {
			s.rt.setStatus(t.ID, "error", err.Error())
		}
		if sc.Notify != "never" {
			s.notifyUser(ctx, u.ID, "schedule", "⚠️ "+sc.Name+" could not start", err.Error(), s.threadURL(sc.LastThreadID))
		}
	}
	if serr := s.store.SaveSchedule(ctx, sc); serr != nil {
		s.log.Warn("save schedule", "id", sc.ID, "err", serr)
	}
	s.log.Info("schedule run", "id", sc.ID, "name", sc.Name, "thread", sc.LastThreadID, "err", err)
	return t, err
}

// scheduleFinished records the verdict of a scheduled run's first turn
// and notifies; it returns true when it handled the notification.
func (s *Server) scheduleFinished(t *store.Thread, ti agent.TurnInfo, text string) bool {
	ctx := s.rt.ctx
	sc, err := s.store.ScheduleByThread(ctx, t.ID)
	if err != nil || sc.LastStatus != "running" {
		return false
	}
	first := strings.TrimSpace(strings.SplitN(strings.TrimSpace(text), "\n", 2)[0])
	first = strings.Trim(first, "*_` ")
	upper := strings.ToUpper(first)
	switch {
	case !ti.OK:
		sc.LastStatus, sc.LastSummary = "failed", firstNonEmpty(ti.Error, "the run failed")
	case strings.HasPrefix(upper, "ATTENTION"):
		sc.LastStatus = "attention"
		sc.LastSummary = strings.TrimSpace(strings.TrimLeft(first[len("ATTENTION"):], ":—- "))
	case strings.HasPrefix(upper, "OK"):
		sc.LastStatus = "ok"
		sc.LastSummary = strings.TrimSpace(strings.TrimLeft(first[2:], ":—- "))
	default:
		sc.LastStatus, sc.LastSummary = "attention", oneLine(first, 200)
	}
	if err := s.store.SaveSchedule(ctx, sc); err != nil {
		s.log.Warn("save schedule", "id", sc.ID, "err", err)
	}
	notify := sc.Notify == "always" || (sc.Notify == "attention" && sc.LastStatus != "ok")
	if notify {
		icon := map[string]string{"ok": "✅", "attention": "⚠️", "failed": "❌"}[sc.LastStatus]
		s.notifyUser(ctx, sc.UserID, "schedule", icon+" "+sc.Name, firstNonEmpty(sc.LastSummary, sc.LastStatus), s.threadURL(t.ID))
	}
	if sc.LastStatus != "ok" {
		s.publish(sc.UserID, "ai.schedule_attention", t.ID, map[string]any{"schedule": sc.Name, "status": sc.LastStatus, "summary": sc.LastSummary, "thread_id": t.ID})
	}
	return true
}

// ---------- API ----------

func (s *Server) handleListSchedules(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListSchedules(r.Context(), me(r))
	if err != nil {
		s.fail(w, err, "schedules")
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handlePreviewSchedule(w http.ResponseWriter, r *http.Request) {
	sp, err := ParseSpec(r.URL.Query().Get("spec"))
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error(), "next": []string{}})
		return
	}
	loc := s.loc(r.Context(), me(r))
	next := []string{}
	t := time.Now()
	for i := 0; i < 3; i++ {
		t = sp.Next(t, loc)
		if t.IsZero() {
			break
		}
		next = append(next, t.Format(time.RFC3339))
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "next": next, "error": ""})
}

func (s *Server) handleSaveSchedule(w http.ResponseWriter, r *http.Request) {
	if !s.requireRun(w, r) {
		return
	}
	ctx := r.Context()
	sc := &store.Schedule{UserID: me(r), Enabled: true, Notify: "attention"}
	if id := idParam(r, "id"); id > 0 {
		cur, err := s.store.GetSchedule(ctx, me(r), id)
		if err != nil {
			s.fail(w, err, "schedule")
			return
		}
		sc = cur
	}
	in := *sc
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	in.ID, in.UserID = sc.ID, sc.UserID
	in.LastRunAt, in.LastThreadID, in.LastStatus, in.LastSummary = sc.LastRunAt, sc.LastThreadID, sc.LastStatus, sc.LastSummary
	in.Name, in.Prompt, in.Spec = strings.TrimSpace(in.Name), strings.TrimSpace(in.Prompt), strings.TrimSpace(in.Spec)
	if in.Name == "" || in.Prompt == "" {
		writeErr(w, http.StatusBadRequest, "a schedule needs a name and a prompt")
		return
	}
	switch in.Notify {
	case "always", "attention", "never":
	default:
		in.Notify = "attention"
	}
	sp, err := ParseSpec(in.Spec)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.checkAgentFields(&in.Provider, &in.Mode, &in.Cwd); err != nil {
		s.fail(w, err, "schedule")
		return
	}
	if in.Enabled && (in.Spec != sc.Spec || in.NextRunAt == "" || !sc.Enabled) {
		in.NextRunAt = store.FormatTime(sp.Next(time.Now(), s.loc(ctx, in.UserID)))
	}
	if !in.Enabled {
		in.NextRunAt = ""
	}
	created := in.ID == 0
	if err := s.store.SaveSchedule(ctx, &in); err != nil {
		s.fail(w, err, "save schedule")
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, in)
}

func (s *Server) handleDeleteSchedule(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteSchedule(r.Context(), me(r), idParam(r, "id")); err != nil {
		s.fail(w, err, "delete schedule")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRunSchedule(w http.ResponseWriter, r *http.Request) {
	if !s.requireRun(w, r) {
		return
	}
	sc, err := s.store.GetSchedule(r.Context(), me(r), idParam(r, "id"))
	if err != nil {
		s.fail(w, err, "schedule")
		return
	}
	next := sc.NextRunAt
	t, err := s.runSchedule(r.Context(), sc)
	// a manual run does not move the regular one
	if sc.Enabled && next != "" {
		sc.NextRunAt = next
		_ = s.store.SaveSchedule(r.Context(), sc)
	}
	if err != nil && t == nil {
		s.fail(w, err, "run schedule")
		return
	}
	writeJSON(w, http.StatusCreated, t)
}
