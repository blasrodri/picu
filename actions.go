package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// action is one parsed --do step: a verb plus selector and/or argument.
type action struct {
	verb, sel, arg string
}

// stepTimeouts bound the waits: polling the page, not the whole fetch.
const (
	waitPollCap  = 30 * time.Second
	waitSleepCap = 60 * time.Second
)

// parseDo splits --do into steps on ';'. The first token after the verb is
// always the selector (quote it when it holds spaces); the rest is the
// verb's argument: free text for type, an option for select (index,
// value, or visible-text prefix), a duration or selector for wait,
// a key name for press. Quoting is shell-like, both quote kinds group.
func parseDo(s string) ([]action, error) {
	var out []action
	for _, part := range strings.Split(s, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		toks := splitQuoted(part)
		if len(toks) == 0 {
			continue
		}
		a, err := parseStep(toks, part)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("--do needs at least one step")
	}
	return out, nil
}

// parseStep builds one action from pre-split tokens. --do splits each ;
// step with splitQuoted; act passes argv straight through so quoted
// multi-word selectors survive (re-joining argv would lose the grouping).
func parseStep(toks []string, raw string) (action, error) {
	if len(toks) == 0 {
		return action{}, fmt.Errorf("--do %q is empty", raw)
	}
	a := action{verb: strings.ToLower(toks[0])}
	switch a.verb {
	case "click", "scroll", "read":
		if len(toks) != 2 {
			return action{}, fmt.Errorf("--do %q needs exactly a selector", raw)
		}
		a.sel = toks[1]
	case "type", "select":
		if len(toks) < 3 {
			return action{}, fmt.Errorf("--do %q needs a selector and text", raw)
		}
		a.sel = toks[1]
		a.arg = strings.Join(toks[2:], " ")
	case "wait":
		if len(toks) != 2 {
			return action{}, fmt.Errorf("--do %q needs a selector or duration", raw)
		}
		if _, err := time.ParseDuration(toks[1]); err == nil {
			a.arg = toks[1]
		} else {
			a.sel = toks[1]
		}
	case "press":
		if len(toks) < 2 {
			return action{}, fmt.Errorf("--do %q needs a key", raw)
		}
		a.arg = strings.Join(toks[1:], " ")
	case "back":
		if len(toks) > 1 {
			return action{}, fmt.Errorf("--do %q takes no arguments", raw)
		}
	default:
		return action{}, fmt.Errorf("--do unknown verb %q (click|type|select|read|wait|scroll|press|back)", toks[0])
	}
	return a, nil
}

// splitQuoted tokenizes shell-style: quote groups survive spaces.
func splitQuoted(s string) []string {
	var toks []string
	var b strings.Builder
	var quote byte
	flush := func() {
		if b.Len() > 0 {
			toks = append(toks, b.String())
			b.Reset()
		}
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0 && c == quote:
			quote = 0
		case quote != 0:
			b.WriteByte(c)
		case c == '"' || c == '\'':
			quote = c
		case c == ' ' || c == '\t':
			flush()
		default:
			b.WriteByte(c)
		}
	}
	flush()
	return toks
}

// keyName normalizes friendly key spellings to KeyboardEvent keys.
func keyName(k string) string {
	switch strings.ToLower(k) {
	case "enter":
		return "Enter"
	case "esc", "escape":
		return "Escape"
	case "tab":
		return "Tab"
	case "space", "spacebar":
		return " "
	case "up":
		return "ArrowUp"
	case "down":
		return "ArrowDown"
	case "left":
		return "ArrowLeft"
	case "right":
		return "ArrowRight"
	case "backspace":
		return "Backspace"
	case "delete":
		return "Delete"
	default:
		return k
	}
}

// runActions executes parsed steps through eval and logs each to stderr.
// A selector that matches nothing fails the run loudly.
func runActions(eval func(string) (string, error), steps []action) error {
	_, err := runSteps(eval, steps)
	return err
}

// runSteps executes steps in order, logging each to stderr and returning
// every step's result for machine-readable envelopes (--json).
func runSteps(eval func(string) (string, error), steps []action) ([]string, error) {
	var out []string
	for _, a := range steps {
		res, err := runOne(eval, a)
		if err != nil {
			fmt.Fprintf(os.Stderr, "act: %s → error: %v\n", describe(a), err)
			return out, err
		}
		fmt.Fprintf(os.Stderr, "act: %s → %s\n", describe(a), shortRes(res))
		out = append(out, res)
	}
	return out, nil
}

