package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const picuVersion = "0.1"

// runDoctor reports everything a fetch depends on: renderer binaries,
// saved profiles with clearance state, and data directories. One fact
// per line so agents can parse it as easily as humans read it.
func runDoctor() {
	fmt.Printf("picu %s\n", picuVersion)
	fmt.Println("static: yes (built in)")
	if bin, ok := findLightpanda(); ok {
		fmt.Printf("lightpanda: %s\n", bin)
	} else {
		fmt.Println("lightpanda: missing (set LIGHTPANDA_BIN or put it on PATH)")
	}
	if bin, ok := findSystemChrome(); ok {
		fmt.Printf("chrome: %s\n", bin)
	} else {
		fmt.Println("chrome: missing (no solver fallback; set PICU_CHROME_BIN)")
	}
	n := 0
	if entries, _ := os.ReadDir(filepath.Join(configDir(), "profiles")); entries != nil {
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			p, err := loadProfile(strings.TrimSuffix(e.Name(), ".json"))
			if err != nil {
				continue
			}
			fmt.Printf("profile %s: %d cookies, clearance %s, updated %s\n",
				p.Name, len(p.Cookies), siteClearance(p), p.Updated.Format("2006-01-02 15:04"))
			n++
		}
	}
	if n == 0 {
		fmt.Println("profiles: none (run: picu login --profile NAME SITE)")
	}
	dir, _ := os.UserCacheDir()
	fmt.Printf("config: %s\n", configDir())
	fmt.Printf("cache: %s\n", filepath.Join(dir, "picu"))
}
