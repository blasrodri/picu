package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/remote"
)

// Sessions give agents a persistent browser across invocations: open once,
// then snap and act in a loop. The browser is the profile daemon, so state
// (cookies, history, page) survives between commands; an idle browser is
// reaped as usual and the next command reports the session expired.

type browserSession struct {
	Name    string `json:"name"`
	Target  string `json:"target"`
	Profile string `json:"profile"`
}

func sessionPath(name string) (string, error) {
	if err := validateProfileName(name); err != nil {
		return "", err
	}
	return filepath.Join(configDir(), "sessions", name+".json"), nil
}

func loadSession(name string) (*browserSession, error) {
	p, err := sessionPath(name)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("no session %q — run: picu open --profile NAME URL", name)
	}
	var s browserSession
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("session %q is corrupt: %v", name, err)
	}
	return &s, nil
}

func saveSession(s *browserSession) error {
	p, err := sessionPath(s.Name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(s, "", "  ")
	return os.WriteFile(p, b, 0o600)
}

func removeSession(name string) {
	if p, err := sessionPath(name); err == nil {
		_ = os.Remove(p)
	}
}

// snapEl is one interactive element: ref for the agent, selector for act.
type snapEl struct {
	Ref      int    `json:"ref"`
	Role     string `json:"role"`
	Name     string `json:"name"`
	Selector string `json:"selector"`
}

// snapshotJS lists up to 100 visible actionable elements as JSON with a
// synthesized CSS selector each: links, controls, small or named images
// (map markers, icons), titled elements, dialog/menu/tooltip roles
// (popups), then short leaf div/span labels outside controls (marker
// pins, popup lines). Pure DOM read, no clicks.
const snapshotJS = `(()=>{function css(e){if(e.id)return '#'+CSS.escape(e.id);const p=[];while(e&&e.nodeType===1&&p.length<6){let s=e.tagName.toLowerCase();const sib=e.parentNode?Array.from(e.parentNode.children).filter(c=>c.tagName===e.tagName):[];if(sib.length>1)s+=':nth-of-type('+(sib.indexOf(e)+1)+')';p.unshift(s);e=e.parentNode}return p.join(' > ')}
const els=[];const seen=new Set();
for(const el of document.querySelectorAll('a[href],button,input,select,textarea,img,[title],[role=button],[role=link],[role=dialog],[role=menu],[role=tooltip],[tabindex]')){if(els.length>=100)break;const r=el.getBoundingClientRect();if(r.width===0&&r.height===0)continue;if(el.tagName==='IMG'&&!el.alt&&!el.title&&!el.getAttribute('aria-label')&&(r.width>64||r.height>64))continue;const choice=el.tagName==='SELECT'?(el.options[el.selectedIndex]?el.options[el.selectedIndex].text:'') :'';const name=(choice||el.innerText||el.value||el.alt||el.getAttribute('aria-label')||el.getAttribute('title')||el.getAttribute('placeholder')||el.name||el.tagName).trim().replace(/\s+/g,' ').slice(0,80);const sel=css(el);if(seen.has(sel))continue;seen.add(sel);els.push({ref:els.length,role:(el.getAttribute('role')||el.tagName).toLowerCase(),name:name,selector:sel})}
for(const el of document.querySelectorAll('div,span')){if(els.length>=100)break;if(el.children.length>0)continue;const t=(el.innerText||'').trim().replace(/\s+/g,' ');if(!t||t.length>120)continue;if(el.closest('a,button,select,input,textarea,label,[role=button],[role=link],[role=dialog],[role=menu],[role=tooltip]'))continue;const r2=el.getBoundingClientRect();if(r2.width===0&&r2.height===0)continue;const sel2=css(el);if(seen.has(sel2))continue;seen.add(sel2);els.push({ref:els.length,role:el.tagName.toLowerCase(),name:t.slice(0,80),selector:sel2})}return JSON.stringify(els)})()`

func snapshotElements(eval func(string) (string, error)) ([]snapEl, error) {
	raw, err := eval(snapshotJS)
	if err != nil {
		return nil, err
	}
	var els []snapEl
	if err := json.Unmarshal([]byte(raw), &els); err != nil {
		return nil, fmt.Errorf("snapshot parse: %v", err)
	}
	return els, nil
}

func formatSnapshot(url string, els []snapEl) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# page: %s (%d interactive)\n", url, len(els))
	for _, e := range els {
		fmt.Fprintf(&b, "[%d] %s %q — %s\n", e.Ref, e.Role, e.Name, e.Selector)
	}
	return b.String()
}

