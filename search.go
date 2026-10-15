package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
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

// isGoogleConsent reports a consent-walled Google page from its visible
// text. It must run on rendered markdown, never raw HTML: scripts and
// hidden templates on plain results pages mention consent URLs and would
// false-positive the wall. Markdown link targets are stripped for the
// same reason: footer links point at consent settings on every results
// page, accepted or not.
func isGoogleConsent(text string) bool {
	hay := strings.ToLower(reMdLink.ReplaceAllString(text, "]()"))
	for _, m := range googleConsentMarkers {
		if strings.Contains(hay, m) {
			return true
		}
	}
	return false
}

var reMdLink = regexp.MustCompile(`\]\([^)]*\)`)

// isGoogleRateLimit reports Google's bare 403 error page, served when the
// network itself is rate-limited. Unlike consent, no session fixes this:
// only stopping requests lets it expire.
func isGoogleRateLimit(text string) bool {
	return strings.Contains(strings.ToLower(text), "error 403 (forbidden)!!1")
}

// parseGoogle extracts hits from a Google results page. Each result is
// one anchor holding the title (usually an h3) plus site and snippet
// text; the href is /goto?url=<token> (legacy /url?q=<target>) or, rarely,
// the direct URL. Navigation anchors match neither and drop out.
func parseGoogle(html string) []searchResult {
	var out []searchResult
	for _, m := range reAnchor.FindAllStringSubmatch(html, -1) {
		attrs, inner := m[1], m[2]
		u := googleTarget(attrs)
		if u == "" {
			continue
		}
		title, rest := googleTitleText(inner)
		if title == "" {
			title = u
		}
		out = append(out, searchResult{Title: title, URL: u, Snippet: rest})
	}
	return out
}

// googleTitleText splits a result anchor into its heading and the
// remaining snippet text.
func googleTitleText(inner string) (title, rest string) {
	if m := reH3.FindStringSubmatch(inner); m != nil {
		title = strings.TrimSpace(stripInline(m[1]))
	}
	text := strings.TrimSpace(stripInline(inner))
	if title == "" {
		if line, _, _ := strings.Cut(text, "\n"); strings.TrimSpace(line) != "" {
			title = strings.TrimSpace(line)
		}
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line == title {
			continue
		}
		if rest != "" {
			rest += " "
		}
		rest += line
	}
	return title, rest
}

var reH3 = regexp.MustCompile(`(?is)<h3[^>]*>(.*?)</h3>`)

// googleTarget resolves a Google result anchor to a fetchable URL: the
// /goto?url= token link absolutized (resolved to the article later),
// the legacy /url?q= target unwrapped, or a direct URL. Google-internal
// hrefs resolve to "".
func googleTarget(attrs string) string {
	hm := reHref.FindStringSubmatch(attrs)
	if hm == nil {
		return ""
	}
	href := htmlUnescape(strings.Trim(hm[1], `"'`))
	if i := strings.Index(href, "/goto?url="); i >= 0 {
		return "https://www.google.com" + href[i:]
	}
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
	// google.* covers translate and other tool links riding inside
	// result anchors: they are actions, not results.
	if strings.Contains(strings.ToLower(href), "google.") {
		return ""
	}
	return href
}

// gotoClient never follows redirects: resolving a /goto token only needs
// the Location header, never the target's body.
var gotoClient = &http.Client{
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// resolveGoto follows one /goto redirect to the article URL. Anything
// else passes through unchanged; failures keep the original link so one
// bad token never sinks the search.
func resolveGoto(link string, timeout time.Duration) string {
	u, err := url.Parse(link)
	if err != nil || !strings.HasPrefix(u.Path, "/goto") {
		return link
	}
	ctx := context.Background()
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(ctx, "GET", link, nil)
	if err != nil {
		return link
	}
	req.Header.Set("User-Agent", "picu/"+picuVersion+" (+agent-research)")
	resp, err := gotoClient.Do(req)
	if err != nil {
		return link
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if loc := resp.Header.Get("Location"); loc != "" {
		if ref, err := url.Parse(loc); err == nil {
			return u.ResolveReference(ref).String()
		}
	}
	return link
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
	u := "https://www.google.com/search?num=" + fmt.Sprint(n) + "&q=" + url.QueryEscape(query)
	html, err := chromeHTML(u, o)
	if err != nil {
		return nil, err
	}
	if dump := os.Getenv("PICU_DUMP_HTML"); dump != "" {
		_ = os.WriteFile(dump, []byte(html), 0o600)
	}
	md := toMarkdown(html)
	if isGoogleRateLimit(md) {
		return nil, fmt.Errorf("google rate limit: automated requests from this network are blocked for now; wait it out (no session fixes this)")
	}
	if isGoogleConsent(md) {
		return nil, fmt.Errorf("google consent wall: use --profile with an accepted session or --use-real-profile (quit Chrome first)")
	}
	results := parseGoogle(html)
	for i, r := range results {
		results[i].URL = resolveGoto(r.URL, o.timeout)
	}
	return results, nil
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
