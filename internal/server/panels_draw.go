package server

import (
	"regexp"
	"strings"
)

// "Draw it": the participants of a panel draw their ideas as SVG in ```svg
// code blocks. The web app shows those blocks as pictures (an <img> with a
// data: URL, so nothing in them can run) and collects them on the panel's
// Drawings tab.

// panelDrawNote is added to every step's message when the panel draws.
func panelDrawNote(chair bool) string {
	if chair {
		return "\n# Drawings\n\nThe participants drew their ideas; the person sees those drawings next to their answers (you get only their names, as [drawing: …]). Refer to the ideas by name. " +
			"If you recommend something that can be seen, add one drawing of your recommendation under it, by the rules below.\n\n" + panelDrawRules
	}
	return "\n# Draw it\n\nThe person wants to SEE the ideas, not only read about them. Under every idea or option you describe, put a drawing of it. " +
		"A rough sketch that shows the idea is far better than none. Drawings do not count toward any word limit.\n\n" + panelDrawRules
}

const panelDrawRules = "How to draw: one complete SVG per drawing in a fenced code block marked svg (```svg … ```), nothing else in that block. " +
	"Start with <svg xmlns=\"http://www.w3.org/2000/svg\" viewBox=\"0 0 512 512\"> (another viewBox is fine for wide things such as screens or diagrams) and put a <title> naming the idea first inside it. " +
	"Use shapes, paths, gradients and short text only: no scripts, no external images, links or fonts. Keep each under about 8 KB. Do not write files for the drawings: they go in your answer."

var drawingBlock = regexp.MustCompile("(?s)```svg[^\\n]*\\n(.*?)```")
var drawingTitle = regexp.MustCompile(`(?s)<title[^>]*>(.*?)</title>`)

// stripDrawings replaces each drawing with its name: what another model reads
// instead of the SVG (it would only fill its context).
func stripDrawings(t string) string {
	if !strings.Contains(t, "```svg") {
		return t
	}
	return drawingBlock.ReplaceAllStringFunc(t, func(block string) string {
		name := "untitled"
		if m := drawingTitle.FindStringSubmatch(block); m != nil {
			if n := strings.Join(strings.Fields(m[1]), " "); n != "" {
				name = n
			}
		}
		return "[drawing: " + name + "]"
	})
}
