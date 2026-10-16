package main

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var (
	_           = flag.Bool("md", true, "output markdown (default)")
	tuiMode     = flag.Bool("tui", false, "interactive TUI viewer")
	renderer    = flag.String("renderer", "auto", "fetch engine: auto|static|lightpanda")
	versionFlag = flag.Bool("version", false, "print version and exit")
	profileFlg  = flag.String("profile", "", "named cookie profile to send with the request")
	cookieFile  = flag.String("cookie-file", "", "cookie file to import (with: picu login)")
	useReal     = flag.Bool("use-real-profile", false, "fetch with your everyday Chrome profile: pages load with your real sessions, no per-site login, no need to quit Chrome")
	loginWait   = flag.Duration("login-timeout", 15*time.Second, "how long to wait for the login browser endpoint")
	noAssist    = flag.Bool("no-assist", false, "never open a visible browser to solve bot walls")
	settle      = flag.Duration("settle", 6*time.Second, "how long growing text must hold still (still text returns in under a second)")
	selectFlg   = flag.String("select", "", "CSS selector: extract only the first matching element (needs JS renderer)")
	extractFlg  = flag.String("extract", "", "extract subset: article|metadata (applies before budget)")
	schemaFile  = flag.String("schema", "", "validate output JSON against schema FILE (or 'default'), retry once")
	asJSON      = flag.Bool("json", false, "output schema-validated JSON")
	budget      = flag.Int("budget", 8000, "token budget for markdown output")
	jobs        = flag.Int("jobs", 4, "parallel fetch workers for multiple URLs")
	countFlg    = flag.Int("count", 10, "results for picu search (0 keeps all)")
	engineFlg   = flag.String("engine", "ddg", "search engine: ddg|google (google needs an accepted session)")
	doFlg       = flag.String("do", "", "browser actions: click|type|select|read|wait|scroll|press|back steps separated by ; (needs --renderer chrome)")
	sessionFlg  = flag.String("session", "default", "named browser session for open/snap/act/close")
	depthFlg    = flag.Int("depth", 1, "crawl link hops beyond the seed (0 = seed only)")
	pagesFlg    = flag.Int("pages", 30, "crawl total page cap")
	cacheTT     = flag.Duration("cache", 24*time.Hour, "cache TTL (0 disables)")
	timeout     = flag.Duration("timeout", 10*time.Second, "fetch timeout")
)

var (
	reTitle   = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	reTags    = regexp.MustCompile(`(?s)<[^>]+>`)
	reSpace   = regexp.MustCompile(`[ \t]+\n|\n{3,}`)
	blockKeys = []string{"just a moment", "enable javascript", "captcha", "cloudflare", "access denied"}
)

