package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type storedCookie struct {
	Name, Value, Domain, Path string
	Expires                   float64
	HttpOnly, Secure          bool
	SameSite                  string
}

type profile struct {
	Name    string
	Cookies []storedCookie
	Updated time.Time
}

func configDir() string {
	if d := os.Getenv("PICU_CONFIG_HOME"); d != "" {
		return d
	}
	dir, _ := os.UserConfigDir()
	if dir == "" {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "picu")
}

func profilePath(name string) (string, error) {
	if err := validateProfileName(name); err != nil {
		return "", err
	}
	return filepath.Join(configDir(), "profiles", name+".json"), nil
}

// validateProfileName keeps profile names inside the profiles directory:
// letters, digits, dash and underscore only, so "../" escapes are rejected
// before any filesystem use.
func validateProfileName(name string) error {
	if len(name) == 0 || len(name) > 64 {
		return fmt.Errorf("invalid profile name %q: use 1-64 letters, digits, - and _", name)
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' {
			continue
		}
		return fmt.Errorf("invalid profile name %q: use 1-64 letters, digits, - and _", name)
	}
	return nil
}

func loadProfile(name string) (*profile, error) {
	path, err := profilePath(name)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("profile %q not found — run: picu login %s --profile %s --cookie-file cookies.txt", name, "<site>", name)
	}
	var p profile
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("profile %q is corrupt: %v", name, err)
	}
	return &p, nil
}

func saveProfile(p *profile) error {
	path, err := profilePath(p.Name)
	if err != nil {
		return err
	}
	p.Updated = time.Now()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(p, "", "  ")
	return os.WriteFile(path, b, 0o600)
}

// headerFor builds a Cookie header from cookies matching the URL. Expired
// cookies are never sent, and Secure cookies only go over https.
func (p *profile) headerFor(target string) string {
	u, err := url.Parse(target)
	if err != nil {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	secureCtx := strings.EqualFold(u.Scheme, "https")
	now := float64(time.Now().Unix())
	var parts []string
	for _, c := range p.Cookies {
		if c.Expires > 0 && c.Expires <= now {
			continue
		}
		if c.Secure && !secureCtx {
			continue
		}
		d := strings.ToLower(strings.TrimPrefix(c.Domain, "."))
		if host != d && !strings.HasSuffix(host, "."+d) {
			continue
		}
		if c.Path != "" && !strings.HasPrefix(u.Path, c.Path) {
			continue
		}
		parts = append(parts, c.Name+"="+c.Value)
	}
	return strings.Join(parts, "; ")
}

// merge replaces cookies by name|domain|path, keeping the rest.
func (p *profile) merge(in []storedCookie) {
	idx := map[string]int{}
	for i, c := range p.Cookies {
		idx[c.Name+"|"+c.Domain+"|"+c.Path] = i
	}
	for _, c := range in {
		k := c.Name + "|" + c.Domain + "|" + c.Path
		if i, ok := idx[k]; ok {
			p.Cookies[i] = c
		} else {
			idx[k] = len(p.Cookies)
			p.Cookies = append(p.Cookies, c)
		}
	}
}

// importCookieFile detects Netscape vs CDP/Puppeteer JSON and stores it.
func importCookieFile(name, path string) (int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	var cookies []storedCookie
	if isJSONCookieFile(b) {
		cookies, err = parseCDPCookies(b)
	} else {
		cookies, err = parseNetscapeCookies(string(b)), nil
	}
	if err != nil {
		return 0, fmt.Errorf("cannot parse %s: %v", path, err)
	}
	if len(cookies) == 0 {
		return 0, fmt.Errorf("no cookies found in %s", path)
	}
	p := &profile{Name: name}
	if existing, err := loadProfile(name); err == nil {
		p = existing
	}
	p.merge(cookies)
	return len(cookies), saveProfile(p)
}

func isJSONCookieFile(b []byte) bool {
	for _, c := range b {
		if c == ' ' || c == '\n' || c == '\t' || c == '\r' {
			continue
		}
		return c == '['
	}
	return false
}

func parseCDPCookies(b []byte) ([]storedCookie, error) {
	var raw []struct {
		Name, Value, Domain, Path string
		Expires                   float64
		HttpOnly, Secure          bool
		SameSite                  string
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	var out []storedCookie
	for _, c := range raw {
		if c.Name == "" {
			continue
		}
		if c.Path == "" {
			c.Path = "/"
		}
		out = append(out, storedCookie{c.Name, c.Value, c.Domain, c.Path, c.Expires, c.HttpOnly, c.Secure, c.SameSite})
	}
	return out, nil
}

func parseNetscapeCookies(s string) []storedCookie {
	var out []storedCookie
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") && !strings.HasPrefix(line, "#HttpOnly") {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) < 7 {
			continue
		}
		var expires float64
		fmt.Sscanf(f[4], "%f", &expires)
		out = append(out, storedCookie{
			Domain: f[0], Path: f[2], Name: f[5], Value: f[6],
			Expires: expires, Secure: f[3] == "TRUE",
		})
	}
	return out
}

