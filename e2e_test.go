package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/remote"
)

// This file is the end-to-end suite: a local fixture server plays the
// page shapes agents meet in the wild (static article, JS-rendered list,
// cookie login, select filter, popup dialog, paginated crawl, robots),
// and tests drive the real pipelines against it. No test here touches
// the network: everything resolves to 127.0.0.1. Tests needing a JS
// renderer skip when the binary is absent, so plain `go test` stays
// green everywhere and CI installs Chrome for full coverage.

func fixtureMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/article", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<html><head><title>Fixture Article</title></head><body><article><h1>Fixture Article</h1><p>first paragraph of the article.</p><p>second paragraph with a <a href="/article/2">continuation</a>.</p></article></body></html>`)
	})
	mux.HandleFunc("/article/2", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<html><head><title>Fixture Article 2</title></head><body><article><h1>part two</h1><p>leaf page, no links.</p></article></body></html>`)
	})
	mux.HandleFunc("/js-list", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<html><head><title>JS List</title></head><body><ul id="items"></ul><script>setTimeout(()=>{document.getElementById('items').innerHTML='<li>item-one</li><li>item-two</li><li>item-three</li>'},300)</script></body></html>`)
	})
	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.FormValue("pw") == "s3cret" {
			http.SetCookie(w, &http.Cookie{Name: "sid", Value: "1", Path: "/"})
			http.Redirect(w, r, "/account", http.StatusFound)
			return
		}
		fmt.Fprint(w, `<html><body><form method="POST"><input name="pw" placeholder="Password"><button>Enter</button></form></body></html>`)
	})
	mux.HandleFunc("/account", func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie("sid")
		if err != nil || c.Value != "1" {
			fmt.Fprint(w, `<html><body><a href="/login">please log in</a></body></html>`)
			return
		}
		fmt.Fprint(w, `<html><body><h1>Welcome back</h1><p>account balance: 42 credits</p></body></html>`)
	})
	mux.HandleFunc("/filter", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<html><body><select id="terr"><option value="all">Todos</option><option value="T10">T10 (3 paradas)</option></select><div id="a" data-t="T10">stop-uno</div><div id="b" data-t="other">stop-dos</div><script>document.getElementById('terr').addEventListener('change',e=>{const v=e.target.value;for(const d of document.querySelectorAll('[data-t]'))d.style.display=d.dataset.t===v||v==='all'?'':'none'})</script></body></html>`)
	})
	mux.HandleFunc("/popup", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<html><body><button id="open">details</button><div id="dlg" role="dialog" style="display:none">Dr. Fixture · Internista · Calle Falsa 123</div><script>document.getElementById('open').addEventListener('click',()=>{document.getElementById('dlg').style.display=''})</script></body></html>`)
	})
	mux.HandleFunc("/paged", func(w http.ResponseWriter, r *http.Request) {
		n := 1
		fmt.Sscanf(r.URL.Query().Get("n"), "%d", &n)
		if n < 1 {
			n = 1
		}
		next := ""
		if n < 3 {
			next = fmt.Sprintf(`<a href="/paged?n=%d">next</a>`, n+1)
		}
		fmt.Fprintf(w, `<html><head><title>page %d</title></head><body><p>page %d body.</p>%s</body></html>`, n, n, next)
	})
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "User-agent: *\nDisallow: /private\n")
	})
	mux.HandleFunc("/private/x", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<html><body><p>secret, robots-blocked.</p></body></html>`)
	})
	mux.HandleFunc("/setcookie", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "sid", Value: "e2e-live", Path: "/"})
		fmt.Fprint(w, `<html><body><p>cookie set.</p></body></html>`)
	})
	return mux
}

func fixtureServer(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(fixtureMux())
	t.Cleanup(srv.Close)
	return srv.URL
}

func staticOpts() fetchOpts {
	return fetchOpts{renderer: "static", budget: 8000, cacheTTL: 0, timeout: 15 * time.Second}
}

// chromeTestOpts materializes an empty snapshot profile under a temp
// config home so chrome-renderer tests never touch real profiles.
// Chrome releases profile files a beat after its process exits, so
// cleanup retries instead of racing the shutdown.
func chromeTestOpts(t *testing.T) fetchOpts {
	t.Helper()
	home, err := os.MkdirTemp("", "picu-e2e-home")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PICU_CONFIG_HOME", home)
	t.Cleanup(func() { removeAllRetry(home) })
	wd, err := webdataDir("e2e")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(wd+"/Default", 0o755); err != nil {
		t.Fatal(err)
	}
	o := staticOpts()
	o.renderer = "chrome"
	o.profile = "e2e"
	return o
}

// needRenderer skips the test when the named JS renderer has no binary.
func needRenderer(t *testing.T, name string) {
	t.Helper()
	switch name {
	case "chrome":
		if _, ok := findSystemChrome(); !ok && os.Getenv("PICU_CHROME_BIN") == "" {
			t.Skip("no system Chrome (set PICU_CHROME_BIN)")
		}
	case "lightpanda":
		if os.Getenv("LIGHTPANDA_BIN") == "" {
			if _, err := exec.LookPath("lightpanda"); err != nil {
				t.Skip("no lightpanda on PATH (set LIGHTPANDA_BIN)")
			}
		}
	}
}

// browserEval attaches a throwaway tab to a daemon browser on a temp
// dir, navigates to page, waits for load, and returns an eval func plus
// cleanup. It mirrors runOpen's attach path (remote allocator,
// WithTargetID, detach-on-cancel) so session behavior is what is tested.
func browserEval(t *testing.T, page string) (func(string) (string, error), func()) {
	t.Helper()
	needRenderer(t, "chrome")
	wd, err := os.MkdirTemp("", "picu-e2e-wd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { removeAllRetry(wd) })
	d, _, ok := daemonGet(wd)
	if !ok {
		t.Fatal("could not start a test browser")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	allocCtx, cancelAlloc := remote.NewAllocator(ctx, d.WSURL)
	tmpCtx, cancelTmp := chromedp.NewContext(allocCtx)
	res, err := chromedp.Call(tmpCtx, target.CreateTarget, target.CreateTargetParams{URL: page})
	cancelTmp()
	if err != nil {
		cancelAlloc()
		cancel()
		t.Fatalf("create target: %v", err)
	}
	taskCtx, cancelTask := chromedp.NewContext(allocCtx,
		chromedp.WithTargetID(target.ID(res.TargetID)), chromedp.WithDetachOnCancel())
	eval := func(expr string) (string, error) { return evalString(taskCtx, expr) }
	deadline := time.Now().Add(20 * time.Second)
	for {
		u, err := eval("location.href")
		if err == nil && u == page {
			break
		}
		if time.Now().After(deadline) {
			cancelTask()
			cancelAlloc()
			cancel()
			t.Fatalf("target never landed on %s", page)
		}
		time.Sleep(250 * time.Millisecond)
	}
	cleanup := func() {
		cancelTask()
		cancelAlloc()
		cancel()
	}
	return eval, cleanup
}

// snapNames returns the visible names of a snapshot for assertions.
func snapNames(t *testing.T, eval func(string) (string, error)) []string {
	t.Helper()
	els, err := snapshotElements(eval)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	var out []string
	for _, e := range els {
		out = append(out, e.Name)
	}
	return out
}

// removeAllRetry removes a browser-touched dir, retrying past the beat
// Chrome holds files after its process exits.
func removeAllRetry(dir string) {
	for i := 0; i < 50; i++ {
		if err := os.RemoveAll(dir); err == nil {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func containsName(names []string, want string) bool {
	for _, n := range names {
		if strings.Contains(n, want) {
			return true
		}
	}
	return false
}

func TestE2EStaticArticle(t *testing.T) {
	base := fixtureServer(t)
	md, title, _, via, err := get(base+"/article", staticOpts())
	if err != nil {
		t.Fatal(err)
	}
	if title != "Fixture Article" {
		t.Errorf("title = %q", title)
	}
	for _, want := range []string{"first paragraph", "continuation"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown missing %q (via %s)", want, via)
		}
	}
}

func TestE2EJSList(t *testing.T) {
	base := fixtureServer(t)
	t.Run("chrome", func(t *testing.T) {
		needRenderer(t, "chrome")
		o := chromeTestOpts(t)
		md, _, _, via, err := get(base+"/js-list", o)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"item-one", "item-two", "item-three"} {
			if !strings.Contains(md, want) {
				t.Errorf("rendered list missing %q (via %s)", want, via)
			}
		}
	})
	t.Run("lightpanda", func(t *testing.T) {
		needRenderer(t, "lightpanda")
		o := staticOpts()
		o.renderer = "lightpanda"
		md, _, _, via, err := get(base+"/js-list", o)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"item-one", "item-two", "item-three"} {
			if !strings.Contains(md, want) {
				t.Errorf("rendered list missing %q (via %s)", want, via)
			}
		}
	})
}

// TestE2ELoginFlow replays the agent login loop: snapshot the form,
// type the password, submit, and read the now-gated account page.
func TestE2ELoginFlow(t *testing.T) {
	base := fixtureServer(t)
	eval, cleanup := browserEval(t, base+"/login")
	defer cleanup()
	if names := snapNames(t, eval); !containsName(names, "Password") {
		t.Fatalf("login form not snapshotted: %v", names)
	}
	steps, err := parseDo(`type input s3cret; click button`)
	if err != nil {
		t.Fatal(err)
	}
	if err := runActions(eval, steps); err != nil {
		t.Fatalf("login steps: %v", err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		if u, _ := eval("location.href"); strings.HasSuffix(u, "/account") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("never landed on /account after login")
		}
		time.Sleep(250 * time.Millisecond)
	}
	// The account page is prose (h1/p), which snapshots deliberately skip:
	// page text is the markdown dump's job. Assert the gated body directly.
	body, err := eval("document.body.innerText")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Welcome back", "42 credits"} {
		if !strings.Contains(body, want) {
			t.Errorf("account page missing %q: %q", want, body)
		}
	}
}

// TestE2EFilterSelect is the doctor-map shape: a select filters a list
// through a change handler, and the snapshot must show the committed
// choice plus only the matching entries.
func TestE2EFilterSelect(t *testing.T) {
	base := fixtureServer(t)
	eval, cleanup := browserEval(t, base+"/filter")
	defer cleanup()
	steps, err := parseDo(`select #terr T10`)
	if err != nil {
		t.Fatal(err)
	}
	if err := runActions(eval, steps); err != nil {
		t.Fatalf("select: %v", err)
	}
	names := snapNames(t, eval)
	if !containsName(names, "T10 (3 paradas)") {
		t.Errorf("committed choice not shown: %v", names)
	}
	if !containsName(names, "stop-uno") {
		t.Errorf("matching entry missing: %v", names)
	}
	if containsName(names, "stop-dos") {
		t.Errorf("filtered-out entry still visible: %v", names)
	}
}

