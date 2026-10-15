package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/cdproto/storage"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/remote"
	"golang.org/x/term"
)

// sessionTree is copied for render-time reuse: cookies plus the DOM storage
// (localStorage/sessionStorage) that SPAs keep their auth tokens in.
var sessionTreeFiles = []string{
	"Cookies", "Cookies-journal", "Preferences", "Local State",
	"Network/Cookies", "Network/Cookies-journal",
}

var sessionTreeDirs = []string{
	"Local Storage/leveldb",
	"Session Storage",
}

func webdataDir(profile string) (string, error) {
	if err := validateProfileName(profile); err != nil {
		return "", err
	}
	return filepath.Join(configDir(), "profiles", profile+".webdata"), nil
}

func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o600)
}

func copySessionTree(srcUserData, dstRoot string) (int64, error) {
	var copied int64
	dstDefault := filepath.Join(dstRoot, "Default")
	for _, name := range sessionTreeFiles {
		src := filepath.Join(srcUserData, "Default", name)
		if name == "Local State" {
			src = filepath.Join(srcUserData, name)
		}
		if _, err := os.Stat(src); err != nil {
			continue
		}
		dst := filepath.Join(dstDefault, name)
		if name == "Local State" {
			dst = filepath.Join(dstRoot, name)
		}
		if err := copyFile(src, dst); err != nil {
			return copied, err
		}
		if st, err := os.Stat(dst); err == nil {
			copied += st.Size()
		}
	}
	for _, dir := range sessionTreeDirs {
		src := filepath.Join(srcUserData, "Default", dir)
		entries, err := os.ReadDir(src)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || e.Name() == "LOCK" || strings.HasPrefix(e.Name(), "Singleton") {
				continue
			}
			if err := copyFile(filepath.Join(src, e.Name()), filepath.Join(dstDefault, dir, e.Name())); err != nil {
				return copied, err
			}
			if st, err := os.Stat(filepath.Join(dstDefault, dir, e.Name())); err == nil {
				copied += st.Size()
			}
		}
	}
	return copied, nil
}

// materializeWebdata snapshots the session files of a logged-in browser dir
// for later headless renders. Call after the browser is killed.
func materializeWebdata(srcUserData, profile string) error {
	dst, err := webdataDir(profile)
	if err != nil {
		return err
	}
	_ = os.RemoveAll(dst)
	n, err := copySessionTree(srcUserData, dst)
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("no session files in %s", srcUserData)
	}
	return nil
}

func boolPtr(b bool) *bool { return &b }

// chromeFetchHTML renders with headless system Chrome over the saved session
// files: full cookie jar plus DOM storage, Chrome's own fingerprint.
var errBotStuck = fmt.Errorf("page stuck on bot verification (it may need an interactive solve)")

var cachedUA string

// desktopUA returns a non-headless user agent for the installed Chrome.
// Headless Chrome advertises "HeadlessChrome" in its UA, which bot walls
// key on even when every cookie is valid.
func desktopUA() string {
	if cachedUA != "" {
		return cachedUA
	}
	cachedUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36"
	if chrome, ok := findSystemChrome(); ok {
		if out, err := exec.Command(chrome, "--version").Output(); err == nil {
			var a, b, c, d int
			if _, err := fmt.Sscanf(string(out), "Google Chrome %d.%d.%d.%d", &a, &b, &c, &d); err == nil {
				cachedUA = fmt.Sprintf("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/%d.%d.%d.%d Safari/537.36", a, b, c, d)
			}
		}
	}
	return cachedUA
}

func launchChrome(headless bool, userDataDir string) (*exec.Cmd, int, error) {
	chrome, ok := findSystemChrome()
	if !ok {
		return nil, 0, fmt.Errorf("no system Chrome found (set PICU_CHROME_BIN)")
	}
	port, err := freePort()
	if err != nil {
		return nil, 0, err
	}
	args := []string{
		fmt.Sprintf("--user-data-dir=%s", userDataDir),
		fmt.Sprintf("--remote-debugging-port=%d", port),
		"--no-first-run", "--no-default-browser-check",
		"about:blank",
	}
	if headless {
		args = append(args, "--headless=new", "--hide-scrollbars", "--mute-audio",
			"--user-agent="+desktopUA(),
			"--disable-blink-features=AutomationControlled")
	}
	cmd := exec.Command(chrome, args...)
	var errLog strings.Builder
	cmd.Stdout, cmd.Stderr = nil, &errLog
	if err := cmd.Start(); err != nil {
		return nil, 0, fmt.Errorf("cannot start chrome: %v", err)
	}
	if _, err := waitDebugger(port, 20*time.Second); err != nil {
		_ = cmd.Process.Kill()
		return nil, 0, fmt.Errorf("chrome endpoint never came up: %v\n%s", err, tailLines(errLog.String(), 5))
	}
	return cmd, port, nil
}