func runLogin(site string) {
	if *profileFlg == "" {
		fatal("login needs --profile NAME, e.g. picu login --profile pplx perplexity.ai")
	}
	if err := validateProfileName(*profileFlg); err != nil {
		fatal(err.Error())
	}
	if *cookieFile != "" {
		n, err := importCookieFile(*profileFlg, *cookieFile)
		if err != nil {
			fatal(err.Error())
		}
		fmt.Printf("profile %q: imported %d cookies from %s\n", *profileFlg, n, *cookieFile)
		return
	}
	if site == "" {
		fatal("give a site or a file: picu login --profile pplx perplexity.ai  OR  picu login --profile pplx --cookie-file cookies.txt")
	}
	if *useReal {
		realDir, ok := realUserDataDir()
		if !ok {
			fatal("no everyday Chrome profile found (set PICU_CHROME_PROFILE to point at one)")
		}
		if chromeRunning() {
			fatal("Chrome is running and owns its profile — quit Chrome completely, then rerun this command")
		}
		fmt.Printf("borrowing session from %s via temp copy (original untouched)\n", realDir)
		tmp, cleanup, err := copyMinimalProfile(realDir)
		if err != nil {
			fatal(err.Error())
		}
		defer cleanup()
		runInteractiveLogin(site, *profileFlg, tmp, true)
		return
	}
	runInteractiveLogin(site, *profileFlg, filepath.Join(configDir(), "profiles", *profileFlg+".browser"), false)
}

func runProfiles() {
	dir := filepath.Join(configDir(), "profiles")
	entries, _ := os.ReadDir(dir)
	if len(entries) == 0 {
		fmt.Println("no profiles — run: picu login --profile NAME SITE  (or --cookie-file cookies.txt)")
		return
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		if p, err := loadProfile(strings.TrimSuffix(e.Name(), ".json")); err == nil {
			fmt.Printf("%-12s %3d cookies  updated %s\n", p.Name, len(p.Cookies), p.Updated.Format("2006-01-02 15:04"))
		}
	}
}

func runLogout() {
	if *profileFlg == "" {
		fatal("logout needs --profile NAME")
	}
	path, err := profilePath(*profileFlg)
	if err != nil {
		fatal(err.Error())
	}
	if err := os.Remove(path); err != nil {
		fatal(fmt.Sprintf("no profile %q", *profileFlg))
	}
	fmt.Printf("profile %q deleted\n", *profileFlg)
}

func fatal(msg string) {
	fmt.Fprintln(os.Stderr, "error: "+msg)
	os.Exit(1)
}

// cdpFile writes the profile in CDP/Puppeteer format for lightpanda --cookie.
// expires <= 0 becomes -1 (session cookie); a literal 0 would mean 1970.
func (p *profile) cdpFile(path string) error {
	type cdp struct {
		Name, Value, Domain, Path string
		Expires                   float64 `json:"expires"`
		HttpOnly                  bool    `json:"httpOnly"`
		Secure                    bool    `json:"secure"`
		SameSite                  string  `json:"sameSite,omitempty"`
	}
	out := make([]cdp, 0, len(p.Cookies))
	for _, c := range p.Cookies {
		exp := c.Expires
		if exp <= 0 {
			exp = -1
		}
		out = append(out, cdp{c.Name, c.Value, c.Domain, c.Path, exp, c.HttpOnly, c.Secure, c.SameSite})
	}
	b, _ := json.Marshal(out)
	return os.WriteFile(path, b, 0o600)
}