// TestE2EPopupClick opens a dialog and reads its text from the snapshot.
func TestE2EPopupClick(t *testing.T) {
	base := fixtureServer(t)
	eval, cleanup := browserEval(t, base+"/popup")
	defer cleanup()
	steps, err := parseDo(`click #open`)
	if err != nil {
		t.Fatal(err)
	}
	if err := runActions(eval, steps); err != nil {
		t.Fatalf("click: %v", err)
	}
	if names := snapNames(t, eval); !containsName(names, "Dr. Fixture") {
		t.Errorf("popup text not snapshotted: %v", names)
	}
}

// TestE2ECrawlPaged walks three paginated pages from the seed and never
// touches the robots-blocked path.
func TestE2ECrawlPaged(t *testing.T) {
	base := fixtureServer(t)
	items := crawl(base+"/paged", staticOpts(), crawlOpts{depth: 5, pages: 10}, 2, get,
		func(string) []string { return nil })
	if len(items) != 3 {
		t.Fatalf("visited %d pages, want 3", len(items))
	}
	for i, want := range []string{"/paged", "/paged?n=2", "/paged?n=3"} {
		if !strings.HasSuffix(items[i].url, want) || items[i].depth != i {
			t.Errorf("visit %d = %s depth %d, want %s depth %d",
				i, items[i].url, items[i].depth, want, i)
		}
		if items[i].err != nil {
			t.Errorf("visit %d failed: %v", i, items[i].err)
		}
	}
}

