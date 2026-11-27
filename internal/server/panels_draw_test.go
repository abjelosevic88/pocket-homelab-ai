package server

import (
	"strings"
	"testing"

	"github.com/abjelosevic88/pocket-homelab-ai/internal/store"
)

// A drawing panel asks every participant to draw, and hands the others only
// the drawings' names, not their SVG.
func TestPanelDrawing(t *testing.T) {
	svg := "```svg\n<svg xmlns=\"http://www.w3.org/2000/svg\" viewBox=\"0 0 512 512\"><title>Pocket\n  spark</title><rect width=\"512\" height=\"512\"/></svg>\n```"
	got := stripDrawings("**Pocket spark.** An indigo pocket.\n\n" + svg + "\n\nNext idea\n\n```svg\n<svg><rect/></svg>\n```\n\n```go\nfmt.Println()\n```")
	if !strings.Contains(got, "[drawing: Pocket spark]") || !strings.Contains(got, "[drawing: untitled]") || strings.Contains(got, "<rect") || !strings.Contains(got, "fmt.Println") {
		t.Fatalf("stripped: %q", got)
	}

	p := &store.Panel{Kind: "brainstorm", Question: "A new icon"}
	p.Spec.Stages = []store.PanelStage{{Name: "Ideas", Who: "all"}, {Name: "Build on each other", Who: "all", Sees: "others"}, {Name: "Summary", Who: "chair", Sees: "all"}}
	a := &store.PanelSeat{ID: 1, Label: "A", Asked: 0}
	b := &store.PanelSeat{ID: 2, Label: "B", Asked: 0}
	chair := &store.PanelSeat{ID: 3, Label: "S", Chair: true, Asked: 0}
	seats := []*store.PanelSeat{a, b, chair}
	answers := []*store.PanelAnswer{{SeatID: 1, Stage: 0, Text: "Idea one\n\n" + svg}}

	if m := panelMessage(p, seats, answers, b, 1, "", ""); strings.Contains(m, "Draw it") || !strings.Contains(m, "<rect width") {
		t.Fatalf("without drawing the panel is as before:\n%s", m)
	}
	p.Spec.Draw = true
	m := panelMessage(p, seats, answers, b, 1, "", "")
	if !strings.Contains(m, "# Draw it") || !strings.Contains(m, "```svg") || strings.Contains(m, "<rect width") || !strings.Contains(m, "[drawing: Pocket spark]") {
		t.Fatalf("participant message:\n%s", m)
	}
	if m := panelMessage(p, seats, answers, chair, 2, "", ""); !strings.Contains(m, "# Drawings") || strings.Contains(m, "<rect width") {
		t.Fatalf("summary writer message:\n%s", m)
	}
}
