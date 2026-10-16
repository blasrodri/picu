package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
)

// fetchFunc fetches one URL through the standard pipeline (get matches it,
// so production batch calls reuse get unchanged per URL). Tests inject a
// stub so batch semantics never touch the network.
type fetchFunc func(url string, o fetchOpts) (md, title string, cached bool, via string, err error)

// batchItem is one item of a batch run, kept in input order.
type batchItem struct {
	url string
	doc map[string]any
	err error
}

// errorEnvelope is the JSON shape for a failed batch item.
func errorEnvelope(url string, err error) map[string]any {
	return map[string]any{"url": url, "error": err.Error()}
}

// fetchOne runs the single-document pipeline (fetch, budget, optional
// schema check with one fresh refetch) and returns the output envelope.
func fetchOne(url string, opts fetchOpts, fn fetchFunc) (map[string]any, error) {
	build := func(md, title string, cached bool, via string) map[string]any {
		return map[string]any{
			"url": url, "title": title, "markdown": md,
			"chars": len(md), "cached": cached, "renderer": via,
			"profile": opts.profile,
		}
	}
	if opts.proxy == "" {
		// Pool rotation lives here (and in get for direct single
		// fetches): each item takes the next proxy. Explicit --proxy
		// wins; proxy URLs never enter the output envelope — they
		// carry credentials.
		opts.proxy = nextPoolProxy()
	}
	md, title, cached, via, err := fn(url, opts)
	if err != nil {
		return nil, err
	}
	md = applyBudget(md, opts.budget)
	doc := build(md, title, cached, via)
	if *schemaFile != "" {
		if bad := checkDoc(doc); len(bad) > 0 {
			fmt.Fprintf(os.Stderr, "schema check failed (%s) — refetching fresh…\n", strings.Join(bad, "; "))
			fresh := opts
			fresh.cacheTTL = 0
			md, title, cached, via, err = fn(url, fresh)
			if err != nil {
				return nil, err
			}
			md = applyBudget(md, opts.budget)
			doc = build(md, title, cached, via)
		}
		if bad := checkDoc(doc); len(bad) > 0 {
			return nil, fmt.Errorf("invalid output: %s", strings.Join(bad, "; "))
		}
	}
	return doc, nil
}

// fetchAll fetches every URL with a bounded pool of jobs workers. Results
// always match input order, never completion order.
func fetchAll(urls []string, opts fetchOpts, jobs int, fn fetchFunc) []batchItem {
	out := make([]batchItem, len(urls))
	if len(urls) == 0 {
		return out
	}
	if jobs < 1 {
		jobs = 1
	}
	if jobs > len(urls) {
		jobs = len(urls)
	}
	next := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < jobs; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				doc, err := fetchOne(urls[i], opts, fn)
				out[i] = batchItem{url: urls[i], doc: doc, err: err}
			}
		}()
	}
	for i := range urls {
		next <- i
	}
	close(next)
	wg.Wait()
	return out
}

// runBatch implements `picu URL1 URL2 …`: fetch in parallel, print in
// input order, exit nonzero when any item failed.
func runBatch(raw []string, opts fetchOpts) {
	if *jobs < 1 {
		fmt.Fprintln(os.Stderr, "--jobs must be >= 1")
		os.Exit(2)
	}
	urls := make([]string, len(raw))
	for i, r := range raw {
		urls[i] = normalizeURL(r)
	}
	results := fetchAll(urls, opts, *jobs, get)
	if *asJSON {
		arr := make([]map[string]any, len(results))
		for i, r := range results {
			if r.err != nil {
				arr[i] = errorEnvelope(r.url, r.err)
				continue
			}
			arr[i] = r.doc
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		_ = enc.Encode(arr)
	} else {
		for _, r := range results {
			if r.err != nil {
				fmt.Fprintf(os.Stderr, "error (%s): %v\n", r.url, r.err)
				continue
			}
			fmt.Printf("# %s\n\nSource: %s (via %s)\n\n%s\n", r.doc["title"], r.url, r.doc["renderer"], r.doc["markdown"])
		}
	}
	for _, r := range results {
		if r.err != nil {
			os.Exit(1)
		}
	}
}
