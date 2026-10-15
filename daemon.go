package main

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Daemon mode (PICU_DAEMON=1) keeps one headless Chrome alive across picu
// invocations so repeated fetches skip the ~0.5s spawn. It only serves
// snapshot profile dirs, never per-fetch borrow copies, and every failure
// falls back to a cold launch, so the daemon can only ever tie.

// daemonIdleTTL bounds daemon afterlife: an unused browser is reaped on
// the next chrome fetch instead of lingering for days.
const daemonIdleTTL = 5 * time.Minute

type daemonInfo struct {
	PID     int    `json:"pid"`
	Port    int    `json:"port"`
	WSURL   string `json:"wsURL"`
	Build   string `json:"build"`
	Dir     string `json:"dir"`
	LastUse int64  `json:"lastUse"`
}

func daemonEnabled() bool { return os.Getenv("PICU_DAEMON") == "1" }

func daemonFile(userDir string) string {
	dir, _ := os.UserCacheDir()
	if dir == "" {
		dir = os.TempDir()
	}
	h := sha1.Sum([]byte(userDir))
	return filepath.Join(dir, "picu", "daemon-"+hex.EncodeToString(h[:8])+".json")
}

func picuBuildID() string {
	exe, err := os.Executable()
	if err != nil {
		return "unknown"
	}
	st, err := os.Stat(exe)
	if err != nil {
		return "unknown"
	}
	return st.ModTime().UTC().Format(time.RFC3339Nano)
}

func saveDaemon(userDir string, d daemonInfo) {
	p := daemonFile(userDir)
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	tmp, err := os.CreateTemp(filepath.Dir(p), "daemon-*.json")
	if err != nil {
		return
	}
	name := tmp.Name()
	b, _ := json.Marshal(d)
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(name)
		return
	}
	tmp.Close()
	_ = os.Chmod(name, 0o600)
	_ = os.Rename(name, p)
}

func readDaemon(userDir string) (daemonInfo, bool) {
	var d daemonInfo
	b, err := os.ReadFile(daemonFile(userDir))
	if err != nil {
		return d, false
	}
	if json.Unmarshal(b, &d) != nil {
		return d, false
	}
	if d.Dir != userDir || d.Build != picuBuildID() || d.Port <= 0 || d.PID <= 0 {
		return daemonInfo{}, false
	}
	return d, true
}

func removeDaemonFile(userDir string) {
	_ = os.Remove(daemonFile(userDir))
}

// hasDaemonRecord reports a recorded browser without touching it.
func hasDaemonRecord(userDir string) bool {
	_, err := os.Stat(daemonFile(userDir))
	return err == nil
}

// killIfChrome kills pid only when it still looks like a browser, so a
// recycled PID can never catch a stray signal.
func killIfChrome(pid int) {
	if pid <= 0 {
		return
	}
	out, err := exec.Command("ps", "-p", fmt.Sprint(pid), "-o", "comm=").Output()
	if err != nil {
		return
	}
	if !strings.Contains(strings.ToLower(string(out)), "chrom") {
		return
	}
	if p, err := os.FindProcess(pid); err == nil {
		_ = p.Kill()
	}
}

// daemonSpawn starts a detached browser for userDir: the child is
// released, not waited on, so it outlives this picu run (dying with the
// login session at the latest). Returns false on any failure.
func daemonSpawn(userDir string) (daemonInfo, bool) {
	var zero daemonInfo
	chrome, ok := findSystemChrome()
	if !ok {
		return zero, false
	}
	port, err := freePort()
	if err != nil {
		return zero, false
	}
	cmd := exec.Command(chrome, chromeArgs(true, userDir, port)...)
	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		return zero, false
	}
	defer devNull.Close()
	cmd.Stdout, cmd.Stderr = devNull, devNull
	if err := cmd.Start(); err != nil {
		return zero, false
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release()
	wsURL, err := waitDebugger(port, 20*time.Second)
	if err != nil {
		killIfChrome(pid)
		return zero, false
	}
	if pid <= 0 {
		return zero, false
	}
	mine := daemonInfo{PID: pid, Port: port, WSURL: wsURL,
		Build: picuBuildID(), Dir: userDir, LastUse: time.Now().Unix()}
	saveDaemon(userDir, mine)
	if cur, found := readDaemon(userDir); found && cur.PID != mine.PID {
		// Lost a spawn race: keep the winner, drop ours.
		killIfChrome(mine.PID)
		if ws, err := debuggerURL(cur.Port); err == nil && ws != "" {
			cur.WSURL = ws
			return cur, true
		}
		removeDaemonFile(userDir)
		return zero, false
	}
	return mine, true
}

// daemonGet returns a live daemon browser, spawning or reaping as needed.
// Every failure path returns ok=false and the caller cold-launches.
func daemonGetWith(userDir string, spawn func(string) (daemonInfo, bool)) (d daemonInfo, fresh, ok bool) {
	var zero daemonInfo
	raw, _ := os.ReadFile(daemonFile(userDir))
	var cur daemonInfo
	if json.Unmarshal(raw, &cur) != nil || cur.Dir != userDir || cur.PID <= 0 || cur.Port <= 0 {
		if d, ok := spawn(userDir); ok {
			return d, true, true
		}
		return zero, false, false
	}
	if cur.Build != picuBuildID() {
		// Orphan from an upgrade: reap it so it cannot hold the
		// profile against the new binary, then start fresh below.
		killIfChrome(cur.PID)
		removeDaemonFile(userDir)
	} else if time.Since(time.Unix(cur.LastUse, 0)) > daemonIdleTTL {
		killIfChrome(cur.PID)
		removeDaemonFile(userDir)
	} else if ws, err := debuggerURL(cur.Port); err == nil && ws != "" {
		cur.WSURL = ws
		cur.LastUse = time.Now().Unix()
		saveDaemon(userDir, cur)
		return cur, false, true
	} else {
		killIfChrome(cur.PID)
		removeDaemonFile(userDir)
	}
	if d, ok := spawn(userDir); ok {
		return d, true, true
	}
	return zero, false, false
}

func daemonGet(userDir string) (daemonInfo, bool, bool) {
	return daemonGetWith(userDir, daemonSpawn)
}

// isLockErr reports a Chrome profile-lock refusal: another browser owns
// the directory, so launching is pointless but reusing it may work.
func isLockErr(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "SingletonLock") ||
		strings.Contains(err.Error(), "ProcessSingleton"))
}

// daemonKill drops the daemon for userDir (interactive assist needs the
// profile exclusively). Best effort by design.
func daemonKill(userDir string) {
	if d, found := readDaemon(userDir); found {
		killIfChrome(d.PID)
	}
	removeDaemonFile(userDir)
}