// TestE2ECrawlRobots proves politeness end to end: /private/x exists
// and links to nothing blocked, but robots.txt bans it, so a seed that
// links it still never fetches it.
func TestE2ECrawlRobots(t *testing.T) {
	base := fixtureServer(t)
	seedMD := fmt.Sprintf(`[ok](/paged) [secret](%s/private/x)`, base)
	fn := func(url string, o fetchOpts) (string, string, bool, string, error) {
		if strings.HasSuffix(url, "/private/x") {
			t.Errorf("robots-blocked URL was fetched: %s", url)
		}
		if url == base+"/" {
			return seedMD, "seed", false, "stub", nil
		}
		return get(url, o)
	}
	items := crawl(base+"/", staticOpts(), crawlOpts{depth: 1, pages: 10}, 2, fn,
		func(host string) []string {
			return robotsFor(host, func(t string) (string, error) { return robotsGetter(t, "") })
		})
	found := false
	for _, it := range items {
		if strings.HasSuffix(it.url, "/private/x") {
			found = true
		}
	}
	if found {
		t.Error("robots-blocked URL visited")
	}
}

func TestCopySkipsLocalState(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	for _, f := range []string{"Default/Cookies", "Default/Preferences", "Local State"} {
		p := src + "/" + f
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := copySessionTree(src, dst); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dst + "/Default/Cookies"); err != nil {
		t.Errorf("Cookies should be copied: %v", err)
	}
	if _, err := os.Stat(dst + "/Local State"); !os.IsNotExist(err) {
		t.Error("Local State must not be copied: it poisons live-profile adoption")
	}
}

