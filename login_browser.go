package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/chromedp/cdproto/storage"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/remote"
)

func findSystemChrome() (string, bool) {
	if p := os.Getenv("WEB_CHROME_BIN"); p != "" {
		return p, true
	}
	candidates := []string{"google-chrome", "chromium", "chromium-browser"}
	if runtime.GOOS == "darwin" {
		candidates = append(candidates,
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Chromium.app/Contents/MacOS/Chromium")
	}
	for _, c := range candidates {
		if p, err := exec.LookPath(c); err == nil {
			return p, true
		}
		if _, err := os.Stat(c); err == nil {
			return c, true
		}
	}
	return "", false
}

// realUserDataDir locates the user's everyday Chrome profile directory.
// WEB_CHROME_PROFILE overrides it (also used by tests).
func realUserDataDir() (string, bool) {
	if d := os.Getenv("WEB_CHROME_PROFILE"); d != "" {
		return d, true
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false
	}
	if runtime.GOOS == "darwin" {
		d := filepath.Join(home, "Library", "Application Support", "Google", "Chrome")
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			return d, true
		}
		return "", false
	}
	d := filepath.Join(home, ".config", "google-chrome")
	if st, err := os.Stat(d); err == nil && st.IsDir() {
		return d, true
	}
	return "", false
}

// chromeRunning reports whether a Chrome process is alive. A running Chrome
// owns its profile exclusively, so borrowing the real dir requires it to
// be quit first.
func chromeRunning() bool {
	names := []string{"Google Chrome", "Chromium", "chrome", "chromium"}
	for _, n := range names {
		if err := exec.Command("pgrep", "-x", n).Run(); err == nil {
			return true
		}
	}
	return false
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// runInteractiveLogin opens the system browser at the site, waits for the
// user to log in, then dumps cookies via CDP into the named profile.
func runInteractiveLogin(site, profileName, userDir string, borrowed bool) {
	chrome, ok := findSystemChrome()
	if !ok {
		fatal("no system Chrome found (set WEB_CHROME_BIN) — fall back to --cookie-file")
	}
	port, err := freePort()
	if err != nil {
		fatal(err.Error())
	}
	var errLog bytes.Buffer
	cmd := exec.Command(chrome,
		fmt.Sprintf("--user-data-dir=%s", userDir),
		fmt.Sprintf("--remote-debugging-port=%d", port),
		"--no-first-run", "--no-default-browser-check",
		site)
	cmd.Stdout, cmd.Stderr = nil, &errLog
	if err := cmd.Start(); err != nil {
		fatal(fmt.Sprintf("cannot start browser: %v", err))
	}
	defer func() { _ = cmd.Process.Kill() }()
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()

	// A real profile is heavy — wait for the debugging endpoint instead of
	// assuming it is up the moment the window appears.
	deadline := time.Now().Add(*loginWait)
	for {
		if _, err := debuggerURL(port); err == nil {
			break
		}
		select {
		case <-exited:
			fatal(fmt.Sprintf("browser exited during startup — tail of its log:\n%s", tailLines(errLog.String(), 8)))
		default:
		}
		if time.Now().After(deadline) {
			fatal(fmt.Sprintf("debugging endpoint never came up — tail of browser log:\n%s", tailLines(errLog.String(), 8)))
		}
		time.Sleep(500 * time.Millisecond)
	}

	if borrowed {
		fmt.Printf("your real Chrome opened at %s — confirm you're logged in, then press Enter here…\n", site)
	} else {
		fmt.Printf("browser opened at %s — log in there, then press Enter here…\n", site)
	}
	bufio.NewReader(os.Stdin).ReadString('\n')

	var cookies []storedCookie
	for i := 0; i < 5; i++ {
		var err error
		cookies, err = dumpCookies(port)
		if err == nil {
			break
		}
		if i == 4 {
			fatal(fmt.Sprintf("could not read session: %v (is the browser window still open?)", err))
		}
		time.Sleep(2 * time.Second)
	}
	if len(cookies) == 0 {
		fatal("browser store is empty — did the login complete before pressing Enter?")
	}
	if n := countForSite(cookies, site); n == 0 {
		fatal(fmt.Sprintf("store has %d cookies but none for %s — log in there first, then press Enter", len(cookies), site))
	}
	p := &profile{Name: profileName}
	if existing, err := loadProfile(profileName); err == nil {
		p = existing
	}
	p.merge(cookies)
	if err := saveProfile(p); err != nil {
		fatal(err.Error())
	}
	fmt.Printf("profile %q: captured %d cookies\n", profileName, len(cookies))
	// Snapshot the full session files (cookies + DOM storage) for the
	// headless chrome renderer. Kill first so LevelDB copies are clean.
	_ = cmd.Process.Kill()
	time.Sleep(time.Second)
	if err := materializeWebdata(userDir, profileName); err != nil {
		fmt.Fprintf(os.Stderr, "note: session-file snapshot failed (%v) — cookie jar still saved\n", err)
	} else {
		fmt.Fprintf(os.Stderr, "session files saved for --renderer chrome\n")
	}
}

// minimalProfileFiles is the allowlist copied when borrowing the real
// profile. Session state lives in Cookies; Preferences/Local State keep
// Chrome from first-run Repair flows. Everything else (caches, extensions,
// GPU blobs) stays behind, so the copy is small and the original untouched.
var minimalProfileFiles = []string{
	"Cookies", "Cookies-journal", "Preferences", "Login Data", "Web Data",
	"Network/Cookies", "Network/Cookies-journal",
}

func copyMinimalProfile(srcUserData string) (string, func(), error) {
	tmp, err := os.MkdirTemp("", "web-realprofile-*")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(tmp) }
	dstDefault := filepath.Join(tmp, "Default")
	if err := os.MkdirAll(dstDefault, 0o755); err != nil {
		cleanup()
		return "", nil, err
	}
	var copied int64
	cp := func(src, dst string) error {
		b, err := os.ReadFile(src)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dst, b, 0o600); err != nil {
			return err
		}
		copied += int64(len(b))
		return nil
	}
	for _, name := range minimalProfileFiles {
		src := filepath.Join(srcUserData, "Default", name)
		if _, err := os.Stat(src); err != nil {
			continue
		}
		if err := cp(src, filepath.Join(dstDefault, name)); err != nil {
			cleanup()
			return "", nil, err
		}
	}
	if b, err := os.ReadFile(filepath.Join(srcUserData, "Local State")); err == nil {
		_ = os.WriteFile(filepath.Join(tmp, "Local State"), b, 0o600)
		copied += int64(len(b))
	}
	if copied == 0 {
		cleanup()
		return "", nil, fmt.Errorf("no session files found in %s (is this a Chrome profile?)", srcUserData)
	}
	fmt.Fprintf(os.Stderr, "copied %.1f MB of session state to temp (original untouched)\n", float64(copied)/1048576)
	return tmp, cleanup, nil
}