func isTerminal() bool {
	// NOTE: /dev/null is a char device too — only a real TTY counts.
	return term.IsTerminal(int(os.Stdin.Fd()))
}

// injectJar sets the profile's cookies into the fresh browser via CDP, so
// the render always uses current jar state, not just login-day files.
func injectJar(ctx context.Context, profileName, pageURL string) error {
	p, err := loadProfile(profileName)
	if err != nil {
		return nil // no jar — render proceeds on session files alone
	}
	var list []*network.CookieParam
	for _, c := range p.Cookies {
		if c.Name == "" {
			continue
		}
		cp := &network.CookieParam{
			Name: c.Name, Value: c.Value, URL: pageURL,
			Domain: c.Domain, Path: c.Path,
			Secure: c.Secure, HTTPOnly: c.HttpOnly,
			SameSite: network.CookieSameSite(c.SameSite),
		}
		if c.Expires > 0 {
			cp.Expires = cdp.TimeSinceEpoch(c.Expires)
		}
		list = append(list, cp)
	}
	if len(list) == 0 {
		return nil
	}
	_, err = chromedp.Call(ctx, storage.SetCookies, storage.SetCookiesParams{Cookies: list})
	return err
}

func renderHeadless(url, profileName, userDir string, overall time.Duration, o fetchOpts) (string, error) {
	cmd, port, err := launchChrome(true, userDir)
	if err != nil {
		return "", err
	}
	defer func() { _ = cmd.Process.Kill() }()

	ctx, cancel := context.WithTimeout(context.Background(), overall)
	defer cancel()
	wsURL, err := debuggerURL(port)
	if err != nil {
		return "", err
	}
	allocCtx, cancelAlloc := remote.NewAllocator(ctx, wsURL)
	defer cancelAlloc()
	taskCtx, cancelTask := chromedp.NewContext(allocCtx)
	defer cancelTask()

	if err := injectJar(taskCtx, profileName, url); err != nil {
		fmt.Fprintf(os.Stderr, "note: cookie injection: %v\n", err)
	}
	tNav := time.Now()
	if _, err := chromedp.Call(taskCtx, page.Navigate, page.NavigateParams{URL: url}); err != nil {
		return "", fmt.Errorf("navigate: %v", err)
	}
	if err := waitReady(taskCtx, minDuration(40*time.Second, overall/4)); err != nil {
		return "", err
	}
	tReady := time.Now()
	if err := waitChallengeGone(taskCtx, minDuration(45*time.Second, overall/4)); err != nil {
		return "", err
	}
	// AI answers stream in after load: wait for the text to stop growing.
	// Deltas go to stderr (progress); the final document owns stdout.
	// A --select scope skips the wait: the element is either there or not.
	expr := "document.documentElement.outerHTML"
	if o.selector != "" {
		expr = fmt.Sprintf("(s => { const el = document.querySelector(%q); return el ? el.outerHTML : null; })()", o.selector)
	} else {
		tStable := time.Now()
		waitTextStable(taskCtx, 45*time.Second, o.settle, func(delta string) {
			fmt.Fprint(os.Stderr, delta)
		})
		fmt.Fprintf(os.Stderr, "timings: navigate+ready %s challenge-wait %s stream-settle %s\n",
			tReady.Sub(tNav).Round(100*time.Millisecond),
			tStable.Sub(tReady).Round(100*time.Millisecond),
			time.Since(tStable).Round(100*time.Millisecond))
	}
	res, err := chromedp.Call(taskCtx, runtime.Evaluate, runtime.EvaluateParams{
		Expression:    expr,
		ReturnByValue: boolPtr(true),
	})
	if err != nil {
		return "", fmt.Errorf("read DOM: %v", err)
	}
	if o.selector != "" && string(res.Result.Value) == "null" {
		return "", fmt.Errorf("selector %q matched nothing", o.selector)
	}
	var html string
	if err := json.Unmarshal([]byte(res.Result.Value), &html); err != nil {
		return "", fmt.Errorf("decode DOM: %v", err)
	}
	if isChallengeMD(toMarkdown(html)) {
		return "", errBotStuck
	}
	if profileName != "" {
		refreshJar(taskCtx, profileName)
	}
	return html, nil
}

