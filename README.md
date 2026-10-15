# picu — curl for the agent era (v0.1)

`picu URL` fetches a page and prints clean markdown within a token
budget. `--json` returns schema-validated JSON for agents.

```bash
go build -o picu .
./picu ole.com.ar/article                      # bare domain, all defaults
./picu ole.com.ar/article --json --budget 500  # flags anywhere
./picu --renderer lightpanda example.com        # force JS rendering
./picu --renderer static example.com           # never spawn a browser
```

v0.1: static fetch, readability-style strip, token budget, disk cache.
v0.2: Lightpanda sidecar, `--profile`, `--visible` assist, `--select`.

## tui

```bash
./picu --tui https://example.com/article
./picu --tui   # enter a URL live
```

keys: j/k scroll, g/G top/bottom, / search, J toggle json,
r refetch, b change url, ? help, q quit.

## profiles (auth walls)

```bash
picu login --profile pplx perplexity.ai              # fresh browser, you log in, Enter captures session
picu login --profile pplx --use-real-profile perplexity.ai  # temp-copy real profile (quit Chrome first)
picu login --profile work --cookie-file cookies.txt  # or import Netscape/CDP JSON
picu profiles
picu --profile pplx --renderer chrome "perplexity.ai/search?q=lightpanda"
picu logout --profile work
```

renderers: static (fast, no JS), lightpanda (cheap JS, cookies via
--cookie), chrome (real Chromium + saved cookies and DOM storage — the
one for signed-in SPAs, with desktop UA and no automation flag so bot
walls treat it as a normal browser). login snapshots session files for
the chrome renderer automatically. if a wall still holds, the chrome
renderer opens the session visibly once for a manual solve
(`--no-assist` disables that).

## batch

```bash
./picu example.com example.org --cache 0 --budget 100
./picu example.com example.org --json --cache 0 --budget 100
```

multiple URLs fetch in parallel (`--jobs N`, default 4) and always
print in input order. `--budget`/`--schema` apply per document.
markdown prints each doc under its header with failures on stderr;
`--json` emits one array with failures as `{"url":…, "error":…}`.
exit status is nonzero when any item failed. `--tui` takes at most
one URL. tip: use `--jobs 1` if a host rate-limits shared-IP bursts.

## flags

`--select CSS` scopes extraction (JS renderers), `--schema FILE` (or
`default`) validates the output envelope and refetches once on failure,
`--settle 2s` shortens the stream tail, `--budget` caps output tokens.

## gate

```bash
gofmt -l . && go vet ./... && go test -count=1 ./... && go build -o picu .
```
