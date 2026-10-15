package main

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestNormalizeURL(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"ole.com.ar/article", "https://ole.com.ar/article"},
		{"https://ole.com.ar/article", "https://ole.com.ar/article"},
		{"http://x.test/y", "http://x.test/y"},
	} {
		if got := normalizeURL(tc.in); got != tc.want {
			t.Errorf("normalizeURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestReorderArgs(t *testing.T) {
	got := reorderArgs([]string{"ole.com.ar/article", "--json", "--budget", "500"})
	want := []string{"--json", "--budget", "500", "ole.com.ar/article"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("reorderArgs = %q, want %q", got, want)
	}
	got = reorderArgs([]string{"--budget=100", "x.test"})
	if !reflect.DeepEqual(got, []string{"--budget=100", "x.test"}) {
		t.Errorf("reorderArgs(=form) = %q", got)
	}
}

func TestProfileCookieMatching(t *testing.T) {
	p := &profile{Name: "t", Cookies: []storedCookie{
		{Name: "sess", Value: "abc", Domain: ".example.com", Path: "/"},
		{Name: "other", Value: "x", Domain: "other.com", Path: "/"},
	}}
	hdr := p.headerFor("https://sub.example.com/a")
	if hdr != "sess=abc" {
		t.Errorf("headerFor subdomain = %q, want sess=abc", hdr)
	}
	if hdr := p.headerFor("https://unrelated.com/"); hdr != "" {
		t.Errorf("headerFor unrelated = %q, want empty", hdr)
	}
}

func TestParseNetscape(t *testing.T) {
	in := "#HttpOnly.example.com\tTRUE\t/\tTRUE\t1790000000\tsess\tabc123\n.example.com\tTRUE\t/acc\tTRUE\t1790000000\ttok\tz\n"
	got := parseNetscapeCookies(in)
	if len(got) != 2 || got[0].Name != "sess" || got[0].Value != "abc123" {
		t.Fatalf("parseNetscape = %+v", got)
	}
	if got[0].Expires != 1790000000 || !got[0].Secure {
		t.Errorf("netscape expiry/secure = %v %v", got[0].Expires, got[0].Secure)
	}
}

func TestImportRoundTrip(t *testing.T) {
	t.Setenv("PICU_CONFIG_HOME", t.TempDir())
	n, err := importCookieFile("demo", "/dev/null")
	if err == nil || n != 0 {
		t.Errorf("empty file should fail, got n=%d err=%v", n, err)
	}
	f := t.TempDir() + "/c.txt"
	os.WriteFile(f, []byte(".example.com\tTRUE\t/\tTRUE\t1790000000\tsess\tabc\n"), 0o600)
	n, err = importCookieFile("demo", f)
	if err != nil || n != 1 {
		t.Fatalf("import = %d, %v", n, err)
	}
	p, err := loadProfile("demo")
	if err != nil || len(p.Cookies) != 1 {
		t.Fatalf("reload = %+v, %v", p, err)
	}
}

func TestFindSystemChromeEnvOverride(t *testing.T) {
	t.Setenv("PICU_CHROME_BIN", "/bin/true")
	got, ok := findSystemChrome()
	if !ok || got != "/bin/true" {
		t.Errorf("env override = %q, %v", got, ok)
	}
}

func TestRealUserDataDirOverride(t *testing.T) {
	t.Setenv("PICU_CHROME_PROFILE", "/tmp/fake-chrome-profile")
	got, ok := realUserDataDir()
	if !ok || got != "/tmp/fake-chrome-profile" {
		t.Errorf("override = %q, %v", got, ok)
	}
}

func TestCdpFileCarriesExpiry(t *testing.T) {
	p := &profile{Name: "t", Cookies: []storedCookie{
		{Name: "sess", Value: "abc", Domain: ".example.com", Path: "/", Expires: 1790000000, HttpOnly: true, Secure: true, SameSite: "Lax"},
		{Name: "tmp", Value: "x", Domain: ".example.com", Path: "/"},
	}}
	f := t.TempDir() + "/c.json"
	if err := p.cdpFile(f); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(f)
	s := string(b)
	if !strings.Contains(s, `"expires":1790000000`) || !strings.Contains(s, `"httpOnly":true`) {
		t.Errorf("cdp file drops fields: %s", s)
	}
	if !strings.Contains(s, `"expires":-1`) {
		t.Errorf("zero expiry must become -1 (session), got: %s", s)
	}
	back, err := parseCDPCookies(b)
	if err != nil || len(back) != 2 || back[0].Expires != 1790000000 {
		t.Errorf("cdp round-trip = %+v, %v", back, err)
	}
}

func TestClearanceStatus(t *testing.T) {
	now := float64(time.Now().Unix())
	fresh := &profile{Cookies: []storedCookie{{Name: "cf_clearance", Expires: now + 8000*3600}}}
	if s, _ := clearanceStatus(fresh); s != "fresh" {
		t.Errorf("fresh = %q", s)
	}
	old := &profile{Cookies: []storedCookie{{Name: "cf_clearance", Expires: now - 3600}}}
	if s, _ := clearanceStatus(old); s != "expired" {
		t.Errorf("expired = %q", s)
	}
	soon := &profile{Cookies: []storedCookie{{Name: "cf_clearance", Expires: now + 3600}}}
	if s, _ := clearanceStatus(soon); s != "expiring" {
		t.Errorf("expiring = %q", s)
	}
	none := &profile{Cookies: []storedCookie{{Name: "sess"}}}
	if s, _ := clearanceStatus(none); s != "missing" {
		t.Errorf("missing = %q", s)
	}
}

func TestCountForSite(t *testing.T) {
	cookies := []storedCookie{
		{Name: "a", Domain: ".perplexity.ai"},
		{Name: "b", Domain: "google.com"},
	}
	if n := countForSite(cookies, "https://www.perplexity.ai/search"); n != 1 {
		t.Errorf("countForSite = %d, want 1", n)
	}
}

func TestToMarkdownGolden(t *testing.T) {
	for _, tc := range []struct{ name, html, want string }{
		{"article",
			`<h1>Hello</h1><p>World <a href="https://x.test/y">link</a> here.</p><script>var x=1;</script><ul><li>one</li><li>two</li></ul>`,
			"# Hello\nWorld [link](https://x.test/y) here.\n- one\n- two"},
		{"attr-quote-entities",
			`<div class="a>b"><p>AT&amp;T &#65; costs &lt;5</p><!-- hidden --><div>tail</div></div>`,
			"AT&T A costs <5\ntail"},
		{"nav-banner",
			`<nav><a href="/x">menu</a></nav><p>Answer body here.</p><div>Allow all</div>`,
			"Answer body here."},
	} {
		if got := toMarkdown(tc.html); got != tc.want {
			t.Errorf("%s:\n got: %q\nwant: %q", tc.name, got, tc.want)
		}
	}
}

func TestToMarkdownNoTagNames(t *testing.T) {
	got := toMarkdown("<div><p>hello</p><div>world</div></div>")
	if strings.Contains(got, "\ndiv\n") || strings.Contains(got, "\np\n") || got == "" {
		t.Errorf("tag names leak into markdown: %q", got)
	}
	if !strings.Contains(got, "hello") || !strings.Contains(got, "world") {
		t.Errorf("content lost: %q", got)
	}
}

func TestStripBanners(t *testing.T) {
	in := "real answer\nCookie Policy\nWe use cookies, pixels and SDKs for stuff\nAllow all\nOnly necessary\nmore content"
	got := stripBanners(in)
	if strings.Contains(got, "Allow all") || strings.Contains(got, "pixels") || strings.Contains(got, "Cookie Policy") {
		t.Errorf("banner survived: %q", got)
	}
	if !strings.Contains(got, "real answer") || !strings.Contains(got, "more content") {
		t.Errorf("content lost: %q", got)
	}
}

func TestRenderPlan(t *testing.T) {
	for _, tc := range []struct {
		prof, web, lp bool
		want          []string
	}{
		{false, false, false, nil},
		{false, false, true, []string{"lightpanda"}},
		{true, false, true, []string{"lightpanda"}},
		{true, true, false, []string{"chrome"}},
		{true, true, true, []string{"chrome", "lightpanda"}},
	} {
		if got := renderPlan(tc.prof, tc.web, tc.lp); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("renderPlan(%v,%v,%v) = %q, want %q", tc.prof, tc.web, tc.lp, got, tc.want)
		}
	}
}

func TestStreamDelta(t *testing.T) {
	if d, ok := streamDelta("hello ", "hello world"); !ok || d != "world" {
		t.Errorf("append = %q, %v", d, ok)
	}
	if _, ok := streamDelta("hello world", "hello"); ok {
		t.Error("shrink must not emit")
	}
	if _, ok := streamDelta("abc", "xyzabc"); ok {
		t.Error("rewrite must not emit")
	}
	if _, ok := streamDelta("same", "same"); ok {
		t.Error("no-change must not emit")
	}
}

func TestTailLines(t *testing.T) {
	if got := tailLines("a\nb\nc\nd", 2); got != "c\nd" {
		t.Errorf("tailLines = %q", got)
	}
	if got := tailLines("only", 8); got != "only" {
		t.Errorf("tailLines short = %q", got)
	}
}

func TestCopyMinimalProfile(t *testing.T) {
	src := t.TempDir()
	os.MkdirAll(src+"/Default/Network", 0o755)
	os.WriteFile(src+"/Default/Cookies", []byte("cookie-db"), 0o600)
	os.WriteFile(src+"/Default/Cache/data", []byte("junk"), 0o600)
	os.WriteFile(src+"/Local State", []byte("{}"), 0o600)
	dst, cleanup, err := copyMinimalProfile(src)
	defer cleanup()
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dst + "/Default/Cookies"); string(b) != "cookie-db" {
		t.Error("Cookies not copied")
	}
	if _, err := os.Stat(dst + "/Default/Cache"); !os.IsNotExist(err) {
		t.Error("Cache should not be copied")
	}
	_, _, err = copyMinimalProfile(t.TempDir())
	if err == nil {
		t.Error("empty dir should fail")
	}
}