func main() {
	os.Args = append([]string{os.Args[0]}, reorderArgs(os.Args[1:])...)
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: picu [flags] example.com/article [URL2 …]\n\n  bare domains work: scheme defaults to https, flags may go before or after the URL.\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if *versionFlag {
		fmt.Printf("picu %s\n", picuVersion)
		return
	}
	if *tuiMode {
		initial := ""
		if flag.NArg() == 1 {
			initial = normalizeURL(flag.Arg(0))
		} else if flag.NArg() > 1 {
			fmt.Fprintln(os.Stderr, "--tui takes at most one URL")
			flag.Usage()
			os.Exit(2)
		}
		if err := runTUI(initial, currentOpts()); err != nil {
			fmt.Fprintf(os.Stderr, "tui: %v (needs a real terminal — try it outside this runner)\n", err)
			os.Exit(1)
		}
		return
	}
	if flag.NArg() >= 1 {
		switch flag.Arg(0) {
		case "login":
			site := ""
			if flag.NArg() == 2 {
				site = normalizeURL(flag.Arg(1))
			} else if flag.NArg() > 2 {
				flag.Usage()
				os.Exit(2)
			}
			runLogin(site)
			return
		case "profiles":
			runProfiles()
			return
		case "logout":
			runLogout()
			return
		case "doctor":
			runDoctor()
			return
		case "search":
			runSearchCmd(flag.Args()[1:])
			return
		case "open":
			runOpen(flag.Args()[1:], currentOpts())
			return
		case "snap":
			runSnap(currentOpts())
			return
		case "act":
			runAct(flag.Args()[1:], currentOpts())
			return
		case "close":
			runClose()
			return
		case "crawl":
			runCrawl(flag.Args()[1:], currentOpts())
			return
		case "sessions":
			runSessions()
			return
		}
	}
	if flag.NArg() < 1 {
		flag.Usage()
		os.Exit(2)
	}
	opts := currentOpts()
	if flag.NArg() > 1 {
		runBatch(flag.Args(), opts)
		return
	}
	url := normalizeURL(flag.Arg(0))
	md, title, cached, via, err := get(url, opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	md = applyBudget(md, *budget)
	doc := func() map[string]any {
		return map[string]any{
			"url": url, "title": title, "markdown": md,
			"chars": len(md), "cached": cached, "renderer": via,
			"profile": *profileFlg,
		}
	}
	if *schemaFile != "" {
		if bad := checkDoc(doc()); len(bad) > 0 {
			fmt.Fprintf(os.Stderr, "schema check failed (%s) — refetching fresh…\n", strings.Join(bad, "; "))
			saved := opts.cacheTTL
			opts.cacheTTL = 0
			md, title, cached, via, err = get(url, opts)
			opts.cacheTTL = saved
			if err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				os.Exit(1)
			}
			md = applyBudget(md, *budget)
		}
		if bad := checkDoc(doc()); len(bad) > 0 {
			fmt.Fprintf(os.Stderr, "invalid output:\n")
			for _, b := range bad {
				fmt.Fprintf(os.Stderr, "  - %s\n", b)
			}
			os.Exit(1)
		}
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		_ = enc.Encode(doc())
		return
	}
	fmt.Printf("# %s\n\nSource: %s (via %s)\n\n%s\n", title, url, via, md)
}

// checkDoc validates the output envelope against --schema FILE, or a
// built-in shape requiring non-empty markdown when "default".
func checkDoc(doc map[string]any) []string {
	var raw []byte
	if *schemaFile == "default" {
		raw = schemaDefault()
	} else {
		var err error
		raw, err = os.ReadFile(*schemaFile)
		if err != nil {
			return []string{fmt.Sprintf("cannot read schema file: %v", err)}
		}
	}
	bad, err := validateDoc(raw, doc)
	if err != nil {
		return []string{err.Error()}
	}
	return bad
}

