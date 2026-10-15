package main

import (
	"errors"
	"strings"
	"testing"
)

func TestParseDo(t *testing.T) {
	steps, err := parseDo(`type #q hello world; click #go; wait .r; scroll bottom; press Enter; back`)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 6 {
		t.Fatalf("got %d steps: %+v", len(steps), steps)
	}
	if steps[0].verb != "type" || steps[0].sel != "#q" || steps[0].arg != "hello world" {
		t.Errorf("type parsed as %+v", steps[0])
	}
	if steps[2].verb != "wait" || steps[2].sel != ".r" {
		t.Errorf("wait-selector parsed as %+v", steps[2])
	}
	steps, err = parseDo(`wait 3s`)
	if err != nil || len(steps) != 1 || steps[0].arg != "3s" || steps[0].sel != "" {
		t.Errorf("wait-duration parsed as %+v, %v", steps, err)
	}
	steps, err = parseDo(`type "div input" "hello there"`)
	if err != nil || steps[0].sel != "div input" || steps[0].arg != "hello there" {
		t.Errorf("quoted selector parsed as %+v, %v", steps, err)
	}
	steps, err = parseDo(`select #fterr "T10 (4378 paradas)"`)
	if err != nil || steps[0].verb != "select" || steps[0].sel != "#fterr" || steps[0].arg != "T10 (4378 paradas)" {
		t.Errorf("select parsed as %+v, %v", steps, err)
	}
	for _, bad := range []string{"", "frobnicate #x", "click", "type #q", "select #x", "wait", "press", "back #x", "click #a #b"} {
		if _, err := parseDo(bad); err == nil {
			t.Errorf("%q should fail", bad)
		}
	}
}

func TestKeyName(t *testing.T) {
	for in, want := range map[string]string{"enter": "Enter", "esc": "Escape", "space": " ", "up": "ArrowUp", "x": "x"} {
		if got := keyName(in); got != want {
			t.Errorf("keyName(%q) = %q, want %q", in, got, want)
		}
	}
}

// stubPage pretends to be a DOM: selectors in present exist, and every
// dispatched script is recorded for assertion.
type stubPage struct {
	present map[string]bool
	ran     []string
}

func (s *stubPage) eval(expr string) (string, error) {
	s.ran = append(s.ran, expr)
	if strings.Contains(expr, "history.back") || strings.Contains(expr, "scrollTo") {
		return "ok", nil
	}
	if strings.Contains(expr, "KeyboardEvent") {
		return "ok", nil
	}
	if strings.Contains(expr, "selectedIndex") {
		return "selected:stub-choice", nil
	}
	for sel := range s.present {
		if strings.Contains(expr, sel) {
			return "ok", nil
		}
	}
	if strings.Contains(expr, "querySelector") {
		return "missing", nil
	}
	return "", errors.New("unexpected script: " + expr)
}

func TestRunActionsDispatch(t *testing.T) {
	pg := &stubPage{present: map[string]bool{"#q": true, "#go": true, ".r": true}}
	steps, err := parseDo(`type #q hello; click #go; wait .r; scroll bottom; press Enter; back`)
	if err != nil {
		t.Fatal(err)
	}
	if err := runActions(pg.eval, steps); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(pg.ran, "\n")
	for _, want := range []string{"hello", "click()", "scrollTo", "KeyboardEvent", "history.back"} {
		if !strings.Contains(joined, want) {
			t.Errorf("no script dispatched %q", want)
		}
	}
}

func TestRunActionsMissing(t *testing.T) {
	pg := &stubPage{present: map[string]bool{}}
	steps, _ := parseDo(`click #ghost`)
	if err := runActions(pg.eval, steps); err == nil || !strings.Contains(err.Error(), "#ghost") {
		t.Errorf("missing selector should fail loudly, got %v", err)
	}
}

func TestSelectVerb(t *testing.T) {
	pg := &stubPage{present: map[string]bool{"#fterr": true}}
	steps, err := parseDo(`select #fterr T10; select #fterr 11`)
	if err != nil {
		t.Fatal(err)
	}
	if err := runActions(pg.eval, steps); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(pg.ran, "\n")
	for _, want := range []string{"selectedIndex", "dispatchEvent", "change"} {
		if !strings.Contains(joined, want) {
			t.Errorf("select dispatched no %q", want)
		}
	}
	for res, hint := range map[string]string{"no-option": "T10", "not-select": "#fterr", "missing": "#fterr"} {
		eval := func(string) (string, error) { return res, nil }
		steps, _ := parseDo(`select #fterr T10`)
		if err := runActions(eval, steps); err == nil || !strings.Contains(err.Error(), hint) {
			t.Errorf("select %q should fail naming %q, got %v", res, hint, err)
		}
	}
}

func TestDescribeMasksTypedText(t *testing.T) {
	if got := describe(action{verb: "type", sel: "#q", arg: "s3cret"}); got != `type #q ***` {
		t.Errorf("type log = %q, want masked", got)
	}
	if got := describe(action{verb: "select", sel: "#fterr", arg: "T10"}); got != `select #fterr T10` {
		t.Errorf("select log = %q", got)
	}
}