// shortRes keeps step logs scannable: full results belong on stdout
// (--json) or in the read output, not in every stderr line.
func shortRes(s string) string {
	const max = 200
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

func describe(a action) string {
	q := func(s string) string {
		if strings.ContainsAny(s, " \t") {
			return strconv.Quote(s)
		}
		return s
	}
	switch a.verb {
	case "type":
		// Typed text can carry passwords and tokens: never log it.
		return "type " + q(a.sel) + " ***"
	case "select":
		return "select " + q(a.sel) + " " + q(a.arg)
	case "wait":
		if a.sel != "" {
			return "wait " + q(a.sel)
		}
		return "wait " + a.arg
	case "press":
		return "press " + a.arg
	case "read":
		return "read " + q(a.sel)
	case "back":
		return "back"
	default:
		return a.verb + " " + q(a.sel)
	}
}

func jsOK(sel, body string) string {
	return `(()=>{const el=document.querySelector(` + strconv.Quote(sel) + `);if(!el)return 'missing';` + body + `})()`
}

// runOne executes a single step, returning a short status for the log.
func runOne(eval func(string) (string, error), a action) (string, error) {
	need := func(res string, err error) (string, error) {
		if err != nil {
			return "", err
		}
		if res == "missing" {
			return "", fmt.Errorf("selector %q matched nothing", a.sel)
		}
		return res, nil
	}
	switch a.verb {
	case "click":
		return need(eval(jsOK(a.sel, `el.click();return 'ok'`)))
	case "read":
		// Full element text, no snapshot truncation: the on-demand read
		// behind popup bodies and long labels.
		return need(eval(jsOK(a.sel, `return ('innerText' in el && el.innerText) ? el.innerText : (el.value||'')`)))
	case "type":
		return need(eval(jsOK(a.sel,
			`if('value' in el){el.value=`+strconv.Quote(a.arg)+`}else{el.textContent=`+strconv.Quote(a.arg)+`}`+
				`el.dispatchEvent(new Event('input',{bubbles:true}));el.dispatchEvent(new Event('change',{bubbles:true}));return 'ok'`)))
	case "select":
		res, err := eval(`((sel,want)=>{const el=document.querySelector(sel);` +
			`if(!el)return 'missing';if(el.tagName!=='SELECT')return 'not-select';let i=-1;` +
			`if(/^\d+$/.test(want)){i=parseInt(want,10);if(i<0||i>=el.options.length)return 'no-option';}` +
			`else{for(let k=0;k<el.options.length;k++){const o=el.options[k];if(o.value===want||o.text===want){i=k;break}}` +
			`if(i<0){const lw=want.toLowerCase();for(let k=0;k<el.options.length;k++){if(el.options[k].text.toLowerCase().startsWith(lw)){i=k;break}}}` +
			`if(i<0)return 'no-option';}` +
			`el.selectedIndex=i;el.dispatchEvent(new Event('input',{bubbles:true}));el.dispatchEvent(new Event('change',{bubbles:true}));` +
			`return 'selected:'+el.options[i].text})(` + strconv.Quote(a.sel) + `,` + strconv.Quote(a.arg) + `)`)
		if err != nil {
			return "", err
		}
		switch {
		case res == "missing":
			return "", fmt.Errorf("selector %q matched nothing", a.sel)
		case res == "not-select":
			return "", fmt.Errorf("selector %q is not a <select>", a.sel)
		case res == "no-option":
			return "", fmt.Errorf("selector %q has no option %q", a.sel, a.arg)
		case strings.HasPrefix(res, "selected:"):
			return res, nil
		default:
			return "", fmt.Errorf("select %q: unexpected result %q", a.sel, res)
		}
	case "scroll":
		switch strings.ToLower(a.sel) {
		case "bottom":
			_, err := eval(`window.scrollTo(0,document.body.scrollHeight);'ok'`)
			return "ok", err
		case "top":
			_, err := eval(`window.scrollTo(0,0);'ok'`)
			return "ok", err
		default:
			return need(eval(jsOK(a.sel, `el.scrollIntoView();return 'ok'`)))
		}
	case "press":
		return need(eval(`(k=>{const t=document.activeElement||document.body;if(!t)return 'missing';` +
			`t.dispatchEvent(new KeyboardEvent('keydown',{key:k,bubbles:true}));` +
			`t.dispatchEvent(new KeyboardEvent('keyup',{key:k,bubbles:true}));return 'ok'})` +
			`(` + strconv.Quote(keyName(a.arg)) + `)`))
	case "back":
		_, err := eval(`history.back();'ok'`)
		return "ok", err
	case "wait":
		if a.sel == "" {
			d, _ := time.ParseDuration(a.arg)
			if d > waitSleepCap {
				d = waitSleepCap
			}
			time.Sleep(d)
			return "slept " + d.String(), nil
		}
		deadline := time.Now().Add(waitPollCap)
		for {
			res, err := eval(jsOK(a.sel, `return 'ok'`))
			if err != nil {
				return "", err
			}
			if res == "ok" {
				return "ok", nil
			}
			if time.Now().After(deadline) {
				return "", fmt.Errorf("wait for %q timed out", a.sel)
			}
			time.Sleep(250 * time.Millisecond)
		}
	default:
		return "", fmt.Errorf("unknown verb %q", a.verb)
	}
}