// sessionCtx attaches to the session's target on the profile daemon.
// Sessions need persistence, so borrowed temp profiles are refused.
//
// It returns the three cancels separately: callers must defer each of them
// (task, allocator, parent — LIFO like renderHeadless). A single combined
// closure overflows the stack under chromedp v0.21's remote allocator, so
// do not recombine them.
func sessionCtx(s *browserSession, o fetchOpts) (eval func(string) (string, error), cancelTask, cancelAlloc, cancelParent context.CancelFunc, err error) {
	if *useReal {
		return nil, nil, nil, nil, fmt.Errorf("sessions need a snapshot profile, not --use-real-profile (borrowed copies cannot persist)")
	}
	profile := o.profile
	if profile == "" {
		profile = s.Profile
	}
	if profile == "" {
		return nil, nil, nil, nil, fmt.Errorf("sessions need --profile NAME (the browser persists per profile)")
	}
	wd, werr := webdataDir(profile)
	if werr != nil {
		return nil, nil, nil, nil, werr
	}
	if st, serr := os.Stat(filepath.Join(wd, "Default")); serr != nil || !st.IsDir() {
		return nil, nil, nil, nil, fmt.Errorf("no saved session files for profile %q — re-run picu login to materialize them", profile)
	}
	d, _, ok := daemonGet(wd)
	if !ok {
		return nil, nil, nil, nil, fmt.Errorf("could not start the session browser")
	}
	overall := o.timeout
	if overall < 150*time.Second {
		overall = 150 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), overall)
	allocCtx, cancelA := remote.NewAllocator(ctx, d.WSURL)
	// Detach on cancel: the session tab must survive between commands so
	// later snap/act invocations can reattach to it.
	taskCtx, cancelT := chromedp.NewContext(allocCtx, chromedp.WithTargetID(target.ID(s.Target)), chromedp.WithDetachOnCancel())
	evalFn := func(expr string) (string, error) { return evalString(taskCtx, expr) }
	if _, err := evalFn("location.href"); err != nil {
		cancelT()
		cancelA()
		cancel()
		return nil, nil, nil, nil, fmt.Errorf("session %q expired — run: picu open --profile %s URL", s.Name, profile)
	}
	return evalFn, cancelT, cancelA, cancel, nil
}

func sessionURL(eval func(string) (string, error)) string {
	if u, err := eval("location.href"); err == nil && u != "" {
		return u
	}
	return "unknown"
}

// resolveRef maps a numeric step argument to the element's selector.
func resolveRef(arg string, els []snapEl) string {
	i, err := strconv.Atoi(strings.TrimSpace(arg))
	if err != nil || i < 0 || i >= len(els) {
		return arg
	}
	return els[i].Selector
}

