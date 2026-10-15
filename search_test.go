package main

import (
	"strings"
	"testing"
)

const ddgPage = `<html><body>
<div><h2 class="result__title">
<a rel="nofollow" class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Flightpanda.io%2F&amp;rut=abc">Lightpanda | The headless browser</a>
</h2>
<a class="result__snippet" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Flightpanda.io%2F&amp;rut=abc">The AI-native web <b>browser</b> built for machines.</a>
</div>
<div><h2 class="result__title">
<a rel="nofollow" class="result__a" href="https://github.com/lightpanda-io/browser">browser repo</a>
</h2></div>
<div><a class="result__a" href="/internal/page">junk relative</a></div>
<div><a class="other" href="https://x.test/">not a result</a></div>
</body></html>`

func TestParseDDG(t *testing.T) {
	got := parseDDG(ddgPage)
	if len(got) != 2 {
		t.Fatalf("parseDDG found %d results, want 2: %+v", len(got), got)
	}
	if got[0].Title != "Lightpanda | The headless browser" || got[0].URL != "https://lightpanda.io/" {
		t.Errorf("first = %+v", got[0])
	}
	if got[0].Snippet != "The AI-native web browser built for machines." {
		t.Errorf("snippet tags not stripped: %q", got[0].Snippet)
	}
	if got[1].URL != "https://github.com/lightpanda-io/browser" || got[1].Snippet != "" {
		t.Errorf("direct href mishandled: %+v", got[1])
	}
}

func TestParseDDGEmpty(t *testing.T) {
	if got := parseDDG(`<html><body><p>no matches here</p></body></html>`); len(got) != 0 {
		t.Errorf("empty page = %+v", got)
	}
}

func TestFormatSearch(t *testing.T) {
	md := formatSearch("q", []searchResult{{Title: "T", URL: "https://x.test/", Snippet: "S"}})
	for _, want := range []string{`## 1 result for "q"`, "- [T](https://x.test/)", "  S"} {
		if !strings.Contains(md, want) {
			t.Errorf("missing %q:\n%s", want, md)
		}
	}
	if md := formatSearch("q", nil); !strings.Contains(md, "no results") {
		t.Errorf("empty should say so: %q", md)
	}
}
