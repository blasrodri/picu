package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

func findLightpanda() (string, bool) {
	if p := os.Getenv("LIGHTPANDA_BIN"); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p, true
		}
		return "", false
	}
	p, err := exec.LookPath("lightpanda")
	if err != nil {
		return "", false
	}
	return p, true
}

// lightpandaFetchHTML dumps rendered HTML via the sidecar binary.
// No CDP client needed: `lightpanda fetch` does load + serialize.
func lightpandaFetchHTML(url string, maxBytes int, timeout time.Duration, o fetchOpts) (string, error) {
	bin, ok := findLightpanda()
	if !ok {
		return "", fmt.Errorf("lightpanda not found (set LIGHTPANDA_BIN or put it on PATH)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	args := []string{"fetch", url, "--dump", "html",
		"--dump-max-bytes", fmt.Sprint(maxBytes),
		"--wait-until", "networkidle", "--wait-ms", "15000"}
	if o.selector != "" {
		args = append(args, "--dump-selector", o.selector)
	}
	var jar string
	profName := o.profile
	if profName != "" {
		p, err := loadProfile(profName)
		if err != nil {
			return "", err
		}
		cookieTmp, err := os.CreateTemp("", "picu-cookies-*.json")
		if err != nil {
			return "", err
		}
		defer os.Remove(cookieTmp.Name())
		if err := p.cdpFile(cookieTmp.Name()); err != nil {
			return "", err
		}
		cookieTmp.Close()
		jarTmp, err := os.CreateTemp("", "picu-cookiejar-*.json")
		if err != nil {
			return "", err
		}
		jarTmp.Close()
		jar = jarTmp.Name()
		defer os.Remove(jar)
		args = append(args, "--cookie", cookieTmp.Name(), "--cookie-jar", jar)
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	var out, errBuf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errBuf
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("lightpanda fetch: %v (%s)", err, strings.TrimSpace(errBuf.String()))
	}
	if jar != "" {
		if b, err := os.ReadFile(jar); err == nil && len(bytes.TrimSpace(b)) > 0 {
			if fresh, err := parseCDPCookies(b); err == nil && len(fresh) > 0 {
				if p, err := loadProfile(profName); err == nil {
					p.merge(fresh)
					_ = saveProfile(p)
				}
			}
		}
	}
	return out.String(), nil
}

// isJSShell reports a 200 page that is really an empty JS mount point.
func isJSShell(html, md string) bool {
	if len(strings.TrimSpace(md)) > 800 {
		return false
	}
	lower := strings.ToLower(html)
	markers := []string{
		`id="root"`, `id="app"`, `__next_data__`, `__nuxt__`,
		`enable javascript`, `requires javascript`,
	}
	for _, m := range markers {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return len(html) > 20000 && len(strings.TrimSpace(md)) < 200
}