func TestCopySessionTree(t *testing.T) {
	src := t.TempDir()
	os.MkdirAll(src+"/Default/Local Storage/leveldb", 0o755)
	os.WriteFile(src+"/Default/Cookies", []byte("cdb"), 0o600)
	os.WriteFile(src+"/Default/Local Storage/leveldb/MANIFEST", []byte("m"), 0o600)
	os.WriteFile(src+"/Default/Local Storage/leveldb/LOCK", []byte("l"), 0o600)
	dst := t.TempDir() + "/webdata"
	n, err := copySessionTree(src, dst)
	if err != nil || n == 0 {
		t.Fatalf("copy = %d, %v", n, err)
	}
	if _, err := os.Stat(dst + "/Default/Local Storage/leveldb/MANIFEST"); err != nil {
		t.Error("leveldb MANIFEST not copied")
	}
	if _, err := os.Stat(dst + "/Default/Local Storage/leveldb/LOCK"); !os.IsNotExist(err) {
		t.Error("LOCK must be skipped")
	}
}

func TestIsChallengeMD(t *testing.T) {
	if !isChallengeMD("# www.perplexity.ai\nPerforming security verification\nRay ID: 123") {
		t.Error("challenge page not detected")
	}
	if isChallengeMD("# Example Domain\nreal content here") {
		t.Error("normal page flagged as challenge")
	}
}

