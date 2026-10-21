package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
)

func sseHeaders(w http.ResponseWriter) (http.Flusher, bool) {
	fl, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming unsupported")
		return nil, false
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprint(w, "retry: 3000\n\n")
	fl.Flush()
	return fl, true
}

func writeEvent(w http.ResponseWriter, ev sseEvent) error {
	b, err := json.Marshal(ev.Data)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Name, b)
	return err
}

// handleThreadEvents streams one thread: a snapshot of the thread and of the
// items still streaming, then every change.
func (s *Server) handleThreadEvents(w http.ResponseWriter, r *http.Request) {
	t, err := s.store.GetThread(r.Context(), me(r), chi.URLParam(r, "id"))
	if err != nil {
		s.fail(w, err, "thread")
		return
	}
	ch := s.rt.subscribeThread(t.ID)
	defer s.rt.unsubscribeThread(t.ID, ch)
	defer s.rt.watch(t)() // follow the CLI's log while it is open
	fl, ok := sseHeaders(w)
	if !ok {
		return
	}
	_ = writeEvent(w, sseEvent{Name: "thread", Data: s.rt.decorate(t)})
	for _, it := range s.rt.liveItems(t.ID) {
		if st, err := s.store.GetItem(r.Context(), t.ID, it.Key); err == nil {
			data, _ := json.Marshal(it)
			st.Data = data
			_ = writeEvent(w, sseEvent{Name: "item", Data: itemJSON(st)})
		}
	}
	fl.Flush()
	s.stream(w, r, fl, ch)
}

// handleUserEvents streams every change to the person's threads.
func (s *Server) handleUserEvents(w http.ResponseWriter, r *http.Request) {
	uid := me(r)
	ch := s.rt.subscribeUser(uid)
	defer s.rt.unsubscribeUser(uid, ch)
	fl, ok := sseHeaders(w)
	if !ok {
		return
	}
	_ = writeEvent(w, sseEvent{Name: "limits", Data: s.rt.limitsSnapshot()})
	fl.Flush()
	s.stream(w, r, fl, ch)
}

func (s *Server) stream(w http.ResponseWriter, r *http.Request, fl http.Flusher, ch chan sseEvent) {
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-s.rt.ctx.Done(): // shutting down: the browser reconnects to the new process
			return
		case ev := <-ch:
			if writeEvent(w, ev) != nil {
				return
			}
			// drain what is already waiting before flushing (deltas come in bursts)
			for n := 0; n < 64; n++ {
				select {
				case ev := <-ch:
					if writeEvent(w, ev) != nil {
						return
					}
					continue
				default:
				}
				break
			}
			fl.Flush()
		case <-ping.C:
			if writeEvent(w, sseEvent{Name: "ping", Data: time.Now().Unix()}) != nil {
				return
			}
			fl.Flush()
		}
	}
}
