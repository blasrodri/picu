package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func daemonTestDir(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	return tmp + "/wd"
}

func TestDaemonRoundTrip(t *testing.T) {
	dir := daemonTestDir(t)
	d := daemonInfo{PID: os.Getpid(), Port: 19999, WSURL: "ws://x",
		Build: picuBuildID(), Dir: dir, LastUse: time.Now().Unix()}
	saveDaemon(dir, d)
	got, found := readDaemon(dir)
	if !found {
		t.Fatal("should read back a fresh record")
	}
	if got.Port != 19999 || got.WSURL != "ws://x" || got.PID != os.Getpid() {
		t.Errorf("round-trip lost fields: %+v", got)
	}
	d.Build = "ancient"
	saveDaemon(dir, d)
	if _, found := readDaemon(dir); found {
		t.Error("build mismatch should invalidate the record")
	}
}

func TestDaemonGetReuses(t *testing.T) {
	dir := daemonTestDir(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"webSocketDebuggerUrl":"ws://reused/1"}`)
	}))
	defer srv.Close()
	port, err := strconv.Atoi(strings.TrimPrefix(srv.URL, "http://127.0.0.1:"))
	if err != nil {
		t.Fatal(err)
	}
	saveDaemon(dir, daemonInfo{PID: os.Getpid(), Port: port, WSURL: "ws://stale",
		Build: picuBuildID(), Dir: dir, LastUse: time.Now().Unix()})
	spawned := 0
	got, fresh, ok := daemonGetWith(dir, func(string) (daemonInfo, bool) {
		spawned++
		return daemonInfo{}, false
	})
	if !ok || fresh {
		t.Fatalf("healthy daemon should be reused, got ok=%v fresh=%v", ok, fresh)
	}
	if spawned != 0 {
		t.Error("reuse must not spawn")
	}
	if got.WSURL != "ws://reused/1" {
		t.Errorf("ws url should refresh from probe, got %q", got.WSURL)
	}
}

func TestIsLockErr(t *testing.T) {
	if !isLockErr(fmt.Errorf("chrome endpoint never came up: timeout\nFailed to create SingletonLock: File exists")) {
		t.Error("lock refusal should classify")
	}
	if isLockErr(fmt.Errorf("cannot start chrome: not found")) {
		t.Error("missing binary is not a lock refusal")
	}
	if isLockErr(nil) {
		t.Error("nil is not a lock refusal")
	}
}

func TestDaemonGetSpawnsWhenStale(t *testing.T) {
	dir := daemonTestDir(t)
	saveDaemon(dir, daemonInfo{PID: 2147483647, Port: 1, WSURL: "ws://dead",
		Build: picuBuildID(), Dir: dir, LastUse: time.Now().Unix()})
	spawned := 0
	fake := daemonInfo{PID: 4242, Port: 19998, WSURL: "ws://new", Build: picuBuildID()}
	got, fresh, ok := daemonGetWith(dir, func(string) (daemonInfo, bool) {
		spawned++
		return fake, true
	})
	if !ok || !fresh || spawned != 1 {
		t.Fatalf("stale daemon should respawn, got ok=%v fresh=%v spawned=%d", ok, fresh, spawned)
	}
	if got.WSURL != "ws://new" {
		t.Errorf("should return the spawned browser, got %+v", got)
	}
}