func TestCacheIsolatesProfile(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CACHE_HOME", tmp)
	a := fetchOpts{renderer: "static", profile: "work", timeout: time.Second, cacheTTL: time.Hour}
	b := fetchOpts{renderer: "static", profile: "personal", timeout: time.Second, cacheTTL: time.Hour}
	if cachePath("https://x.test/a", "static", a) == cachePath("https://x.test/a", "static", b) {
		t.Fatal("cache key ignores profile: two profiles share one entry")
	}
	saveCache("https://x.test/a", "static", "secret-body", "T", a)
	if _, _, ok := loadCache("https://x.test/a", "static", b); ok {
		t.Fatal("profile B hit profile A's cache entry")
	}
	if md, _, ok := loadCache("https://x.test/a", "static", a); !ok || md != "secret-body" {
		t.Fatal("profile A should hit its own cache entry")
	}
	st, err := os.Stat(cachePath("https://x.test/a", "static", a))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("cache file perm = %o, want 600 (authenticated content)", st.Mode().Perm())
	}
}

func TestHeaderForExpiryAndSecure(t *testing.T) {
	now := float64(time.Now().Unix())
	p := &profile{Name: "t", Cookies: []storedCookie{
		{Name: "sess", Value: "abc", Domain: ".example.com", Path: "/", Expires: now + 3600},
		{Name: "old", Value: "gone", Domain: ".example.com", Path: "/", Expires: now - 3600},
		{Name: "sec", Value: "s", Domain: ".example.com", Path: "/", Expires: now + 3600, Secure: true},
	}}
	https := p.headerFor("https://sub.example.com/a")
	if !strings.Contains(https, "sess=abc") {
		t.Errorf("fresh cookie missing over https: %q", https)
	}
	if strings.Contains(https, "old=gone") {
		t.Errorf("expired cookie sent: %q", https)
	}
	if !strings.Contains(https, "sec=s") {
		t.Errorf("secure cookie missing over https: %q", https)
	}
	plain := p.headerFor("http://sub.example.com/a")
	if strings.Contains(plain, "sec=s") {
		t.Errorf("secure cookie sent over plain http: %q", plain)
	}
	if strings.Contains(plain, "old=gone") {
		t.Errorf("expired cookie sent over plain http: %q", plain)
	}
}

