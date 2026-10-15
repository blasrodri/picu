package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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

const googlePage = `<html><body>
<div><a class="zReHs" href="/goto?url=CAESabc==" data-ved="1"><h3 class="LC20lb">Lightpanda | The headless browser</h3><br><div><span>The Guardian</span></div><div><span>Fast browser for machines.</span></div></a></div>
<div><a href="/url?q=https://github.com%2Flightpanda&amp;sa=U"><h3>repo</h3></a></div>
<div><a href="https://example.com/direct"><h3>direct</h3></a></div>
<div><a href="/preferences?hl=en">Preferences</a></div>
<div><a href="https://consent.google.com/ml?continue=x">consent</a></div>
</body></html>`

func TestParseGoogle(t *testing.T) {
	got := parseGoogle(googlePage)
	if len(got) != 3 {
		t.Fatalf("parseGoogle found %d results, want 3: %+v", len(got), got)
	}
	if got[0].Title != "Lightpanda | The headless browser" {
		t.Errorf("title = %+v", got[0])
	}
	if got[0].URL != "https://www.google.com/goto?url=CAESabc==" {
		t.Errorf("goto kept absolute, got %+v", got[0])
	}
	if !strings.Contains(got[0].Snippet, "Fast browser for machines.") {
		t.Errorf("snippet lost: %+v", got[0])
	}
	if got[1].URL != "https://github.com/lightpanda" {
		t.Errorf("legacy /url?q= mishandled: %+v", got[1])
	}
	if got[2].URL != "https://example.com/direct" {
		t.Errorf("direct href mishandled: %+v", got[2])
	}
	if n := len(parseGoogle(`<a href="https://translate.google.com/translate?u=x"><h3>T</h3></a>`)); n != 0 {
		t.Errorf("tool links should drop, got %d results", n)
	}
}

func TestResolveGoto(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/goto" {
			w.Header().Set("Location", "https://x.test/article")
			w.WriteHeader(http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	if got := resolveGoto(srv.URL+"/goto?url=TOKEN", 5*time.Second); got != "https://x.test/article" {
		t.Errorf("redirect not followed, got %q", got)
	}
	plain := "https://x.test/direct"
	if got := resolveGoto(plain, 5*time.Second); got != plain {
		t.Errorf("non-goto must pass through, got %q", got)
	}
	if got := resolveGoto("http://127.0.0.1:9/goto?url=x", time.Second); !strings.Contains(got, "/goto") {
		t.Errorf("failed resolve should keep the link, got %q", got)
	}
}

func TestGoogleConsent(t *testing.T) {
	walled := "Antes de continuar para a Pesquisa Google\nIniciar sesión"
	if !isGoogleConsent(walled) {
		t.Error("Portuguese consent page not detected")
	}
	if isGoogleConsent("[T](/url?q=https://x.test/)") {
		t.Error("results page flagged as consent wall")
	}
	// Consent URLs inside scripts must not trip the wall: detection
	// runs on rendered text, where scripts never appear.
	scripty := `<html><head><script>var u="https://consent.google.com/ml?x=1";</script></head><body><p>real results here</p></body></html>`
	if isGoogleConsent(toMarkdown(scripty)) {
		t.Error("script-embedded consent URL flagged as wall")
	}
	// Footer consent-settings links appear on every accepted results
	// page; only the wall's heading text counts.
	accepted := "[Configuración](https://consent.google.com/ml?continue=x)\nWhy Messi retired\nYahoo Sports"
	if isGoogleConsent(accepted) {
		t.Error("accepted results page flagged as wall")
	}
}

func TestGoogleRateLimit(t *testing.T) {
	if !isGoogleRateLimit("# Error 403 (Forbidden)!!1\nwe are sorry") {
		t.Error("google 403 page not detected")
	}
	if isGoogleRateLimit("## 2 results for \"x\"\n- [T](https://x.test/)") {
		t.Error("results flagged as rate limit")
	}
}

func TestCheckEngine(t *testing.T) {
	if err := checkEngine("bing"); err == nil {
		t.Error("unknown engine should error")
	}
	for _, ok := range []string{"ddg", "google"} {
		if err := checkEngine(ok); err != nil {
			t.Errorf("engine %q should pass: %v", ok, err)
		}
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
