package main

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

// stubFetch returns canned markdown per URL, sleeping longer for earlier
// indices so completion order is the reverse of input order unless the
// pool preserves it. URLs containing "fail" return an error.
func stubFetch(urls []string) fetchFunc {
	return func(url string, o fetchOpts) (string, string, bool, string, error) {
		if strings.Contains(url, "fail") {
			return "", "", false, "", errors.New("boom for " + url)
		}
		for i, u := range urls {
			if u == url {
				time.Sleep(time.Duration(len(urls)-i) * 5 * time.Millisecond)
				return "body of " + url, "title of " + url, false, "stub", nil
			}
		}
		return "body of " + url, "title of " + url, false, "stub", nil
	}
}

func batchOpts() fetchOpts {
	return fetchOpts{renderer: "static", budget: 8000}
}

func TestFetchAllKeepsInputOrder(t *testing.T) {
	old := *schemaFile
	*schemaFile = ""
	defer func() { *schemaFile = old }()
	urls := []string{"https://a.test/", "https://b.test/", "https://c.test/", "https://d.test/"}
	got := fetchAll(urls, batchOpts(), 4, stubFetch(urls))
	if len(got) != len(urls) {
		t.Fatalf("fetchAll returned %d results, want %d", len(got), len(urls))
	}
	for i, r := range got {
		if r.err != nil {
			t.Fatalf("item %d (%s) errored: %v", i, urls[i], r.err)
		}
		if r.url != urls[i] {
			t.Errorf("item %d url = %q, want %q", i, r.url, urls[i])
		}
		if r.doc["markdown"] != "body of "+urls[i] {
			t.Errorf("item %d markdown = %q, want body of %q", i, r.doc["markdown"], urls[i])
		}
	}
}

func TestFetchAllCapturesErrors(t *testing.T) {
	old := *schemaFile
	*schemaFile = ""
	defer func() { *schemaFile = old }()
	urls := []string{"https://ok.test/", "https://fail.test/"}
	got := fetchAll(urls, batchOpts(), 2, stubFetch(urls))
	if got[0].err != nil {
		t.Errorf("ok item errored: %v", got[0].err)
	}
	if got[1].err == nil {
		t.Fatal("failing item should carry an error")
	}
	if got[1].doc != nil {
		t.Errorf("failing item should have no doc, got %v", got[1].doc)
	}
	env := errorEnvelope(got[1].url, got[1].err)
	if env["url"] != "https://fail.test/" {
		t.Errorf("envelope url = %v", env["url"])
	}
	if _, ok := env["error"].(string); !ok {
		t.Errorf("envelope error should be a string, got %T", env["error"])
	}
	if len(env) != 2 {
		t.Errorf("envelope should hold exactly url+error, got %v", env)
	}
}

func TestFetchAllJobsParity(t *testing.T) {
	old := *schemaFile
	*schemaFile = ""
	defer func() { *schemaFile = old }()
	urls := []string{"https://a.test/", "https://b.test/", "https://c.test/", "https://d.test/"}
	one := fetchAll(urls, batchOpts(), 1, stubFetch(urls))
	four := fetchAll(urls, batchOpts(), 4, stubFetch(urls))
	for i := range urls {
		if one[i].err != nil || four[i].err != nil {
			t.Fatalf("item %d errored: %v / %v", i, one[i].err, four[i].err)
		}
		if !reflect.DeepEqual(one[i].doc, four[i].doc) {
			t.Errorf("item %d differs between --jobs 1 and 4:\n%v\n%v", i, one[i].doc, four[i].doc)
		}
	}
}

func TestFetchOneAppliesBudget(t *testing.T) {
	old := *schemaFile
	*schemaFile = ""
	defer func() { *schemaFile = old }()
	fn := func(url string, o fetchOpts) (string, string, bool, string, error) {
		return strings.Repeat("x\n", 5000), "t", false, "stub", nil
	}
	doc, err := fetchOne("https://a.test/", fetchOpts{budget: 100}, fn)
	if err != nil {
		t.Fatal(err)
	}
	md := doc["markdown"].(string)
	if !strings.HasSuffix(md, "…(truncated to budget)") {
		t.Errorf("budget not applied per document, chars=%d", len(md))
	}
	if doc["chars"] != len(md) {
		t.Errorf("chars field = %v, want %d", doc["chars"], len(md))
	}
	for _, k := range []string{"url", "title", "markdown", "chars", "cached", "renderer", "profile"} {
		if _, ok := doc[k]; !ok {
			t.Errorf("envelope missing key %q: %v", k, doc)
		}
	}
}

func TestReorderArgsJobs(t *testing.T) {
	got := reorderArgs([]string{"a.test", "--jobs", "2", "b.test"})
	want := []string{"--jobs", "2", "a.test", "b.test"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("reorderArgs = %q, want %q", got, want)
	}
}