func TestProfileNameValidation(t *testing.T) {
	t.Setenv("PICU_CONFIG_HOME", t.TempDir())
	for _, bad := range []string{"", "../evil", "..", "a/b", "a b", ".hidden", "x.json", "semi;colon"} {
		if err := validateProfileName(bad); err == nil {
			t.Errorf("validateProfileName(%q) accepted", bad)
		}
	}
	for _, good := range []string{"demo", "pplx", "work-1", "a_b"} {
		if err := validateProfileName(good); err != nil {
			t.Errorf("validateProfileName(%q) rejected: %v", good, err)
		}
	}
	if _, err := loadProfile("../evil"); err == nil {
		t.Error("loadProfile accepted traversal name")
	}
	if err := saveProfile(&profile{Name: "../evil"}); err == nil {
		t.Error("saveProfile accepted traversal name")
	}
	if _, err := profilePath("../evil"); err == nil {
		t.Error("profilePath accepted traversal name")
	}
	if _, err := webdataDir("../evil"); err == nil {
		t.Error("webdataDir accepted traversal name")
	}
}

func TestFreePort(t *testing.T) {
	p, err := freePort()
	if err != nil || p <= 0 {
		t.Errorf("freePort = %d, %v", p, err)
	}
}

func TestIsJSShell(t *testing.T) {
	shell := `<html><head><title>t</title></head><body><div id="root"></div><script src="/a.js"></script></body></html>`
	if !isJSShell(shell, "") {
		t.Error("empty mount point should count as JS shell")
	}
	rich := `<html><body>` + strings.Repeat("<p>real paragraph of content here</p>", 60) + `</body></html>`
	if isJSShell(rich, toMarkdown(rich)) {
		t.Error("content-rich page must not count as JS shell")
	}
}
