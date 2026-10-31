package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

// Claude Remote Control per thread: while it is on, the thread's Claude
// process stays up (no idle stop), its session shows in claude.ai/code and
// the Claude app, and it comes back after a crash or a Pocket AI restart,
// reattached to the same claude.ai session where Claude Code allows it.

// rcState is the live side of one thread's Remote Control.
type rcState struct {
	store.RemoteControl
	bridgeID  string
	attaching bool
	fails     int // attach failures in a row (backoff)
}

func (rt *engine) rcGet(threadID string) *store.RemoteControl {
	rt.rcMu.Lock()
	defer rt.rcMu.Unlock()
	if st := rt.rc[threadID]; st != nil {
		c := st.RemoteControl
		return &c
	}
	return nil
}

func (rt *engine) rcSet(threadID string, f func(st *rcState)) {
	rt.rcMu.Lock()
	if st := rt.rc[threadID]; st != nil {
		f(st)
	}
	rt.rcMu.Unlock()
	if t, err := rt.s.store.GetThreadAny(rt.ctx, threadID); err == nil {
		rt.publishThread(t)
	}
}

// loadRemote reads which threads want Remote Control and starts them, so
// they are reachable from the phone without opening Pocket AI first.
func (rt *engine) loadRemote(ctx context.Context) {
	list, err := rt.s.store.ListThreadRemotes(ctx)
	if err != nil {
		rt.s.log.Warn("remote control: list", "err", err)
		return
	}
	rt.rcMu.Lock()
	for _, x := range list {
		rt.rc[x.ThreadID] = &rcState{RemoteControl: store.RemoteControl{State: "starting", URL: x.SessionURL}, bridgeID: x.BridgeSessionID}
	}
	rt.rcMu.Unlock()
	if len(list) == 0 {
		return
	}
	go func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
		for _, x := range list {
			if err := rt.attachRemote(x.ThreadID); err != nil {
				rt.s.log.Warn("remote control: restore", "thread", x.ThreadID, "err", err)
			}
		}
	}()
}

// remoteWanted: the person turned Remote Control on for the thread.
func (rt *engine) remoteWanted(threadID string) bool {
	rt.rcMu.Lock()
	defer rt.rcMu.Unlock()
	return rt.rc[threadID] != nil
}

// attachRemote makes sure the thread's process runs with Remote Control on.
func (rt *engine) attachRemote(threadID string) error {
	rt.rcMu.Lock()
	st := rt.rc[threadID]
	if st == nil || st.attaching {
		rt.rcMu.Unlock()
		return nil
	}
	st.attaching = true
	reattach := st.bridgeID
	rt.rcMu.Unlock()
	defer func() {
		rt.rcMu.Lock()
		if st := rt.rc[threadID]; st != nil {
			st.attaching = false
		}
		rt.rcMu.Unlock()
	}()
	fail := func(err error) error {
		rt.rcSet(threadID, func(st *rcState) { st.State, st.Error = "error", err.Error(); st.fails++ })
		return err
	}
	t, err := rt.s.store.GetThreadAny(rt.ctx, threadID)
	if err != nil {
		rt.rcMu.Lock()
		delete(rt.rc, threadID) // the thread is gone
		rt.rcMu.Unlock()
		return nil
	}
	if t.Archived || t.Provider != "claude" || t.Source == "ask" {
		return fail(errors.New("Remote Control is off while the thread is archived"))
	}
	rt.rcSet(threadID, func(st *rcState) { st.State, st.Error = "starting", "" })
	rt.s.re.waitPending(threadID)
	if rt.s.isReattached(threadID) {
		return nil // the agent from before the restart works on: on once it is done (followLeftover)
	}
	r, err := rt.ensure(t)
	if err != nil {
		return fail(err)
	}
	r.mu.Lock()
	already := r.remote
	r.mu.Unlock()
	if already {
		return nil
	}
	ctl, ok := r.sess.(agent.RemoteControllable)
	if !ok {
		return fail(errors.New("this agent has no Remote Control"))
	}
	name := oneLine(firstNonEmpty(t.Title, t.Preview, "Pocket AI conversation"), 60)
	ctx, cancel := context.WithTimeout(rt.ctx, 45*time.Second)
	defer cancel()
	info, err := ctl.RemoteControl(ctx, true, name, reattach)
	if err != nil && reattach != "" && !errors.Is(err, agent.ErrClosed) {
		// the old claude.ai session is gone: start a new one
		rt.s.log.Info("remote control: reattach failed, new session", "thread", threadID, "err", err)
		info, err = ctl.RemoteControl(ctx, true, name, "")
	}
	if err != nil {
		return fail(err)
	}
	r.mu.Lock()
	r.remote = true
	r.mu.Unlock()
	if !rt.remoteWanted(threadID) {
		// turned off while it was connecting
		off, cancel := context.WithTimeout(rt.ctx, 15*time.Second)
		defer cancel()
		_, _ = ctl.RemoteControl(off, false, "", "")
		r.mu.Lock()
		r.remote = false
		r.mu.Unlock()
		return nil
	}
	if info.SessionURL == "" {
		info.SessionURL = rt.rcGet(threadID).URL
	}
	if info.BridgeSessionID == "" {
		info.BridgeSessionID = reattach
	}
	_ = rt.s.store.SetThreadRemote(rt.ctx, store.ThreadRemote{ThreadID: threadID, BridgeSessionID: info.BridgeSessionID, SessionURL: info.SessionURL})
	rt.rcSet(threadID, func(st *rcState) {
		st.bridgeID, st.URL, st.Error, st.fails = info.BridgeSessionID, info.SessionURL, "", 0
		if st.State == "starting" || st.State == "error" {
			st.State = "ready"
		}
	})
	rt.s.log.Info("remote control on", "thread", threadID, "url", info.SessionURL)
	return nil
}