// storedFromNetworkCookie converts a CDP cookie into profile form.
func storedFromNetworkCookie(c *network.Cookie) storedCookie {
	var exp float64
	if c.Expires > 0 {
		exp = float64(c.Expires)
	}
	path := c.Path
	if path == "" {
		path = "/"
	}
	return storedCookie{
		Name: c.Name, Value: c.Value, Domain: c.Domain, Path: path,
		Expires: exp, HttpOnly: c.HTTPOnly, Secure: c.Secure,
		SameSite: string(c.SameSite),
	}
}

// readJar pulls the page's live cookies out of the rendered session.
func readJar(ctx context.Context) ([]storedCookie, error) {
	res, err := chromedp.Call(ctx, storage.GetCookies, storage.GetCookiesParams{})
	if err != nil {
		return nil, err
	}
	var out []storedCookie
	for _, c := range res.Cookies {
		if c.Name == "" {
			continue
		}
		out = append(out, storedFromNetworkCookie(c))
	}
	return out, nil
}

// refreshJar merges the page's live cookies back into the profile so the
// next fast-path fetch (lightpanda, static) carries renewed clearance.
func refreshJar(ctx context.Context, profileName string) {
	fresh, err := readJar(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "note: cookie refresh: %v\n", err)
		return
	}
	if len(fresh) == 0 {
		return
	}
	p, err := loadProfile(profileName)
	if err != nil {
		return // render ran on session files alone; nothing to refresh
	}
	p.merge(fresh)
	if err := saveProfile(p); err != nil {
		fmt.Fprintf(os.Stderr, "note: cookie refresh: %v\n", err)
	}
}

// chromeFetchHTML renders with headless system Chrome over the saved session
// files. If a bot wall refuses to clear and the user can interact, it opens
// the same session visibly once so they solve it by hand; the resulting
// clearance cookie lands in the session files and later runs pass silently.
func chromeFetchHTML(url, profile string, timeout time.Duration, o fetchOpts) (string, error) {
	wd, err := webdataDir(profile)
	if err != nil {
		return "", err
	}
	if st, err := os.Stat(filepath.Join(wd, "Default")); err != nil || !st.IsDir() {
		return "", fmt.Errorf("no saved session files for profile %q — re-run picu login to materialize them", profile)
	}
	// The chrome renderer is the heavy path: challenge solves plus streamed
	// answers need minutes, not seconds. Never run it on less than that.
	overall := timeout
	if overall < 150*time.Second {
		overall = 150 * time.Second
	}
	if profile != "" {
		if p, err := loadProfile(profile); err == nil {
			switch state, hours := clearanceForURL(p, url); state {
			case "missing":
				fmt.Fprintln(os.Stderr, "note: no cf_clearance in profile — expect a bot solve")
			case "expired":
				fmt.Fprintf(os.Stderr, "note: clearance expired %.0fh ago — expect a bot solve\n", -hours)
			case "expiring":
				fmt.Fprintf(os.Stderr, "note: clearance expires in %.1fh — a solve may be near\n", hours)
			}
		}
	}
	html, err := renderHeadless(url, profile, wd, overall, o)
	if err == nil {
		return html, nil
	}
	if !isBotStuck(err) || *noAssist || !isTerminal() {
		return "", err
	}
	if err := assistedSolve(url, profile); err != nil {
		return "", err
	}
	return renderHeadless(url, profile, wd, overall, o)
}

func isBotStuck(err error) bool {
	return err != nil && strings.Contains(err.Error(), errBotStuck.Error())
}