// clearanceStatus reports Cloudflare clearance state in a profile jar:
// fresh, expiring, expired, or missing. Callers warn on anything but fresh.
func clearanceStatus(p *profile) (state string, hours float64) {
	for _, c := range p.Cookies {
		if c.Name != "cf_clearance" {
			continue
		}
		if c.Expires <= 0 {
			return "session", 0
		}
		hours = (c.Expires - float64(time.Now().Unix())) / 3600
		if hours < 0 {
			return "expired", hours
		}
		if hours < 6 {
			return "expiring", hours
		}
		return "fresh", hours
	}
	return "missing", 0
}

func countForSite(cookies []storedCookie, site string) int {
	u, err := url.Parse(site)
	if err != nil {
		return 0
	}
	host := strings.ToLower(u.Hostname())
	n := 0
	for _, c := range cookies {
		d := strings.ToLower(strings.TrimPrefix(c.Domain, "."))
		if host == d || strings.HasSuffix(host, "."+d) {
			n++
		}
	}
	return n
}

func tailLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) <= n {
		return strings.TrimSpace(s)
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}

func debuggerURL(port int) (string, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/json/version", port))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	var v struct {
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	if err := json.Unmarshal(body, &v); err != nil || v.WebSocketDebuggerURL == "" {
		return "", fmt.Errorf("no debugger url (is remote debugging enabled?)")
	}
	return v.WebSocketDebuggerURL, nil
}

func dumpCookies(port int) ([]storedCookie, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	wsURL, err := debuggerURL(port)
	if err != nil {
		return nil, err
	}
	allocCtx, cancelAlloc := remote.NewAllocator(ctx, wsURL)
	defer cancelAlloc()
	taskCtx, cancelTask := chromedp.NewContext(allocCtx)
	defer cancelTask()
	res, err := chromedp.Call(taskCtx, storage.GetCookies, storage.GetCookiesParams{})
	if err != nil {
		return nil, err
	}
	raw := res.Cookies
	var out []storedCookie
	for _, c := range raw {
		if c.Name == "" {
			continue
		}
		path := c.Path
		if path == "" {
			path = "/"
		}
		out = append(out, storedCookie{c.Name, c.Value, c.Domain, path, c.Expires, c.HTTPOnly, c.Secure, string(c.SameSite)})
	}
	return out, nil
}