func get(url string, o fetchOpts) (md, title string, cached bool, via string, err error) {
	want := o.renderer
	if want != "static" && want != "lightpanda" && want != "chrome" && want != "auto" {
		return "", "", false, "", fmt.Errorf("unknown --renderer %q (auto|static|lightpanda|chrome)", want)
	}
	if err := checkExtract(o.extract); err != nil {
		return "", "", false, "", err
	}
	if *useReal && (want == "static" || want == "lightpanda") {
		return "", "", false, "", fmt.Errorf("--use-real-profile borrows the Chrome session, so it needs --renderer chrome (or auto)")
	}
	if o.do != "" && want != "chrome" {
		return "", "", false, "", fmt.Errorf("--do runs scripted browser actions, so it needs --renderer chrome")
	}
	if want == "lightpanda" {
		return renderViaLightpanda(url, o)
	}
	if want == "chrome" {
		if *useReal {
			return renderViaBorrowed(url, o)
		}
		return renderViaChrome(url, o)
	}
	if want == "static" && o.selector != "" {
		return "", "", false, "", fmt.Errorf("--select needs a JS renderer: use --renderer lightpanda|chrome (or drop --renderer)")
	}
	var html string
	var staticBlocked error
	if o.selector == "" {
		if o.cacheTTL > 0 {
			if b, t, ok := loadCache(url, "static", o); ok {
				return b, t, true, "static", nil
			}
		}
		var err error
		var status int
		html, status, err = staticFetch(url, o)
		_ = status
		if err != nil {
			if want == "auto" && isBlockedErr(err) {
				fmt.Fprintf(os.Stderr, "static %v — trying JS renderers\n", err)
				staticBlocked = err
			} else {
				return "", "", false, "", err
			}
		} else {
			full := toMarkdown(html)
			md = applyExtract(html, full, o.extract)
			if !isJSShell(html, full) {
				title = "untitled"
				if m := reTitle.FindStringSubmatch(html); len(m) == 2 {
					title = strings.TrimSpace(reTags.ReplaceAllString(m[1], ""))
				}
				if o.cacheTTL > 0 {
					saveCache(url, "static", md, title, o)
				}
				return md, title, false, "static", nil
			}
		}
	} else {
		fmt.Fprintln(os.Stderr, "--select needs a JS renderer — skipping static fetch")
	}
	if want == "auto" {
		_, hasLightpanda := findLightpanda()
		hasWebdata := false
		if o.profile != "" {
			wd, werr := webdataDir(o.profile)
			if werr != nil {
				return "", "", false, "", werr
			}
			if st, err := os.Stat(filepath.Join(wd, "Default")); err == nil && st.IsDir() {
				hasWebdata = true
			}
		}
		plan := renderPlan(o.profile != "", hasWebdata, hasLightpanda)
		if *useReal {
			plan = []string{"chrome"}
		}
		fmt.Fprintf(os.Stderr, "renderer plan: %s\n", strings.Join(plan, " → "))
		for _, engine := range plan {
			switch engine {
			case "chrome":
				if cmd, ctitle, ccached, _, cerr := renderViaChrome(url, o); cerr == nil {
					return cmd, ctitle, ccached, "chrome", nil
				} else {
					fmt.Fprintln(os.Stderr, "chrome render: "+cerr.Error())
				}
			case "lightpanda":
				if lmd, ltitle, lcached, _, lerr := renderViaLightpanda(url, o); lerr == nil {
					return lmd, ltitle, lcached, "lightpanda", nil
				} else {
					fmt.Fprintln(os.Stderr, "lightpanda render: "+lerr.Error())
				}
			}
		}
		if md == "" {
			if staticBlocked != nil {
				return "", "", false, "", staticBlocked
			}
			return "", "", false, "", fmt.Errorf("no JS renderer available (install lightpanda or log in a profile for chrome)")
		}
		fmt.Fprintln(os.Stderr, "no JS renderer available — returning thin static output")
	}
	title = "untitled"
	if m := reTitle.FindStringSubmatch(html); len(m) == 2 {
		title = strings.TrimSpace(reTags.ReplaceAllString(m[1], ""))
	}
	if o.cacheTTL > 0 {
		saveCache(url, "static", md, title, o)
	}
	return md, title, false, "static", nil
}

// isBlockedErr reports a static-fetch refusal (HTTP 403/429 or a
// recognized bot-wall page) as opposed to a transport failure.
func isBlockedErr(err error) bool {
	return err != nil && strings.HasPrefix(err.Error(), "blocked:")
}

func renderViaLightpanda(url string, o fetchOpts) (md, title string, cached bool, via string, err error) {
	if o.cacheTTL > 0 {
		if b, t, ok := loadCache(url, "lightpanda", o); ok {
			return b, t, true, "lightpanda", nil
		}
	}
	html, err := lightpandaFetchHTML(url, 1<<20, 3*o.timeout, o)
	if err != nil {
		return "", "", false, "", err
	}
	title, md, err = docFromHTML(html)
	if err != nil {
		return "", "", false, "", err
	}
	md = applyExtract(html, md, o.extract)
	if o.cacheTTL > 0 {
		saveCache(url, "lightpanda", md, title, o)
	}
	return md, title, false, "lightpanda", nil
}

// docFromHTML converts rendered HTML to the output document, rejecting
// bot-verification pages so they are never cached or served as content.
func docFromHTML(html string) (title, md string, err error) {
	title = "untitled"
	if m := reTitle.FindStringSubmatch(html); len(m) == 2 {
		title = strings.TrimSpace(reTags.ReplaceAllString(m[1], ""))
	}
	md = toMarkdown(html)
	if isChallengeMD(md) {
		return "", "", fmt.Errorf("page stuck on bot verification (it may need an interactive solve)")
	}
	return title, md, nil
}