func snapOutput(url string, els []snapEl) {
	if *asJSON {
		doc := map[string]any{"url": url, "elements": els}
		if els == nil {
			doc["elements"] = []snapEl{}
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		_ = enc.Encode(doc)
		return
	}
	fmt.Print(formatSnapshot(url, els))
}

// runOpen implements `picu open --profile P [--session S] URL`.
func runOpen(args []string, o fetchOpts) {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: picu open --profile NAME [--session S] URL")
		os.Exit(2)
	}
	name := *sessionFlg
	if *useReal {
		fmt.Fprintln(os.Stderr, "error: sessions need a snapshot profile, not --use-real-profile")
		os.Exit(2)
	}
	if o.profile == "" {
		fmt.Fprintln(os.Stderr, "error: sessions need --profile NAME (the browser persists per profile)")
		os.Exit(2)
	}
	url := normalizeURL(args[0])
	if old, err := loadSession(name); err == nil {
		runCloseTarget(old)
		fmt.Fprintf(os.Stderr, "note: replaced session %q\n", name)
	}
	wd, err := webdataDir(o.profile)
	if err != nil {
		fatal(err.Error())
	}
	if st, err := os.Stat(filepath.Join(wd, "Default")); err != nil || !st.IsDir() {
		fatal(fmt.Sprintf("no saved session files for profile %q — re-run picu login to materialize them", o.profile))
	}
	d, _, ok := daemonGet(wd)
	if !ok {
		fatal("could not start the session browser")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	allocCtx, cancelAlloc := remote.NewAllocator(ctx, d.WSURL)
	defer cancelAlloc()
	tmpCtx, cancelTmp := chromedp.NewContext(allocCtx)
	res, err := chromedp.Call(tmpCtx, target.CreateTarget, target.CreateTargetParams{URL: url})
	cancelTmp()
	if err != nil {
		fatal(fmt.Sprintf("open: %v", err))
	}
	s := &browserSession{Name: name, Target: string(res.TargetID), Profile: o.profile}
	if err := saveSession(s); err != nil {
		fatal(err.Error())
	}
	eval, cancelTask, cancelAlloc, cancelParent, err := sessionCtx(s, o)
	if err != nil {
		fatal(err.Error())
	}
	defer cancelParent()
	defer cancelAlloc()
	defer cancelTask()
	// The new tab starts at about:blank; wait for the navigation to land
	// before snapshotting, otherwise open reports an empty page.
	deadline := time.Now().Add(20 * time.Second)
	for sessionURL(eval) == "about:blank" && time.Now().Before(deadline) {
		time.Sleep(250 * time.Millisecond)
	}
	els, err := snapshotElements(eval)
	if err != nil {
		fatal(err.Error())
	}
	snapOutput(sessionURL(eval), els)
}

func runCloseTarget(s *browserSession) {
	wd, err := webdataDir(s.Profile)
	if err != nil {
		return
	}
	d, _, ok := daemonGet(wd)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	allocCtx, cancelAlloc := remote.NewAllocator(ctx, d.WSURL)
	defer cancelAlloc()
	tmpCtx, cancelTmp := chromedp.NewContext(allocCtx)
	_, _ = chromedp.Call(tmpCtx, target.CloseTarget, target.CloseTargetParams{TargetID: target.ID(s.Target)})
	cancelTmp()
}

// runSnap implements `picu snap [--session S]`.
func runSnap(o fetchOpts) {
	name := *sessionFlg
	s, err := loadSession(name)
	if err != nil {
		fatal(err.Error())
	}
	eval, cancelTask, cancelAlloc, cancelParent, err := sessionCtx(s, o)
	if err != nil {
		fatal(err.Error())
	}
	defer cancelParent()
	defer cancelAlloc()
	defer cancelTask()
	els, err := snapshotElements(eval)
	if err != nil {
		fatal(err.Error())
	}
	snapOutput(sessionURL(eval), els)
}

// runAct implements `picu act [--session S] verb args…` (one step).
func runAct(args []string, o fetchOpts) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: picu act [--session S] click|type|select|wait|scroll|press|back …")
		os.Exit(2)
	}
	name := *sessionFlg
	s, err := loadSession(name)
	if err != nil {
		fatal(err.Error())
	}
	eval, cancelTask, cancelAlloc, cancelParent, err := sessionCtx(s, o)
	if err != nil {
		fatal(err.Error())
	}
	defer cancelParent()
	defer cancelAlloc()
	defer cancelTask()
	// act takes exactly one step: parse argv directly (no re-join) so
	// quoted multi-word selectors keep their grouping. Use ; scripts
	// with --do for multi-step flows.
	a, err := parseStep(args, strings.Join(args, " "))
	if err != nil {
		fatal(err.Error())
	}
	if a.sel != "" {
		if _, err := strconv.Atoi(strings.TrimSpace(a.sel)); err == nil {
			els, err := snapshotElements(eval)
			if err != nil {
				fatal(err.Error())
			}
			a.sel = resolveRef(a.sel, els)
		}
	}
	if err := runActions(eval, []action{a}); err != nil {
		fatal(err.Error())
	}
	els, err := snapshotElements(eval)
	if err != nil {
		fatal(err.Error())
	}
	snapOutput(sessionURL(eval), els)
}

// runClose implements `picu close [--session S]`.
func runClose() {
	name := *sessionFlg
	s, err := loadSession(name)
	if err != nil {
		fatal(err.Error())
	}
	runCloseTarget(s)
	removeSession(name)
	fmt.Fprintf(os.Stderr, "closed session %q\n", name)
}
