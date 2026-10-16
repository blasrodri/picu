package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestParseProxyURL(t *testing.T) {
	for _, good := range []string{"http://127.0.0.1:8080", "https://proxy.lan:3128", "http://user:pw@proxy:8080"} {
		if _, err := parseProxyURL(good); err != nil {
			t.Errorf("parseProxyURL(%q) = %v", good, err)
		}
	}
	for _, bad := range []string{"", "socks5://127.0.0.1:1080", "http://", "://no-scheme", "ftp://x:21"} {
		if _, err := parseProxyURL(bad); err == nil {
			t.Errorf("parseProxyURL(%q) should fail", bad)
		}
	}
}

func TestChromeArgsProxy(t *testing.T) {
	args := strings.Join(chromeArgs(true, "/tmp/wd", 9222, "http://127.0.0.1:8080"), " ")
	for _, want := range []string{"--proxy-server=http://127.0.0.1:8080", "--proxy-bypass-list=<-loopback>"} {
		if !strings.Contains(args, want) {
			t.Errorf("chrome args missing %q: %s", want, args)
		}
	}
	plain := strings.Join(chromeArgs(true, "/tmp/wd", 9222, ""), " ")
	if strings.Contains(plain, "proxy-server") {
		t.Errorf("direct launch must not carry proxy flags: %s", plain)
	}
}

func TestHTTPClientFor(t *testing.T) {
	if httpClientFor("") != sharedHTTPClient {
		t.Error("empty proxy must reuse the shared client")
	}
	a := httpClientFor("http://127.0.0.1:8080")
	if a == sharedHTTPClient {
		t.Error("proxy client must differ from the shared client")
	}
	if b := httpClientFor("http://127.0.0.1:8080"); b != a {
		t.Error("proxy clients must be cached per URL")
	}
}

func TestLoadProxyList(t *testing.T) {
	body := "# comment\n\n1.2.3.4:6810:user:pw\r\n5.6.7.8:3128\nhttp://9.9.9.9:8080\n"
	got, err := loadProxyList(body)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"http://user:pw@1.2.3.4:6810", "http://5.6.7.8:3128", "http://9.9.9.9:8080"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("loadProxyList = %v, want %v", got, want)
	}
	for _, bad := range []string{"", "# nothing here\n", "nonsense", "a:b:c", "socks5://x:1"} {
		if _, err := loadProxyList(bad); err == nil {
			t.Errorf("loadProxyList(%q) should fail", bad)
		}
	}
}

func TestPoolRotation(t *testing.T) {
	proxyPool.Lock()
	proxyPool.urls = []string{"http://a:1", "http://b:2"}
	proxyPool.next = 0
	proxyPool.Unlock()
	defer func() {
		proxyPool.Lock()
		proxyPool.urls = nil
		proxyPool.next = 0
		proxyPool.Unlock()
	}()
	var seq []string
	for i := 0; i < 5; i++ {
		seq = append(seq, nextPoolProxy())
	}
	want := []string{"http://a:1", "http://b:2", "http://a:1", "http://b:2", "http://a:1"}
	if !reflect.DeepEqual(seq, want) {
		t.Errorf("rotation = %v, want %v", seq, want)
	}
}

func TestFetchRotatesPoolProxy(t *testing.T) {
	proxyPool.Lock()
	proxyPool.urls = []string{"http://a:1", "http://b:2"}
	proxyPool.next = 0
	proxyPool.Unlock()
	defer func() {
		proxyPool.Lock()
		proxyPool.urls = nil
		proxyPool.next = 0
		proxyPool.Unlock()
	}()
	var seen []string
	fn := func(url string, o fetchOpts) (string, string, bool, string, error) {
		seen = append(seen, o.proxy)
		return "md", "t", false, "stub", nil
	}
	for _, u := range []string{"http://x.test/1", "http://x.test/2", "http://x.test/3"} {
		if _, err := fetchOne(u, fetchOpts{budget: 10}, fn); err != nil {
			t.Fatal(err)
		}
	}
	if want := []string{"http://a:1", "http://b:2", "http://a:1"}; !reflect.DeepEqual(seen, want) {
		t.Errorf("fetch proxies = %v, want %v", seen, want)
	}
}

func TestCacheKeyVariesWithProxy(t *testing.T) {
	a := fetchOpts{renderer: "static", budget: 10}
	b := a
	b.proxy = "http://127.0.0.1:8080"
	if cachePath("http://x.test/", "static", a) == cachePath("http://x.test/", "static", b) {
		t.Error("proxy must change the cache key")
	}
}

// forwardProxy is a test forward proxy: it answers with marker HTML for
// the proxied.test host and 204s everything else (a real browser sends
// background CONNECTs too), so tests prove the fetch rode the proxy.
func forwardProxy(t *testing.T, hits *int) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Host, "proxied.test") {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		*hits++
		fmt.Fprint(w, `<html><head><title>Via Proxy</title></head><body><p>proxied marker body.</p></body></html>`)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestE2EStaticViaProxy(t *testing.T) {
	var hits int
	proxy := forwardProxy(t, &hits)
	o := staticOpts()
	o.proxy = proxy
	md, title, _, via, err := get("http://proxied.test/page", o)
	if err != nil {
		t.Fatal(err)
	}
	if hits != 1 {
		t.Errorf("proxy saw %d requests, want 1", hits)
	}
	if title != "Via Proxy" || !strings.Contains(md, "proxied marker body") {
		t.Errorf("proxied doc wrong (via %s): title=%q md=%q", via, title, md)
	}
}

func TestE2EChromeViaProxy(t *testing.T) {
	needRenderer(t, "chrome")
	var hits int
	proxy := forwardProxy(t, &hits)
	o := chromeTestOpts(t)
	o.proxy = proxy
	md, _, _, via, err := get("http://proxied.test/page", o)
	if err != nil {
		t.Fatal(err)
	}
	if hits < 1 {
		t.Error("proxy saw no requests: chromium went direct (impossible — the host does not resolve)")
	}
	if !strings.Contains(md, "proxied marker body") {
		t.Errorf("chromium did not render through the proxy (via %s): %q", via, md)
	}
}
