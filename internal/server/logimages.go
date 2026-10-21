package server

import (
	"errors"
	"net/http"
	"strconv"
	"sync"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/agent"
)

// handleLogImage serves an image that lives only in the CLI's session file:
// a screenshot pasted with a prompt in Paseo or a terminal, or what a Read of
// a picture returned. GET /threads/{id}/log-image?key=<item key>&n=<index>
func (s *Server) handleLogImage(w http.ResponseWriter, r *http.Request) {
	t := s.thread(w, r)
	if t == nil {
		return
	}
	key := r.URL.Query().Get("key")
	n, err := strconv.Atoi(r.URL.Query().Get("n"))
	if t.Provider != "claude" || t.ProviderSessionID == "" || key == "" || err != nil {
		writeErr(w, http.StatusNotFound, "no such image")
		return
	}
	ck := t.ProviderSessionID + "|" + key + "|" + strconv.Itoa(n)
	img, ok := logImages.get(ck)
	if !ok {
		path, err := agent.FindClaude(s.cfg.ClaudeHome, t.ProviderSessionID)
		if err != nil {
			writeErr(w, http.StatusNotFound, "the session file is gone")
			return
		}
		data, mime, err := agent.LogImage(path, key, n)
		if errors.Is(err, agent.ErrNoImage) || (err == nil && imageExt[mime] == "") {
			writeErr(w, http.StatusNotFound, "no such image")
			return
		}
		if err != nil {
			s.fail(w, err, "log image")
			return
		}
		img = cachedImage{data: data, mime: mime}
		logImages.put(ck, img)
	}
	w.Header().Set("Content-Type", img.mime)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=604800, immutable")
	_, _ = w.Write(img.data)
}

type cachedImage struct {
	data []byte
	mime string
}

// A small cache: finding an image means scanning the session file, and a
// thread with screenshots asks for each of them whenever it is opened.
type imageCache struct {
	mu    sync.Mutex
	m     map[string]cachedImage
	order []string
	size  int
}

const imageCacheMax = 64 << 20

var logImages = &imageCache{m: map[string]cachedImage{}}

func (c *imageCache) get(k string) (cachedImage, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.m[k]
	return v, ok
}

func (c *imageCache) put(k string, v cachedImage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.m[k]; ok || len(v.data) > imageCacheMax/4 {
		return
	}
	c.m[k] = v
	c.order = append(c.order, k)
	c.size += len(v.data)
	for c.size > imageCacheMax && len(c.order) > 0 {
		old := c.order[0]
		c.order = c.order[1:]
		c.size -= len(c.m[old].data)
		delete(c.m, old)
	}
}