func TestCopyWithJournals(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	if err := os.WriteFile(src+"/Cookies", []byte("main"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src+"/Cookies-wal", []byte("wal-rows"), 0o600); err != nil {
		t.Fatal(err)
	}
	n, err := copyWithJournals(src+"/Cookies", dst+"/Cookies")
	if err != nil {
		t.Fatal(err)
	}
	if want := int64(len("main") + len("wal-rows")); n != want {
		t.Errorf("copied %d bytes, want %d", n, want)
	}
	for _, f := range []string{"/Cookies", "/Cookies-wal"} {
		if _, err := os.Stat(dst + f); err != nil {
			t.Errorf("companion missing: %s", f)
		}
	}
}

// TestE2ELiveImport proves the zero-friction path: a browser holds the
// source dir open (never quit), a cookie lands in it, and the import
// still captures that cookie into the named profile.
func TestE2ELiveImport(t *testing.T) {
	needRenderer(t, "chrome")
	base := fixtureServer(t)
	home, err := os.MkdirTemp("", "picu-e2e-home")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PICU_CONFIG_HOME", home)
	t.Cleanup(func() { removeAllRetry(home) })
	src, err := os.MkdirTemp("", "picu-e2e-src")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { removeAllRetry(src) })
	d, _, ok := daemonGet(src)
	if !ok {
		t.Fatal("could not start a source browser")
	}
	t.Cleanup(func() { daemonKill(src) })
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	allocCtx, cancelAlloc := remote.NewAllocator(ctx, d.WSURL)
	defer cancelAlloc()
	tmpCtx, cancelTmp := chromedp.NewContext(allocCtx)
	_, cerr := chromedp.Call(tmpCtx, target.CreateTarget, target.CreateTargetParams{URL: base + "/setcookie"})
	cancelTmp()
	if cerr != nil {
		t.Fatalf("visit setcookie: %v", cerr)
	}
	// Chrome flushes the cookie store to SQLite on its own cadence, so
	// wait for the files the live copy needs before importing.
	deadline := time.Now().Add(45 * time.Second)
	for {
		if _, err := os.Stat(src + "/Default/Cookies"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("source browser never wrote its cookie store")
		}
		time.Sleep(500 * time.Millisecond)
	}
	time.Sleep(2 * time.Second)
	t.Setenv("PICU_CHROME_PROFILE", src)
	importRealProfile("imp")
	p, err := loadProfile("imp")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range p.Cookies {
		if c.Name == "sid" && c.Value == "e2e-live" {
			found = true
		}
	}
	if !found {
		t.Errorf("imported %d cookies but not sid=e2e-live", len(p.Cookies))
	}
}

// TestE2EBatchEnvelopes checks the batch contract against one good and
// one missing page: doc plus error, input order kept.
func TestE2EBatchEnvelopes(t *testing.T) {
	base := fixtureServer(t)
	// Unroutable host: a hard fetch error. (A 404 body is a successful
	// doc by current contract — only 403/429 error — so it cannot play
	// the failure item here.)
	dead := "http://127.0.0.1:9/"
	results := fetchAll([]string{base + "/article", dead}, staticOpts(), 2, get)
	if len(results) != 2 || results[0].err != nil || results[1].err == nil {
		t.Fatalf("batch = %+v", results)
	}
	env := errorEnvelope(results[1].url, results[1].err)
	if env["url"] != dead || env["error"] == "" {
		t.Errorf("envelope = %v", env)
	}
}