func renderViaChrome(url string, o fetchOpts) (md, title string, cached bool, via string, err error) {
	if o.cacheTTL > 0 {
		if b, t, ok := loadCache(url, "chrome", o); ok {
			return b, t, true, "chrome", nil
		}
	}
	html, err := chromeHTML(url, o)
	if err != nil {
		return "", "", false, "", err
	}
	title, md, err = docFromHTML(html)
	if err != nil {
		return "", "", false, "", err
	}
	md = applyExtract(html, md, o.extract)
	if o.cacheTTL > 0 {
		saveCache(url, "chrome", md, title, o)
	}
	return md, title, false, "chrome", nil
}

// chromeHTML renders with headless Chrome on the profile snapshot, or on
// a borrow of the everyday profile under --use-real-profile.
func chromeHTML(url string, o fetchOpts) (string, error) {
	if *useReal {
		realDir, ok := realUserDataDir()
		if !ok {
			return "", fmt.Errorf("no everyday Chrome profile found (set PICU_CHROME_PROFILE to point at one)")
		}
		// Live copy with journal companions: safe while Chrome runs,
		// so no quit is ever needed.
		tmp, cleanup, err := borrowForFetch(realDir)
		if err != nil {
			return "", err
		}
		defer cleanup()
		return renderHeadless(url, "", tmp, 3*o.timeout, o, false)
	}
	return chromeFetchHTML(url, o.profile, 3*o.timeout, o)
}

// renderViaBorrowed renders with headless Chrome on a temp copy of the
// everyday browser profile, so pages load with the user's real sessions
// (including OAuth) and no per-site login. The original profile is never
// touched; the copy is removed afterwards.
func renderViaBorrowed(url string, o fetchOpts) (md, title string, cached bool, via string, err error) {
	const v = "chrome-real"
	if o.cacheTTL > 0 {
		if b, t, ok := loadCache(url, v, o); ok {
			return b, t, true, v, nil
		}
	}
	html, err := chromeHTML(url, o)
	if err != nil {
		return "", "", false, "", err
	}
	title, md, err = docFromHTML(html)
	if err != nil {
		return "", "", false, "", err
	}
	md = applyExtract(html, md, o.extract)
	if o.cacheTTL > 0 {
		saveCache(url, v, md, title, o)
	}
	return md, title, false, v, nil
}

// sharedHTTPClient reuses TCP/TLS connections across fetches in one run.
// Timeouts ride the request context so a single client serves every
// budget; a fresh client per fetch would redo TLS on every URL.
var sharedHTTPClient = &http.Client{}

func staticFetch(url string, o fetchOpts) (html string, status int, err error) {
	ctx := context.Background()
	if o.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, o.timeout)
		defer cancel()
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	req.Header.Set("User-Agent", "picu/"+picuVersion+" (+agent-research)")
	if o.profile != "" {
		if p, err := loadProfile(o.profile); err != nil {
			return "", 0, err
		} else if hdr := p.headerFor(url); hdr != "" {
			req.Header.Set("Cookie", hdr)
		}
	}
	resp, err := sharedHTTPClient.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 403 || resp.StatusCode == 429 {
		return "", resp.StatusCode, fmt.Errorf("blocked: HTTP %d — retry with --profile plus --renderer chrome|lightpanda", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 5<<20))
	if err != nil {
		return "", resp.StatusCode, err
	}
	html = string(body)
	lower := strings.ToLower(html)
	for _, k := range blockKeys {
		if strings.Contains(lower, k) && len(html) < 20000 {
			return "", resp.StatusCode, fmt.Errorf("blocked: suspected bot-wall (%q) — retry with --profile plus --renderer chrome|lightpanda", k)
		}
	}
	return html, resp.StatusCode, nil
}

var skipBlocks = map[string]bool{
	"script": true, "style": true, "noscript": true, "nav": true,
	"footer": true, "template": true, "svg": true,
}

var breakBefore = map[string]bool{
	"p": true, "div": true, "section": true, "article": true,
	"blockquote": true, "ul": true, "ol": true, "li": true,
	"table": true, "tr": true, "br": true, "hr": true, "pre": true,
}

var reHref = regexp.MustCompile(`(?i)href\s*=\s*("[^"]*"|'[^']*'|[^\s>]+)`)

