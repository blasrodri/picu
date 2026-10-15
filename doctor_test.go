package main

import (
	"io"
	"os"
	"strings"
	"testing"
)

func TestDoctorRuns(t *testing.T) {
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	runDoctor()
	w.Close()
	os.Stdout = old
	b, _ := io.ReadAll(r)
	out := string(b)
	for _, want := range []string{"picu " + picuVersion, "static:", "lightpanda:", "chrome:", "config:", "cache:"} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor output missing %q:\n%s", want, out)
		}
	}
}