// assistedSolve reopens the saved session visibly at the same URL. The user
// solves the challenge by hand; the clearance cookie stays in the session
// files and is merged back into the cookie jar for the other renderers.
func assistedSolve(url, profileName string) error {
	fmt.Printf("headless is walled — opening your session visibly at %s\nsolve it there, then press Enter here…\n", url)
	wd, err := webdataDir(profileName)
	if err != nil {
		return err
	}
	cmd, port, err := launchChrome(false, wd)
	if err != nil {
		return err
	}
	defer func() { _ = cmd.Process.Kill() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	wsURL, err := debuggerURL(port)
	if err != nil {
		return err
	}
	allocCtx, cancelAlloc := remote.NewAllocator(ctx, wsURL)
	defer cancelAlloc()
	taskCtx, cancelTask := chromedp.NewContext(allocCtx)
	defer cancelTask()
	if _, err := chromedp.Call(taskCtx, page.Navigate, page.NavigateParams{URL: url}); err != nil {
		return fmt.Errorf("navigate: %v", err)
	}
	// An unreadable stdin (pipe, /dev/null, EOF) means nobody is there to
	// solve anything: abort before touching the saved profile.
	if _, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
		return fmt.Errorf("no interactive input — solve in a terminal, or pass --no-assist")
	}
	cookies, err := dumpCookies(port)
	if err != nil {
		return fmt.Errorf("could not read solved session: %v", err)
	}
	p, err := loadProfile(profileName)
	if err != nil {
		p = &profile{Name: profileName}
	}
	p.merge(cookies)
	if err := saveProfile(p); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "solved session saved (%d cookies) — retrying headless…\n", len(cookies))
	return nil
}

func waitDebugger(port int, max time.Duration) (string, error) {
	deadline := time.Now().Add(max)
	for {
		if ws, err := debuggerURL(port); err == nil {
			return ws, nil
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("timeout")
		}
		time.Sleep(300 * time.Millisecond)
	}
}

func evalString(ctx context.Context, expr string) (string, error) {
	res, err := chromedp.Call(ctx, runtime.Evaluate, runtime.EvaluateParams{
		Expression:    expr,
		ReturnByValue: boolPtr(true),
	})
	if err != nil || res.Result == nil {
		return "", err
	}
	var s string
	if err := json.Unmarshal([]byte(res.Result.Value), &s); err != nil {
		return "", err
	}
	return s, nil
}

// challengeMarkers appear on bot-verification interstitials. A successful
// solve navigates away, so their disappearance means the real page loaded.
func challengePresent(ctx context.Context) bool {
	title, _ := evalString(ctx, "document.title")
	body, _ := evalString(ctx, "document.body ? document.body.innerText.slice(0, 500) : ''")
	hay := strings.ToLower(title + "\n" + body)
	for _, m := range []string{
		"just a moment", "performing security verification",
		"verifying you are human", "checking your browser",
	} {
		if strings.Contains(hay, m) {
			return true
		}
	}
	return false
}

func waitChallengeGone(ctx context.Context, max time.Duration) error {
	deadline := time.Now().Add(max)
	for {
		if !challengePresent(ctx) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("page stuck on bot verification (it may need an interactive solve)")
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting out bot verification")
		case <-time.After(time.Second):
		}
	}
}

func isChallengeMD(md string) bool {
	hay := strings.ToLower(md)
	for _, m := range []string{
		"just a moment", "performing security verification",
		"verifying you are human", "ray id:",
	} {
		if strings.Contains(hay, m) {
			return true
		}
	}
	return false
}

func waitReady(ctx context.Context, max time.Duration) error {
	deadline := time.Now().Add(max)
	for {
		s, err := evalString(ctx, "document.readyState")
		if err == nil && s == "complete" {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout waiting for page load")
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for page load")
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// streamDelta returns the newly appended suffix when text grew by
// extension, so streamed output never repeats or garbles rewrites.
func streamDelta(prev, text string) (string, bool) {
	if text == "" || len(text) <= len(prev) || !strings.HasPrefix(text, prev) {
		return "", false
	}
	return text[len(prev):], true
}

// waitTextStable waits until the visible text stops growing (streamed
// answers) or gives up and lets the caller dump whatever exists.
// --settle controls how long the text must hold still (default 6s).
// onDelta receives each newly streamed suffix so callers can show progress
// (stderr) while the final document still goes to stdout intact.
func waitTextStable(ctx context.Context, max, settle time.Duration, onDelta func(string)) {
	need := int(settle.Seconds() / 2)
	if need < 1 {
		need = 1
	}
	if need > 5 {
		need = 5
	}
	deadline := time.Now().Add(max)
	var last []int
	var prev string
	first := true
	for {
		text, n := "", -1
		if s, err := evalString(ctx, "document.body ? document.body.innerText : ''"); err == nil {
			text, n = s, len(s)
		}
		last = append(last, n)
		if len(last) > need {
			last = last[1:]
		}
		if !first && onDelta != nil {
			if delta, ok := streamDelta(prev, text); ok {
				onDelta(delta)
			}
		}
		first, prev = false, text
		stable := len(last) == need && n > 0
		for _, v := range last {
			if v != n {
				stable = false
			}
		}
		if stable {
			return
		}
		if time.Now().After(deadline) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
