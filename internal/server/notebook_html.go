package server

import (
	"html"
	"regexp"
	"strings"
)

// htmlText turns a web page into readable Markdown-ish text: its title, and
// the article (or main part, or body) without scripts, menus and footers.
// No HTML parser dependency: pages are messy, and this only has to be good
// enough for an agent to read.
func htmlText(page string) (title, text string) {
	if m := reTitle.FindStringSubmatch(page); m != nil {
		title = strings.TrimSpace(html.UnescapeString(reSpace.ReplaceAllString(stripTags(m[1]), " ")))
	}
	if m := reOGTitle.FindStringSubmatch(page); m != nil && title == "" {
		title = strings.TrimSpace(html.UnescapeString(m[1]))
	}
	body := page
	for _, re := range []*regexp.Regexp{reComment, reDrop} {
		body = re.ReplaceAllString(body, " ")
	}
	// prefer the article, then main, then the body
	best := ""
	for _, re := range []*regexp.Regexp{reArticle, reMain, reBody} {
		for _, m := range re.FindAllStringSubmatch(body, -1) {
			if len(m[1]) > len(best) {
				best = m[1]
			}
		}
		if len(stripTags(best)) > 400 {
			break
		}
	}
	if best == "" {
		best = body
	}
	best = reNoise.ReplaceAllString(best, " ")
	best = reHeading.ReplaceAllStringFunc(best, func(h string) string {
		level := int(h[2] - '0')
		if h[1] == '/' {
			return "\n\n"
		}
		return "\n\n" + strings.Repeat("#", level) + " "
	})
	best = reLi.ReplaceAllString(best, "\n- ")
	best = reBlock.ReplaceAllString(best, "\n")
	best = reCell.ReplaceAllString(best, " | ")
	best = html.UnescapeString(stripTags(best))
	lines := strings.Split(best, "\n")
	out := make([]string, 0, len(lines))
	blank := false
	for _, l := range lines {
		l = strings.TrimSpace(reSpace.ReplaceAllString(l, " "))
		if l == "" || l == "-" || l == "|" {
			if !blank && len(out) > 0 {
				out = append(out, "")
			}
			blank = true
			continue
		}
		blank = false
		out = append(out, l)
	}
	return title, strings.TrimSpace(strings.Join(out, "\n"))
}

func stripTags(s string) string { return reTag.ReplaceAllString(s, "") }

var (
	reTitle   = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	reOGTitle = regexp.MustCompile(`(?is)<meta[^>]+property=["']og:title["'][^>]+content=["']([^"']+)`)
	reComment = regexp.MustCompile(`(?s)<!--.*?-->`)
	reDrop    = regexp.MustCompile(`(?is)<(script|style|noscript|svg|template|iframe|head|canvas|select)\b.*?</(script|style|noscript|svg|template|iframe|head|canvas|select)>`)
	reNoise   = regexp.MustCompile(`(?is)<(nav|footer|aside|form|button)\b.*?</(nav|footer|aside|form|button)>`)
	reArticle = regexp.MustCompile(`(?is)<article\b[^>]*>(.*?)</article>`)
	reMain    = regexp.MustCompile(`(?is)<main\b[^>]*>(.*?)</main>`)
	reBody    = regexp.MustCompile(`(?is)<body\b[^>]*>(.*)</body>`)
	reHeading = regexp.MustCompile(`(?i)</?h[1-6]\b[^>]*>`)
	reLi      = regexp.MustCompile(`(?i)<li\b[^>]*>`)
	reBlock   = regexp.MustCompile(`(?i)<(br|/p|p|/div|div|/tr|tr|/ul|/ol|/table|/section|/blockquote|blockquote|hr|/li|/dd|/dt|pre|/pre)\b[^>]*/?>`)
	reCell    = regexp.MustCompile(`(?i)</t[dh]>`)
	reTag     = regexp.MustCompile(`(?s)<[^>]*>`)
	reSpace   = regexp.MustCompile(`[ \t\r\f\v\x{00a0}]+`)
)
