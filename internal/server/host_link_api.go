package server

// Settings → Machines → "Use one connection" (machine_link.go). The steps
// run in the background; the card follows them with GET …/link?wait=1&since=V
// (answers at the next change, or after 25 s), like a sign-in's state.
//
//	GET    /hosts/{id}/link              the link's state (linkView)
//	POST   /hosts/{id}/link              {"mode":"status"|"agents"}: Install, Connect, Test, then wait at Confirm
//	                                     {"mode":…,"confirm":true}: Switch (agents: they move one at a time)
//	                                     {"cancel":true}: Cancel (at Confirm, or while it tests)
//	POST   /hosts/{id}/link/retry        Retry link (two passing tests in a row move it back)
//	DELETE /hosts/{id}/link              Switch back to ssh per agent
//
// Seeing the state: whoever may use the machine. Changing it: whoever may
// edit the machine (its person; an admin for the server's own machines).

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

func (s *Server) hostLinkRoutes(r chi.Router) {
	r.Get("/hosts/{id}/link", s.handleHostLink)
	r.Post("/hosts/{id}/link", s.handleHostLinkStart)
	r.Post("/hosts/{id}/link/retry", s.handleHostLinkRetry)
	r.Delete("/hosts/{id}/link", s.handleHostLinkOff)
}

func (s *Server) handleHostLink(w http.ResponseWriter, r *http.Request) {
	if !s.requireRun(w, r) {
		return
	}
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	h, err := s.checkHostUse(r.Context(), userFrom(r.Context()), id)
	if err == nil && h == nil {
		err = errNoHost
	}
	if err != nil {
		s.fail(w, err, "link")
		return
	}
	l, err := s.links.get(r.Context(), h.ID)
	if err != nil {
		s.fail(w, err, "link")
		return
	}
	if queryBool(r, "wait") {
		since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
		l.waitChange(r.Context(), since)
	}
	writeJSON(w, http.StatusOK, l.view(r.Context(), h))
}

type hostLinkIn struct {
	Mode    string `json:"mode"`
	Confirm bool   `json:"confirm"`
	Cancel  bool   `json:"cancel"`
}

func (s *Server) handleHostLinkStart(w http.ResponseWriter, r *http.Request) {
	h := s.manageHost(w, r)
	if h == nil {
		return
	}
	var in hostLinkIn
	if err := decode(r, &in); err != nil {
		s.fail(w, badRequest("bad body"), "link")
		return
	}
	l, err := s.links.get(r.Context(), h.ID)
	if err != nil {
		s.fail(w, err, "link")
		return
	}
	if in.Cancel {
		l.cancelFlow()
		writeJSON(w, http.StatusOK, l.view(r.Context(), h))
		return
	}
	mode := firstNonEmpty(in.Mode, store.LinkStatus)
	switch mode {
	case store.LinkStatus:
	case store.LinkAgents:
		// "Move agents too" (or the whole link at once): its own test steps
		// (a test holder, a tools round trip) and its own Confirm
	default:
		s.fail(w, badRequest("unknown link mode "+mode), "link")
		return
	}
	if ok, why := s.links.support(h); !ok {
		s.fail(w, badRequest(why), "link")
		return
	}
	if in.Confirm {
		if err := l.confirm(r.Context(), mode); err != nil {
			s.fail(w, err, "link")
			return
		}
		s.log.Info("machine link switched on", "machine", h.Name, "mode", mode)
		s.rt.publishHosts()
		writeJSON(w, http.StatusOK, l.view(r.Context(), h))
		return
	}
	if _, err := l.begin(mode); err != nil {
		s.fail(w, err, "link")
		return
	}
	writeJSON(w, http.StatusAccepted, l.view(r.Context(), h))
}

func (s *Server) handleHostLinkRetry(w http.ResponseWriter, r *http.Request) {
	h := s.manageHost(w, r)
	if h == nil {
		return
	}
	l, err := s.links.get(r.Context(), h.ID)
	if err != nil {
		s.fail(w, err, "link")
		return
	}
	if v := l.view(r.Context(), h); v.Mode == store.LinkOff {
		s.fail(w, badRequest("this machine does not use one connection"), "link")
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(s.links.baseCtx(), 5*time.Minute)
		defer cancel()
		if err := l.retry(ctx); err != nil {
			s.log.Info("machine link: retry did not bring it back", "machine", h.Name, "err", err)
		}
	}()
	writeJSON(w, http.StatusAccepted, l.view(r.Context(), h))
}

func (s *Server) handleHostLinkOff(w http.ResponseWriter, r *http.Request) {
	h := s.manageHost(w, r)
	if h == nil {
		return
	}
	l, err := s.links.get(r.Context(), h.ID)
	if err != nil {
		s.fail(w, err, "link")
		return
	}
	l.cancelFlow()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 2*time.Minute)
	defer cancel()
	if err := l.switchBack(ctx); err != nil {
		// off here all the same; the card says what could not be done there
		s.log.Warn("machine link: switched back, with trouble", "machine", h.Name, "err", err)
	} else {
		s.log.Info("machine link switched back", "machine", h.Name)
	}
	s.rt.publishHosts()
	writeJSON(w, http.StatusOK, l.view(r.Context(), h))
}

// CloseLinks closes every machine link and gives the controller lease up,
// so the next process takes it at once (shutdown).
func (s *Server) CloseLinks() { s.links.close() }
