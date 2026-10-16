package main

import (
	"errors"
	"reflect"
	"testing"
)

// stubSite is a tiny link graph: seed fans out to /a and /b (which share
// /c), seed links itself (cycle), one cross-host link, one robots-blocked
// path, one image, and one non-http target.
var stubSite = map[string]string{
	"http://t.test/":          `[a](/a) [b](/b) [x](http://other.test/x) [p](/private/1) ![img](/i.png) [m](mailto:x@y.zz) [self](http://t.test/#frag)`,
	"http://t.test/a":         `[c](/c) [seed](http://t.test/)`,
	"http://t.test/b":         `[c](/c)`,
	"http://t.test/c":         `leaf, no links`,
	"http://t.test/private/1": `blocked by robots`,
}

func stubCrawlFetch(url string, o fetchOpts) (string, string, bool, string, error) {
	md, ok := stubSite[url]
	if !ok {
		return "", "", false, "", errors.New("404: " + url)
	}
	return md, url, false, "stub", nil
}

func stubRobots(host string) []string {
	if host == "t.test" {
		return []string{"/private"}
	}
	return nil
}

func crawlURLs(items []crawlItem) []string {
	var out []string
	for _, it := range items {
		out = append(out, it.url)
	}
	return out
}

func TestCrawlDepthCutoff(t *testing.T) {
	o := fetchOpts{renderer: "static", budget: 8000}
	got := crawl("http://t.test/", o, crawlOpts{depth: 0, pages: 30}, 4, stubCrawlFetch, stubRobots)
	if want := []string{"http://t.test/"}; !reflect.DeepEqual(crawlURLs(got), want) {
		t.Errorf("depth 0 = %v, want %v", crawlURLs(got), want)
	}
	got = crawl("http://t.test/", o, crawlOpts{depth: 1, pages: 30}, 4, stubCrawlFetch, stubRobots)
	if want := []string{"http://t.test/", "http://t.test/a", "http://t.test/b"}; !reflect.DeepEqual(crawlURLs(got), want) {
		t.Errorf("depth 1 = %v, want %v", crawlURLs(got), want)
	}
	for _, it := range got {
		if it.url == "http://t.test/" && it.depth != 0 {
			t.Errorf("seed depth = %d, want 0", it.depth)
		}
	}
}

func TestCrawlDedupeAndScope(t *testing.T) {
	o := fetchOpts{renderer: "static", budget: 8000}
	got := crawl("http://t.test/", o, crawlOpts{depth: 2, pages: 30}, 4, stubCrawlFetch, stubRobots)
	want := []string{"http://t.test/", "http://t.test/a", "http://t.test/b", "http://t.test/c"}
	if !reflect.DeepEqual(crawlURLs(got), want) {
		t.Errorf("depth 2 = %v, want %v", crawlURLs(got), want)
	}
	for _, it := range got {
		if it.err != nil {
			t.Errorf("%s failed: %v", it.url, it.err)
		}
	}
}

func TestCrawlPagesCap(t *testing.T) {
	o := fetchOpts{renderer: "static", budget: 8000}
	got := crawl("http://t.test/", o, crawlOpts{depth: 2, pages: 2}, 4, stubCrawlFetch, stubRobots)
	if want := []string{"http://t.test/", "http://t.test/a"}; !reflect.DeepEqual(crawlURLs(got), want) {
		t.Errorf("pages 2 = %v, want %v", crawlURLs(got), want)
	}
}

func TestExtractLinks(t *testing.T) {
	md := `[a](/a) [b](http://t.test/b#s) ![i](/i.png) [x](http://other.test/x) [m](mailto:a@b.c)`
	got := extractLinks(md, "http://t.test/dir/p")
	want := []string{"http://t.test/a", "http://t.test/b"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("extractLinks = %v, want %v", got, want)
	}
}

func TestRobotsFor(t *testing.T) {
	body := "User-agent: *\nDisallow: /x\n\nUser-agent: otherbot\nDisallow: /y\n"
	got := robotsFor("h.test", func(string) (string, error) { return body, nil })
	if want := []string{"/x"}; !reflect.DeepEqual(got, want) {
		t.Errorf("robotsFor = %v, want %v", got, want)
	}
	if !robotsAllowed("/ok", got) {
		t.Errorf("/ok should be allowed")
	}
	if robotsAllowed("/public", got) != true || robotsAllowed("/x/1", got) != false {
		t.Errorf("prefix matching wrong: %v", got)
	}
	if got := robotsFor("h.test", func(string) (string, error) { return "", errors.New("down") }); len(got) != 0 {
		t.Errorf("robots failure should fail open, got %v", got)
	}
}
