package main

import (
	"strings"
	"testing"
	"time"
)

const extractPage = `<html><head><title>Test Story</title>
<meta name="description" content="A story about &amp; testing">
<meta property="article:author" content="Ada">
<meta property="article:published_time" content="2026-10-01">
<link rel="canonical" href="https://x.test/story">
<script type="application/ld+json">{"@type":"Article","headline":"Test"}</script>
</head><body>
<nav><p>home about contact login subscribe</p></nav>
<article><h1>Test Story</h1><p>Real answer content here with enough words to matter for agents reading.</p></article>
<footer><p>copyright stuff</p></footer>
</body></html>`

func TestArticleKeepsMainContent(t *testing.T) {
	md := applyExtract(extractPage, toMarkdown(extractPage), "article")
	if !strings.Contains(md, "Real answer content here") {
		t.Errorf("article body lost: %q", md)
	}
	if strings.Contains(md, "copyright stuff") {
		t.Errorf("footer leaked into article: %q", md)
	}
}

func TestArticleFallsBackToFullPage(t *testing.T) {
	html := `<html><body><p>just a plain page with content</p></body></html>`
	md := applyExtract(html, toMarkdown(html), "article")
	if !strings.Contains(md, "just a plain page") {
		t.Errorf("fallback dropped content: %q", md)
	}
}

func TestArticleNestedBalance(t *testing.T) {
	html := `<html><body><article><p>outer</p><article><p>inner</p></article><p>tail</p></article></body></html>`
	sub, ok := firstElement(html, strings.ToLower(html), "article")
	if !ok {
		t.Fatal("nested article not balanced")
	}
	for _, want := range []string{"outer", "inner", "tail"} {
		if !strings.Contains(sub, want) {
			t.Errorf("balanced element missing %q: %q", want, sub)
		}
	}
}

func TestMetadataLines(t *testing.T) {
	md := applyExtract(extractPage, toMarkdown(extractPage), "metadata")
	for _, want := range []string{
		"title: Test Story",
		"description: A story about & testing",
		"canonical: https://x.test/story",
		"author: Ada",
		"published: 2026-10-01",
		`jsonld: {"@type":"Article","headline":"Test"}`,
	} {
		if !strings.Contains(md, want) {
			t.Errorf("metadata missing %q:\n%s", want, md)
		}
	}
}

func TestMetadataSparse(t *testing.T) {
	md := applyExtract(`<html><head><title>Only</title></head><body></body></html>`, "", "metadata")
	if md != "title: Only" {
		t.Errorf("sparse metadata = %q, want single title line", md)
	}
}

func TestCheckExtractRejects(t *testing.T) {
	if err := checkExtract("fulltext"); err == nil {
		t.Error("unknown mode should error")
	}
	for _, ok := range []string{"", "article", "metadata"} {
		if err := checkExtract(ok); err != nil {
			t.Errorf("mode %q should pass: %v", ok, err)
		}
	}
}

func TestCacheIsolatesExtract(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CACHE_HOME", tmp)
	a := fetchOpts{renderer: "static", timeout: time.Second, cacheTTL: time.Hour}
	b := a
	b.extract = "article"
	if cachePath("https://x.test/a", "static", a) == cachePath("https://x.test/a", "static", b) {
		t.Fatal("cache key ignores extract mode")
	}
}
