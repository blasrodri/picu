package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
)

// searchResult is one ranked hit: title, direct URL, plain-text snippet.
type searchResult struct {
	Title, URL, Snippet string
}

var (
	reAnchor    = regexp.MustCompile(`(?is)<a\b([^>]*)>(.*?)</a>`)
	reAnchorCls = regexp.MustCompile(`(?i)\bclass\s*=\s*("[^"]*"|'[^']*'|[^\s>]+)`)
)

// parseDDG extracts hits from a DuckDuckGo html/?q= page. Titles carry
// class result__a with the target in the uddg redirect param (or a direct
// href); the following result__snippet anchor, if any, belongs to them.
func parseDDG(html string) []searchResult {
	var out []searchResult
	for _, m := range reAnchor.FindAllStringSubmatch(html, -1) {
		attrs, inner := m[1], m[2]
		cm := reAnchorCls.FindStringSubmatch(attrs)
		if cm == nil {
			continue
		}
		class := strings.ToLower(cm[1])
		switch {
		case strings.Contains(class, "result__a"):
			u := anchorTarget(attrs)
			if u == "" {
				continue
			}
			title := strings.TrimSpace(stripInline(inner))
			if title == "" {
				title = u
			}
			out = append(out, searchResult{Title: title, URL: u})
		case strings.Contains(class, "result__snippet"):
			if len(out) == 0 || out[len(out)-1].Snippet != "" {
				continue
			}
			out[len(out)-1].Snippet = strings.TrimSpace(stripInline(inner))
		}
	}
	return out
}

// anchorTarget resolves an anchor's href to a direct http(s) URL,
// unwrapping DuckDuckGo's uddg redirect parameter when present.
func anchorTarget(attrs string) string {
	hm := reHref.FindStringSubmatch(attrs)
	if hm == nil {
		return ""
	}
	href := htmlUnescape(strings.Trim(hm[1], `"'`))
	if i := strings.Index(href, "uddg="); i >= 0 {
		rest := href[i+len("uddg="):]
		if j := strings.Index(rest, "&"); j >= 0 {
			rest = rest[:j]
		}
		if dec, err := url.QueryUnescape(rest); err == nil {
			href = dec
		}
	}
	if !strings.HasPrefix(href, "http://") && !strings.HasPrefix(href, "https://") {
		return ""
	}
	return href
}

// stripInline drops tags but keeps text (titles carry no block layout).
func stripInline(s string) string {
	return strings.TrimSpace(htmlUnescape(reTags.ReplaceAllString(s, "")))
}

// searchDDG runs one query through the keyless HTML endpoint. No cache:
// rankings are live by nature.
func searchDDG(query string, o fetchOpts) ([]searchResult, error) {
	u := "https://html.duckduckgo.com/html/?q=" + url.QueryEscape(query)
	html, _, err := staticFetch(u, o)
	if err != nil {
		return nil, err
	}
	return parseDDG(html), nil
}

func formatSearch(query string, results []searchResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## %d result%s for %q\n", len(results), plural(len(results)), query)
	if len(results) == 0 {
		b.WriteString("\nno results\n")
		return b.String()
	}
	for _, r := range results {
		fmt.Fprintf(&b, "\n- [%s](%s)\n", r.Title, r.URL)
		if r.Snippet != "" {
			fmt.Fprintf(&b, "  %s\n", r.Snippet)
		}
	}
	return b.String()
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// checkEngine rejects unknown --engine modes before any fetch runs.
func checkEngine(mode string) error {
	switch mode {
	case "ddg", "google":
		return nil
	default:
		return fmt.Errorf("unknown --engine %q (ddg|google)", mode)
	}
}

// googleConsentMarkers identify Google's consent/interstitial pages, which
// carry no results. Observed live in English and Portuguese variants.
var googleConsentMarkers = []string{
	"consent.google.com",
	"before continuing to google search",
	"antes de continuar",
	"unusual traffic",
	"/sorry/",
}

// isGoogleConsent reports a consent-walled Google page.
func isGoogleConsent(html string) bool {
	hay := strings.ToLower(htmlUnescape(reTags.ReplaceAllString(html, " ")))
	for _, m := range googleConsentMarkers {
		if strings.Contains(hay, m) {
			return true
		}
	}
	return false
}

// parseGoogle extracts hits from a Google results page. Result anchors
// point at /url?q=<target>&… with the title inside (often an h3); anchors
// without an http target are navigation, not results.
func parseGoogle(html string) []searchResult {
	var out []searchResult
	for _, m := range reAnchor.FindAllStringSubmatch(html, -1) {
		attrs, inner := m[1], m[2]
		u := googleTarget(attrs)
		if u == "" {
			continue
		}
		title := strings.TrimSpace(stripInline(inner))
		if title == "" {
			title = u
		}
		out = append(out, searchResult{Title: title, URL: u})
	}
	return out
}

// googleTarget resolves a Google result anchor to its direct URL.
func googleTarget(attrs string) string {
	hm := reHref.FindStringSubmatch(attrs)
	if hm == nil {
		return ""
	}
	href := htmlUnescape(strings.Trim(hm[1], `"'`))
	if i := strings.Index(href, "/url?q="); i >= 0 {
		rest := href[i+len("/url?q="):]
		if j := strings.Index(rest, "&"); j >= 0 {
			rest = rest[:j]
		}
		if dec, err := url.QueryUnescape(rest); err == nil {
			href = dec
		} else {
			href = rest
		}
	}
	if !strings.HasPrefix(href, "http://") && !strings.HasPrefix(href, "https://") {
		return ""
	}
	if strings.Contains(strings.ToLower(href), "google.") {
		return ""
	}
	return href
}

// searchGoogle runs one query through Google in headless Chrome. Google
// consent-walls unknown sessions, so this needs an accepted session:
// --profile with a logged-in snapshot or --use-real-profile. A wall
// returns an explicit error, never wall text as results.
func searchGoogle(query string, count int, o fetchOpts) ([]searchResult, error) {
	n := count
	if n <= 0 || n > 50 {
		n = 10
	}
	if o.profile == "" && !*useReal {
		return nil, fmt.Errorf("google engine needs an accepted session: --profile NAME or --use-real-profile")
	}
	u := "https://www.google.com/search?gbv=1&num=" + fmt.Sprint(n) + "&q=" + url.QueryEscape(query)
	html, err := chromeHTML(u, o)
	if err != nil {
		return nil, err
	}
	if isGoogleConsent(html) {
		return nil, fmt.Errorf("google consent wall: use --profile with an accepted session or --use-real-profile (quit Chrome first)")
	}
	return parseGoogle(html), nil
}

// runSearchCmd implements `picu search [--engine ddg|google] [--count N] [--json] words…`.
func runSearchCmd(args []string) {
	query := strings.TrimSpace(strings.Join(args, " "))
	if query == "" {
		fmt.Fprintln(os.Stderr, "usage: picu search [--engine ddg|google] [--count N] [--json] words…")
		os.Exit(2)
	}
	if err := checkEngine(*engineFlg); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(2)
	}
	var results []searchResult
	var err error
	if *engineFlg == "google" {
		results, err = searchGoogle(query, *countFlg, currentOpts())
	} else {
		results, err = searchDDG(query, currentOpts())
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if len(results) > *countFlg && *countFlg >= 0 {
		results = results[:*countFlg]
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		docs := make([]map[string]string, 0, len(results))
		for _, r := range results {
			docs = append(docs, map[string]string{"title": r.Title, "url": r.URL, "snippet": r.Snippet})
		}
		_ = enc.Encode(docs)
		return
	}
	fmt.Print(formatSearch(query, results))
}
