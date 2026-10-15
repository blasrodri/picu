package main

import (
	"fmt"
	"regexp"
	"strings"
)

// checkExtract rejects unknown --extract modes before any fetch runs.
func checkExtract(mode string) error {
	switch mode {
	case "", "article", "metadata":
		return nil
	default:
		return fmt.Errorf("unknown --extract %q (article|metadata)", mode)
	}
}

// applyExtract narrows fetched HTML to the requested subset. Article mode
// keeps the main-content subtree; metadata mode returns document fields.
// The empty mode is a passthrough.
func applyExtract(html, md, mode string) string {
	switch mode {
	case "article":
		return toMarkdown(articleHTML(html))
	case "metadata":
		return extractMetadata(html)
	default:
		return md
	}
}

// articleHTML returns the main-content subtree: <article>, then <main>,
// then an element with role="main", else the whole page.
func articleHTML(html string) string {
	lower := strings.ToLower(html)
	for _, tag := range []string{"article", "main"} {
		if sub, ok := firstElement(html, lower, tag); ok {
			return sub
		}
	}
	if m := reRoleMain.FindStringSubmatch(html); m != nil {
		if sub, ok := elementFrom(html, strings.Index(html, m[0]), m[1]); ok {
			return sub
		}
	}
	return html
}

var reRoleMain = regexp.MustCompile(`(?i)<([a-z][a-z0-9]*)[^>]*\brole\s*=\s*["']?main["']?[^>]*>`)

// firstElement returns the first balanced <tag>…</tag> element in html.
func firstElement(html, lower, tag string) (string, bool) {
	open := "<" + tag
	for i := 0; i+len(open) <= len(lower); {
		j := strings.Index(lower[i:], open)
		if j < 0 {
			return "", false
		}
		j += i
		if j+len(open) >= len(lower) {
			return "", false
		}
		if nc := lower[j+len(open)]; nc != '>' && nc != ' ' && nc != '\t' && nc != '\n' && nc != '\r' && nc != '/' {
			i = j + len(open)
			continue
		}
		return elementFrom(html, j, tag)
	}
	return "", false
}

// elementFrom balances nested same-name tags starting at the opening tag
// at html[start], returning the whole outer element.
func elementFrom(html string, start int, tag string) (string, bool) {
	end := scanTagEnd(html, start)
	if end < 0 {
		return "", false
	}
	if strings.HasSuffix(strings.TrimSpace(html[start+1:end]), "/") {
		return "", false
	}
	depth, k := 1, end+1
	for k < len(html) && depth > 0 {
		lt := strings.Index(html[k:], "<")
		if lt < 0 {
			return "", false
		}
		k += lt
		e := scanTagEnd(html, k)
		if e < 0 {
			return "", false
		}
		raw := html[k+1 : e]
		if tagName(raw) == tag {
			switch {
			case strings.HasPrefix(raw, "/"):
				depth--
			case !strings.HasSuffix(strings.TrimSpace(raw), "/"):
				depth++
			}
		}
		k = e + 1
	}
	if depth != 0 {
		return "", false
	}
	return html[start:k], true
}

var (
	reMetaTag  = regexp.MustCompile(`(?i)<meta[^>]+>`)
	reMetaKey  = regexp.MustCompile(`(?i)(?:name|property|itemprop)\s*=\s*("[^"]*"|'[^']*'|[^\s>]+)`)
	reMetaVal  = regexp.MustCompile(`(?i)content\s*=\s*("[^"]*"|'[^']*'|[^\s>]+)`)
	reLinkTag  = regexp.MustCompile(`(?i)<link[^>]+>`)
	reJSONLD   = regexp.MustCompile(`(?is)<script[^>]*type\s*=\s*["']application/ld\+json["'][^>]*>(.*?)</script>`)
	reAttrHref = regexp.MustCompile(`(?i)href\s*=\s*("[^"]*"|'[^']*'|[^\s>]+)`)
)

// extractMetadata renders document fields as key: value lines, present
// fields only, followed by any JSON-LD blocks.
func extractMetadata(html string) string {
	meta := map[string]string{}
	for _, tag := range reMetaTag.FindAllString(html, -1) {
		km := reMetaKey.FindStringSubmatch(tag)
		vm := reMetaVal.FindStringSubmatch(tag)
		if km == nil || vm == nil {
			continue
		}
		key := strings.ToLower(strings.Trim(km[1], `"'`))
		val := strings.TrimSpace(htmlUnescape(strings.Trim(vm[1], `"'`)))
		if val == "" {
			continue
		}
		switch key {
		case "description", "og:description":
			if _, seen := meta["description"]; !seen {
				meta["description"] = val
			}
		case "author", "article:author":
			if _, seen := meta["author"]; !seen {
				meta["author"] = val
			}
		case "article:published_time", "datepublished", "publish_date":
			if _, seen := meta["published"]; !seen {
				meta["published"] = val
			}
		}
	}
	var out []string
	title := "untitled"
	if m := reTitle.FindStringSubmatch(html); len(m) == 2 {
		if t := strings.TrimSpace(htmlUnescape(reTags.ReplaceAllString(m[1], ""))); t != "" {
			title = t
		}
	}
	out = append(out, "title: "+title)
	if v, ok := meta["description"]; ok {
		out = append(out, "description: "+v)
	}
	for _, tag := range reLinkTag.FindAllString(html, -1) {
		if strings.Contains(strings.ToLower(tag), "canonical") {
			if hm := reAttrHref.FindStringSubmatch(tag); hm != nil {
				out = append(out, "canonical: "+strings.Trim(hm[1], `"'`))
				break
			}
		}
	}
	if v, ok := meta["author"]; ok {
		out = append(out, "author: "+v)
	}
	if v, ok := meta["published"]; ok {
		out = append(out, "published: "+v)
	}
	for _, m := range reJSONLD.FindAllStringSubmatch(html, -1) {
		if js := strings.TrimSpace(m[1]); js != "" {
			out = append(out, "jsonld: "+js)
		}
	}
	return strings.Join(out, "\n")
}