// toMarkdown converts HTML to readable markdown in a single pass: text is
// emitted as seen, block tags become line breaks, links keep their targets,
// and script/style/nav/footer subtrees are skipped outright.
func toMarkdown(html string) string {
	var b strings.Builder
	skip, depth := "", 0
	linkHref := ""
	i := 0
	for i < len(html) {
		if html[i] != '<' {
			j := i
			for j < len(html) && html[j] != '<' {
				j++
			}
			if skip == "" {
				b.WriteString(html[i:j])
			}
			i = j
			continue
		}
		end := scanTagEnd(html, i)
		if end < 0 {
			if skip == "" {
				b.WriteString(html[i:])
			}
			break
		}
		raw := html[i+1 : end]
		i = end + 1
		if strings.HasPrefix(raw, "!--") {
			continue
		}
		closing := strings.HasPrefix(raw, "/")
		name := tagName(raw)
		if name == "" {
			continue
		}
		if skip != "" {
			if !closing && name == skip {
				depth++
			} else if closing && name == skip {
				depth--
				if depth == 0 {
					skip = ""
				}
			}
			continue
		}
		if !closing && skipBlocks[name] {
			skip, depth = name, 1
			continue
		}
		switch {
		case name == "a" && !closing:
			href := ""
			if m := reHref.FindStringSubmatch(raw); m != nil {
				href = strings.Trim(m[1], `"'`)
			}
			if href != "" {
				b.WriteString("[")
				linkHref = href
			}
		case name == "a" && closing:
			if linkHref != "" {
				b.WriteString("](" + linkHref + ")")
				linkHref = ""
			}
		case name[0] == 'h' && len(name) == 2 && name[1] >= '1' && name[1] <= '6':
			if !closing {
				b.WriteString("\n" + strings.Repeat("#", int(name[1]-'0')) + " ")
			} else {
				b.WriteString("\n")
			}
		case name == "li" && !closing:
			b.WriteString("\n- ")
		case breakBefore[name]:
			b.WriteString("\n")
		}
	}
	s := htmlUnescape(b.String())
	lines := strings.Split(s, "\n")
	var out []string
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		out = append(out, l)
	}
	s = strings.Join(out, "\n")
	return reSpace.ReplaceAllString(stripBanners(s), "\n\n")
}

// scanTagEnd finds the '>' closing the tag at s[i]=='<', skipping quoted
// attribute values that may contain '>'. Returns -1 when unterminated.
func scanTagEnd(s string, i int) int {
	var quote byte
	for j := i + 1; j < len(s); j++ {
		c := s[j]
		if quote != 0 {
			if c == quote {
				quote = 0
			}
			continue
		}
		if c == '"' || c == '\'' {
			quote = c
		} else if c == '>' {
			return j
		}
	}
	return -1
}

func tagName(raw string) string {
	raw = strings.TrimPrefix(raw, "/")
	var b strings.Builder
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' {
			b.WriteByte(c)
		} else if c >= 'A' && c <= 'Z' {
			b.WriteByte(c + ('a' - 'A'))
		} else {
			break
		}
	}
	return b.String()
}

// stripBanners drops cookie-consent boilerplate lines that otherwise eat
// the token budget on every gated site.
func stripBanners(s string) string {
	bannerLine := map[string]bool{"allow all": true, "only necessary": true, "cookie policy": true}
	var keep []string
	for _, l := range strings.Split(s, "\n") {
		low := strings.ToLower(strings.TrimSpace(l))
		if bannerLine[low] || strings.Contains(low, "we use cookies, pixels") {
			continue
		}
		keep = append(keep, l)
	}
	return strings.Join(keep, "\n")
}

var reNumEntity = regexp.MustCompile(`&#(x[0-9a-fA-F]+|[0-9]+);`)

func htmlUnescape(s string) string {
	for _, r := range [][2]string{
		{"&amp;", "&"}, {"&lt;", "<"}, {"&gt;", ">"}, {"&quot;", `"`}, {"&#39;", "'"}, {"&nbsp;", " "},
	} {
		s = strings.ReplaceAll(s, r[0], r[1])
	}
	return reNumEntity.ReplaceAllStringFunc(s, func(m string) string {
		num := m[2 : len(m)-1]
		var n int
		if strings.HasPrefix(num, "x") {
			fmt.Sscanf(num[1:], "%x", &n)
		} else {
			fmt.Sscanf(num, "%d", &n)
		}
		if n <= 0 || n > 0x10FFFF {
			return m
		}
		return string(rune(n))
	})
}