// detachRemote turns Remote Control off; the process idles out as usual.
func (rt *engine) detachRemote(threadID string) {
	rt.rcMu.Lock()
	delete(rt.rc, threadID)
	rt.rcMu.Unlock()
	_ = rt.s.store.DeleteThreadRemote(rt.ctx, threadID)
	if r := rt.get(threadID); r != nil {
		r.mu.Lock()
		on := r.remote
		r.remote = false
		r.lastUsed = time.Now()
		r.mu.Unlock()
		if ctl, ok := r.sess.(agent.RemoteControllable); ok && on {
			ctx, cancel := context.WithTimeout(rt.ctx, 15*time.Second)
			defer cancel()
			if _, err := ctl.RemoteControl(ctx, false, "", ""); err != nil {
				rt.s.log.Warn("remote control off", "thread", threadID, "err", err)
			}
		}
	}
	if t, err := rt.s.store.GetThreadAny(rt.ctx, threadID); err == nil {
		rt.publishThread(t)
	}
	rt.s.log.Info("remote control off", "thread", threadID)
}

// reviveRemote brings Remote Control back after the process ended (crash,
// settings restart, account switch), with a growing pause between tries.
func (rt *engine) reviveRemote(threadID string) {
	rt.rcMu.Lock()
	st := rt.rc[threadID]
	if st == nil {
		rt.rcMu.Unlock()
		return
	}
	wait := 3 * time.Second << min(st.fails, 7) // 3 s … ~6 min
	if st.State != "error" {
		st.State = "starting"
	}
	rt.rcMu.Unlock()
	if t, err := rt.s.store.GetThreadAny(rt.ctx, threadID); err == nil {
		rt.publishThread(t)
	}
	select {
	case <-rt.ctx.Done():
		return
	case <-time.After(wait):
	}
	if rt.get(threadID) != nil && !rt.remoteWanted(threadID) {
		return
	}
	if err := rt.attachRemote(threadID); err != nil {
		rt.s.log.Warn("remote control: revive", "thread", threadID, "err", err)
		go rt.reviveRemote(threadID)
	}
}

// Bridge: how the link to claude.ai is doing.
func (k *runSink) Bridge(state string) {
	switch state {
	case "connected":
	case "ready", "connecting":
		state = "ready"
	case "reconnecting", "disconnected":
		state = "reconnecting"
	case "failed", "error":
		state = "error"
	default:
		return
	}
	k.rt.rcSet(k.r.threadID, func(st *rcState) {
		st.State = state
		if state != "error" {
			st.Error = ""
		} else if st.Error == "" {
			st.Error = "The link to claude.ai failed"
		}
	})
}

// RequestCancelled: Claude Code withdrew a request (answered in the Claude app).
func (k *runSink) RequestCancelled(id string) {
	if !k.beginEvent() {
		return
	}
	defer k.r.eventMu.Unlock()
	r, rt := k.r, k.rt
	r.mu.Lock()
	delete(r.pending, id)
	left := len(r.pending)
	r.mu.Unlock()
	if st, err := rt.s.store.GetItem(rt.ctx, r.threadID, "req-"+id); err == nil {
		var it agent.Item
		if json.Unmarshal(st.Data, &it) == nil && it.Request != nil && it.Request.Status == "pending" {
			it.Request.Status = "expired"
			rt.saveItem(r.threadID, it)
		}
	}
	if left == 0 {
		if t, err := rt.s.store.GetThreadAny(rt.ctx, r.threadID); err == nil && t.Status == "waiting" {
			rt.setStatus(r.threadID, "running", "")
		}
	}
}

// POST /threads/{id}/remote-control {on}
func (s *Server) handleRemoteControl(w http.ResponseWriter, r *http.Request) {
	if !s.requireRun(w, r) {
		return
	}
	t := s.threadAs(w, r, accessOwner)
	if t == nil {
		return
	}
	var in struct {
		On bool `json:"on"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if !in.On {
		s.rt.detachRemote(t.ID)
	} else {
		switch {
		case t.Provider != "claude":
			writeErr(w, http.StatusBadRequest, "Remote Control is a Claude Code feature")
			return
		case t.Source == "ask":
			writeErr(w, http.StatusBadRequest, "Pocket Ask conversations cannot use Remote Control")
			return
		case t.Archived:
			writeErr(w, http.StatusBadRequest, "Move the thread out of the archive first")
			return
		}
		s.rt.rcMu.Lock()
		fresh := s.rt.rc[t.ID] == nil
		if fresh {
			s.rt.rc[t.ID] = &rcState{RemoteControl: store.RemoteControl{State: "starting"}}
		}
		s.rt.rcMu.Unlock()
		if fresh {
			_ = s.store.SetThreadRemote(r.Context(), store.ThreadRemote{ThreadID: t.ID})
		}
		if err := s.rt.attachRemote(t.ID); err != nil {
			s.rt.detachRemote(t.ID)
			s.fail(w, err, "remote control")
			return
		}
	}
	th, err := s.store.GetThreadAny(r.Context(), t.ID)
	if err != nil {
		s.fail(w, err, "remote control")
		return
	}
	writeJSON(w, http.StatusOK, s.rt.decorate(th))
}
