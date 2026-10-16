package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

// Live integration tests against real websites. They never run by
// default so the normal gate stays hermetic and offline:
//
//	PICU_LIVE=1 go test -count=1 -run Live ./...
//
// example.com/example.org are used because they are tiny, stable,
// and documented for this purpose.

func liveOpts() fetchOpts {
	return fetchOpts{renderer: "static", budget: 8000, timeout: 15 * time.Second}
}

func requireLive(t *testing.T) {
	t.Helper()
	if os.Getenv("PICU_LIVE") == "" {
		t.Skip("set PICU_LIVE=1 to run live integration tests")
	}
}

func TestLiveSingleFetch(t *testing.T) {
	requireLive(t)
	md, title, _, via, err := get("https://example.com", liveOpts())
	if err != nil {
		t.Fatalf("get(example.com) = %v", err)
	}
	if via != "static" {
		t.Errorf("via = %q, want static", via)
	}
	// Canary asserts stay structural: they must survive site copy edits
	// and only fire on picu breakage.
	if !strings.Contains(title, "Example Domain") {
		t.Errorf("title = %q, want it to mention Example Domain", title)
	}
	if len(md) == 0 {
		t.Error("markdown is empty")
	}
}

func TestLiveBatchOrderAndEnvelopes(t *testing.T) {
	requireLive(t)
	old := *schemaFile
	*schemaFile = ""
	defer func() { *schemaFile = old }()
	urls := []string{"https://example.com", "https://example.org"}
	got := fetchAll(urls, liveOpts(), 2, get)
	for i, r := range got {
		if r.err != nil {
			t.Fatalf("item %d (%s): %v", i, urls[i], r.err)
		}
		if r.url != urls[i] {
			t.Errorf("item %d url = %q, want %q", i, r.url, urls[i])
		}
		for _, k := range []string{"url", "title", "markdown", "chars", "cached", "renderer", "profile"} {
			if _, ok := r.doc[k]; !ok {
				t.Errorf("item %d envelope missing key %q", i, k)
			}
		}
	}
}

// TestLiveCrawlDepth crawls example.com one hop: its only links leave
// the host, so a correct crawl returns exactly the seed.
func TestLiveCrawlDepth(t *testing.T) {
	requireLive(t)
	items := crawl("https://example.com", liveOpts(), crawlOpts{depth: 1, pages: 10}, 2, get,
		func(host string) []string {
			return robotsFor(host, func(t string) (string, error) { return robotsGetter(t, "") })
		})
	if len(items) != 1 {
		t.Fatalf("visited %d pages, want exactly the seed", len(items))
	}
	if items[0].err != nil {
		t.Fatalf("seed failed: %v", items[0].err)
	}
	if items[0].depth != 0 {
		t.Errorf("seed depth = %d, want 0", items[0].depth)
	}
}

func TestLiveBatchMixedFailure(t *testing.T) {
	requireLive(t)
	old := *schemaFile
	*schemaFile = ""
	defer func() { *schemaFile = old }()
	urls := []string{"https://example.com", "http://127.0.0.1:9/"}
	got := fetchAll(urls, liveOpts(), 2, get)
	if got[0].err != nil {
		t.Errorf("good item errored: %v", got[0].err)
	}
	if got[1].err == nil {
		t.Fatal("refused connection should surface an error")
	}
	env := errorEnvelope(got[1].url, got[1].err)
	if env["url"] != urls[1] {
		t.Errorf("envelope url = %v, want %s", env["url"], urls[1])
	}
	if _, ok := env["error"].(string); !ok {
		t.Errorf("envelope error should be a string, got %T", env["error"])
	}
}
