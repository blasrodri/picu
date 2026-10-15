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
// verb's argument: free text for type, a duration or selector for wait,
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
		a := action{verb: strings.ToLower(toks[0])}
		switch a.verb {
		case "click", "scroll":
			if len(toks) != 2 {
				return nil, fmt.Errorf("--do %q needs exactly a selector", part)
			}
			a.sel = toks[1]
		case "type":
			if len(toks) < 3 {
				return nil, fmt.Errorf("--do %q needs a selector and text", part)
			}
			a.sel = toks[1]
			a.arg = strings.Join(toks[2:], " ")
		case "wait":
			if len(toks) != 2 {
				return nil, fmt.Errorf("--do %q needs a selector or duration", part)
			}
			if _, err := time.ParseDuration(toks[1]); err == nil {
				a.arg = toks[1]
			} else {
				a.sel = toks[1]
			}
		case "press":
			if len(toks) < 2 {
				return nil, fmt.Errorf("--do %q needs a key", part)
			}
			a.arg = strings.Join(toks[1:], " ")
		case "back":
			if len(toks) > 1 {
				return nil, fmt.Errorf("--do %q takes no arguments", part)
			}
		default:
			return nil, fmt.Errorf("--do unknown verb %q (click|type|wait|scroll|press|back)", toks[0])
		}
		out = append(out, a)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("--do needs at least one step")
	}
	return out, nil
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
	for _, a := range steps {
		res, err := runOne(eval, a)
		if err != nil {
			fmt.Fprintf(os.Stderr, "act: %s → error: %v\n", describe(a), err)
			return err
		}
		fmt.Fprintf(os.Stderr, "act: %s → %s\n", describe(a), res)
	}
	return nil
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
		return "type " + q(a.sel) + " " + q(a.arg)
	case "wait":
		if a.sel != "" {
			return "wait " + q(a.sel)
		}
		return "wait " + a.arg
	case "press":
		return "press " + a.arg
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
	case "type":
		return need(eval(jsOK(a.sel,
			`if('value' in el){el.value=`+strconv.Quote(a.arg)+`}else{el.textContent=`+strconv.Quote(a.arg)+`}`+
				`el.dispatchEvent(new Event('input',{bubbles:true}));el.dispatchEvent(new Event('change',{bubbles:true}));return 'ok'`)))
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
