package server

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

// panelImagesMax: pictures one panel question can carry.
const panelImagesMax = 8

// panelImages checks the pictures attached to a panel's question: the
// person's own, pictures, not sent anywhere yet. The panel keeps them (its
// question page shows them); participants get copies (panelImageCopies).
func (s *Server) panelImages(ctx context.Context, u *store.User, ids []string) ([]store.PanelImage, error) {
	if len(ids) > panelImagesMax {
		return nil, badRequest(fmt.Sprintf("At most %d pictures per panel", panelImagesMax))
	}
	out := []store.PanelImage{}
	for _, id := range ids {
		up, err := s.store.GetUpload(ctx, u.ID, id)
		if err != nil {
			return nil, badRequest("A picture is gone: attach it again")
		}
		if !strings.HasPrefix(up.Mime, "image/") {
			return nil, badRequest(up.Name + " is not a picture: panels take pictures (put documents in from your apps)")
		}
		if up.ThreadID != nil {
			return nil, badRequest("That picture belongs to a conversation: attach it again")
		}
		out = append(out, store.PanelImage{ID: up.ID, Name: up.Name})
	}
	return out, nil
}

// panelImageCopies copies the panel's pictures for one participant's first
// message: an upload belongs to one conversation, and a deleted participant
// must not take the others' pictures with it. A picture that cannot be
// copied is left out (the rest still go).
func (s *Server) panelImageCopies(ctx context.Context, owner *store.User, p *store.Panel) []string {
	var ids []string
	for _, im := range p.Spec.Images {
		up, err := s.store.GetUpload(ctx, owner.ID, im.ID)
		if err != nil {
			continue
		}
		data, err := os.ReadFile(up.Path)
		if err != nil {
			slog.Warn("panel picture", "panel", p.ID, "err", err)
			continue
		}
		cp, err := s.saveUpload(ctx, owner.ID, up.Name, data)
		if err != nil {
			slog.Warn("panel picture copy", "panel", p.ID, "err", err)
			continue
		}
		ids = append(ids, cp.ID)
	}
	return ids
}

// dropPanelImages removes a deleted panel's own pictures.
func (s *Server) dropPanelImages(ctx context.Context, p *store.Panel) {
	for _, im := range p.Spec.Images {
		if up, err := s.store.GetUpload(ctx, p.UserID, im.ID); err == nil && up.ThreadID == nil {
			s.removeUploadFile(up.Path)
			_ = s.store.DeleteUpload(ctx, p.UserID, up.ID)
		}
	}
}
