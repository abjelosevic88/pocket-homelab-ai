package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

// Antigravity's permission ids have a colon; the app sends it as %3A.
func TestRequestIDParamUnescapes(t *testing.T) {
	var got string
	rt := chi.NewRouter()
	rt.Post("/threads/{id}/requests/{rid}", func(w http.ResponseWriter, r *http.Request) { got = requestIDParam(r) })
	for path, want := range map[string]string{
		"/threads/t1/requests/rq-jetski-acp-front%3A2": "rq-jetski-acp-front:2",
		"/threads/t1/requests/rq-jetski-acp-front:2":   "rq-jetski-acp-front:2",
		"/threads/t1/requests/rq-7":                    "rq-7",
	} {
		got = ""
		rt.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, path, nil))
		if got != want {
			t.Errorf("%s: got %q, want %q", path, got, want)
		}
	}
}
