package server

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

// Pictures pasted into a panel's question go to every participant as its own
// copy with the first message; the panel keeps the originals (not cleaned up
// as unsent) until it is deleted.
func TestPanelPicturesReachEveryone(t *testing.T) {
	fastPanels(t)
	s, h, root := testServer(t)
	c, u := person(t, s, h, "pics@example.test", "admin", true)
	fakeClaude(t, s, root)
	if err := os.WriteFile(s.cfg.ClaudeBin, []byte(panelClaude), 0700); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x02\x00\x00\x00\x90wS\xde\x00\x00\x00\x0cIDATx\x9cc\xf8\x0f\x00\x00\x01\x01\x00\x05\x18\xd8N\x00\x00\x00\x00IEND\xaeB`\x82")
	pic, err := s.saveUpload(ctx, u.ID, "icon.png", png)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := s.saveUpload(ctx, u.ID, "notes.txt", []byte("some text"))
	if err != nil {
		t.Fatal(err)
	}
	seats := []map[string]any{{"provider": "claude", "model": "default"}, {"provider": "claude", "model": "default"}}
	c.want("POST", "/api/v1/panels", map[string]any{"kind": "ask", "question": "Redesign it", "depth": "quick", "seats": seats, "upload_ids": []string{doc.ID}}, 400, nil)

	var started struct {
		ID string `json:"id"`
	}
	c.want("POST", "/api/v1/panels", map[string]any{"kind": "ask", "question": "Redesign it", "depth": "quick", "seats": seats, "upload_ids": []string{pic.ID}}, 201, &started)
	p := panelDone(t, s, started.ID)
	if len(p.Spec.Images) != 1 || p.Spec.Images[0].ID != pic.ID || p.Spec.Images[0].Name != "icon.png" {
		t.Fatalf("panel images %+v", p.Spec.Images)
	}
	ps, _ := s.store.PanelSeats(ctx, p.ID)
	copies := map[string]bool{}
	for _, st := range ps {
		ups := s.store.UploadsOfThread(ctx, st.ThreadID)
		if len(ups) != 1 || ups[0].ID == pic.ID {
			t.Fatalf("participant %s pictures %+v", st.Label, ups)
		}
		copies[ups[0].ID] = true
		items, _ := s.store.ListItems(ctx, st.ThreadID)
		if len(items) == 0 || !strings.Contains(items[0].Body, "A picture comes with this message") {
			t.Fatalf("first message of %s: %+v", st.Label, items)
		}
	}
	if len(copies) != len(ps) {
		t.Fatalf("copies %v", copies)
	}
	// the original is not an unsent upload to clean up
	orphans, _ := s.store.OrphanUploads(ctx, store.FormatTime(time.Now().Add(time.Hour)))
	for _, o := range orphans {
		if o.ID == pic.ID {
			t.Fatal("the panel's picture would be cleaned up")
		}
	}
	// an attached picture cannot start a second panel
	c.want("POST", "/api/v1/panels", map[string]any{"kind": "ask", "question": "Again", "depth": "quick", "seats": seats, "upload_ids": []string{copiesKey(copies)}}, 400, nil)

	if err := s.deletePanel(ctx, p); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.GetUpload(ctx, u.ID, pic.ID); err == nil {
		t.Fatal("the panel's picture outlived it")
	}
	if _, err := os.Stat(pic.Path); !os.IsNotExist(err) {
		t.Fatalf("picture file left: %v", err)
	}
}

func copiesKey(m map[string]bool) string {
	for k := range m {
		return k
	}
	return ""
}