func applyBudget(md string, tokens int) string {
	maxChars := tokens * 4
	if len(md) <= maxChars {
		return md
	}
	cut := strings.LastIndex(md[:maxChars], "\n")
	if cut < 0 {
		cut = maxChars
	}
	return md[:cut] + "\n\n…(truncated to budget)"
}

// renderPlan lists fallback engines after a thin static fetch, in order:
// lightpanda reads fast with the profile's session cookies, chrome is the
// solver fallback for pages that challenge lightpanda. Chrome needs a
// profile with saved session files; lightpanda just needs its binary.
// Called only when static output looked like a JS shell.
func renderPlan(hasProfile, hasWebdata, hasLightpanda bool) []string {
	var plan []string
	if hasLightpanda {
		plan = append(plan, "lightpanda")
	}
	if hasProfile && hasWebdata {
		plan = append(plan, "chrome")
	}
	return plan
}

func normalizeURL(raw string) string {
	if strings.Contains(raw, "://") {
		return raw
	}
	return "https://" + raw
}

var valueFlags = map[string]bool{"budget": true, "cache": true, "timeout": true, "renderer": true, "profile": true, "cookie-file": true, "login-timeout": true, "settle": true, "select": true, "schema": true, "jobs": true, "extract": true, "count": true, "engine": true, "do": true, "session": true, "depth": true, "pages": true}

// fetchOpts is the fetch pipeline's configuration, snapshotted from flags
// once per run so pipeline functions take it explicitly instead of reading
// globals. Login/TUI chrome flags stay on the flag set directly.
type fetchOpts struct {
	renderer, profile, selector, extract, do string
	budget                                   int
	cacheTTL, timeout, settle                time.Duration
}

func currentOpts() fetchOpts {
	return fetchOpts{
		renderer: *renderer, profile: *profileFlg, selector: *selectFlg, extract: *extractFlg, do: *doFlg,
		budget: *budget, cacheTTL: *cacheTT, timeout: *timeout, settle: *settle,
	}
}

// reorderArgs moves flags before positionals so `web example.com --json`
// works the same as `picu --json example.com` with the stdlib flag package.
func reorderArgs(args []string) []string {
	var flags, rest []string
	for i := 0; i < len(args); {
		a := args[i]
		if a == "--" {
			rest = append(rest, args[i+1:]...)
			break
		}
		if len(a) > 1 && strings.HasPrefix(a, "-") {
			name := strings.TrimLeft(a, "-")
			if eq := strings.Index(name, "="); eq >= 0 {
				name = name[:eq]
			}
			flags = append(flags, a)
			if valueFlags[name] && eqAbsent(a) && i+1 < len(args) {
				flags = append(flags, args[i+1])
				i += 2
				continue
			}
			i++
			continue
		}
		rest = append(rest, a)
		i++
	}
	return append(flags, rest...)
}

func eqAbsent(a string) bool { return strings.Index(a, "=") < 0 }

// cacheVersion bumps whenever the fetch/convert pipeline changes shape,
// so old entries can never masquerade as fresh output.
const cacheVersion = "v5"

func cachePath(url, via string, o fetchOpts) string {
	h := sha1.Sum([]byte(cacheVersion + "|" + via + "|" + o.selector + "|" + o.extract + "|" + o.profile + "|" + url))
	dir, _ := os.UserCacheDir()
	if dir == "" {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "picu", hex.EncodeToString(h[:])+".json")
}

func loadCache(url, via string, o fetchOpts) (md, title string, ok bool) {
	p := cachePath(url, via, o)
	st, err := os.Stat(p)
	if err != nil || time.Since(st.ModTime()) > o.cacheTTL {
		return "", "", false
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", "", false
	}
	var v struct {
		MD, Title string
	}
	if json.Unmarshal(b, &v) != nil {
		return "", "", false
	}
	return v.MD, v.Title, true
}

func saveCache(url, via, md, title string, o fetchOpts) {
	p := cachePath(url, via, o)
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	b, _ := json.Marshal(map[string]string{"md": md, "title": title})
	_ = os.WriteFile(p, b, 0o600)
}
