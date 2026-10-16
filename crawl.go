package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

// crawlOpts bounds a crawl: link hops beyond the seed and total pages.
type crawlOpts struct {
	depth int
	pages int
}

// crawlItem is one crawled page in BFS visit order.
type crawlItem struct {
	url   string
	depth int
	doc   map[string]any
	err   error
}

// reMDLink finds markdown links. Images are excluded by the caller (a
// leading '!' means image, not navigation).
var reMDLink = regexp.MustCompile(`\[([^\]]*)\]\(([^)\s]+)\)`)

// extractLinks pulls same-host http(s) links out of a markdown document,
// resolved against the page URL, fragments stripped, order preserved.
func extractLinks(md, pageURL string) []string {
	base, err := url.Parse(pageURL)
	if err != nil {
		return nil
	}
	var out []string
	for _, idx := range reMDLink.FindAllStringSubmatchIndex(md, -1) {
		if idx[0] > 0 && md[idx[0]-1] == '!' {
			continue
		}
		raw := md[idx[4]:idx[5]]
		u, err := url.Parse(strings.TrimSpace(raw))
		if err != nil {
			continue
		}
		u = base.ResolveReference(u)
		if u.Scheme != "http" && u.Scheme != "https" {
			continue
		}
		if !strings.EqualFold(u.Host, base.Host) {
			continue
		}
		u.Fragment = ""
		out = append(out, u.String())
	}
	return out
}

// robotsFor fetches a host's robots.txt once (via get) and returns its
// Disallow paths for our UA or '*'. Fetch failure means allow-all
// (fail-open: a missing robots.txt blocks nothing).
func robotsFor(host string, get func(string) (string, error)) []string {
	body, err := get("http://" + host + "/robots.txt")
	if err != nil {
		return nil
	}
	var out []string
	matched := false
	for _, line := range strings.Split(body, "\n") {
		if i := strings.Index(line, "#"); i >= 0 {
			line = line[:i]
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		k = strings.ToLower(strings.TrimSpace(k))
		v = strings.TrimSpace(v)
		switch k {
		case "user-agent":
			matched = v == "*" || strings.Contains(strings.ToLower(v), "picu")
		case "disallow":
			if matched && v != "" {
				out = append(out, v)
			}
		}
	}
	return out
}

// robotsGetter fetches one robots.txt, short timeout, small body cap:
// politeness metadata, never page content. It rides --proxy like any
// other fetch so crawls behind a proxy stay consistent.
func robotsGetter(target, proxy string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", target, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "picu/0.1")
	resp, err := httpClientFor(proxy).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("robots.txt: %s", resp.Status)
	}
	var b strings.Builder
	buf := make([]byte, 32*1024)
	n, _ := resp.Body.Read(buf)
	b.Write(buf[:n])
	return b.String(), nil
}

// robotsAllowed reports whether path may be crawled under disallows
// (simple prefix match, the rule robots.txt means by default).
func robotsAllowed(path string, disallows []string) bool {
	for _, d := range disallows {
		if strings.HasPrefix(path, d) {
			return false
		}
	}
	return true
}

// crawl walks a BFS frontier from seed through fetchAll (per level, so
// each level keeps input order and the whole run keeps visit order).
// robots resolves a host to its Disallow list and is consulted once per
// host; same-host links only, cycles and repeats skipped, pages caps
// the total before any fetch.
func crawl(seed string, o fetchOpts, co crawlOpts, jobs int, fn fetchFunc, robots func(string) []string) []crawlItem {
	var out []crawlItem
	seen := map[string]bool{seed: true}
	frontier := []string{seed}
	known := map[string][]string{}
	disallows := func(raw string) []string {
		u, err := url.Parse(raw)
		if err != nil {
			return nil
		}
		if d, ok := known[u.Host]; ok {
			return d
		}
		d := robots(u.Host)
		known[u.Host] = d
		return d
	}
	for depth := 0; depth <= co.depth && len(frontier) > 0 && len(out) < co.pages; depth++ {
		batch := frontier
		if len(out)+len(batch) > co.pages {
			batch = batch[:co.pages-len(out)]
		}
		frontier = nil
		results := fetchAll(batch, o, jobs, fn)
		for i, r := range results {
			out = append(out, crawlItem{url: r.url, depth: depth, doc: r.doc, err: r.err})
			if r.err != nil || depth == co.depth {
				continue
			}
			md, _ := r.doc["markdown"].(string)
			for _, l := range extractLinks(md, batch[i]) {
				if seen[l] {
					continue
				}
				seen[l] = true
				u, err := url.Parse(l)
				if err != nil || !robotsAllowed(u.Path, disallows(l)) {
					continue
				}
				frontier = append(frontier, l)
			}
		}
	}
	return out
}

// runCrawl implements `picu crawl URL [--depth N] [--pages M]`: BFS from
// the seed, same host, robots honored, JSON array or per-doc markdown
// in visit order, nonzero exit when any page failed.
func runCrawl(raw []string, opts fetchOpts) {
	if len(raw) != 1 {
		fmt.Fprintln(os.Stderr, "usage: picu crawl URL [--depth N] [--pages M]")
		os.Exit(2)
	}
	if *depthFlg < 0 || *pagesFlg < 1 {
		fmt.Fprintln(os.Stderr, "--depth must be >= 0 and --pages >= 1")
		os.Exit(2)
	}
	if *jobs < 1 {
		fmt.Fprintln(os.Stderr, "--jobs must be >= 1")
		os.Exit(2)
	}
	seed := normalizeURL(raw[0])
	items := crawl(seed, opts, crawlOpts{depth: *depthFlg, pages: *pagesFlg}, *jobs, get, func(host string) []string {
		return robotsFor(host, func(t string) (string, error) { return robotsGetter(t, opts.proxy) })
	})
	if *asJSON {
		arr := make([]map[string]any, len(items))
		for i, it := range items {
			if it.err != nil {
				arr[i] = errorEnvelope(it.url, it.err)
				continue
			}
			it.doc["depth"] = it.depth
			arr[i] = it.doc
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		_ = enc.Encode(arr)
	} else {
		for _, it := range items {
			if it.err != nil {
				fmt.Fprintf(os.Stderr, "error (%s): %v\n", it.url, it.err)
				continue
			}
			fmt.Printf("# %s\n\nSource: %s (via %s)\n\n%s\n", it.doc["title"], it.url, it.doc["renderer"], it.doc["markdown"])
		}
	}
	for _, it := range items {
		if it.err != nil {
			os.Exit(1)
		}
	}
}
