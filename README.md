# picu — curl for the agent era (v0.1)

`picu URL` fetches a page and prints clean markdown within a token
budget. `--json` returns schema-validated JSON for agents.

```bash
make install                                    # builds ./picu, copies to ~/.local/bin
./picu doctor                                   # renderers, profiles, dirs — run this first
./picu ole.com.ar/article                      # bare domain, all defaults
./picu ole.com.ar/article --json --budget 500  # flags anywhere
./picu --renderer lightpanda example.com        # force JS rendering
./picu --renderer static example.com           # never spawn a browser
```

v0.1: static fetch, readability-style strip, token budget, disk cache.
v0.2: Lightpanda sidecar, `--profile`, `--visible` assist, `--select`.

## releases

push a tag and CI builds all four platform tarballs onto a GitHub
release with the version stamped in (`--version` and User-Agent):

```bash
git tag v0.2.0 && git push origin v0.2.0
curl -sL https://github.com/blasrodri/picu/releases/download/v0.2.0/picu-v0.2.0-darwin-arm64.tar.gz | tar xz
```

`main` stays releasable: CI runs `gofmt`, `go vet`, tests, and a
build on Linux and macOS for every push and pull request.

## tui

```bash
./picu --tui https://example.com/article
./picu --tui   # enter a URL live
```

keys: j/k scroll, g/G top/bottom, / search, J toggle json,
r refetch, b change url, ? help, q quit.

## profiles (auth walls)

```bash
picu login --profile every --use-real-profile       # one command: your whole everyday store, Chrome keeps running
picu login --profile pplx perplexity.ai              # fresh browser, you log in, Enter captures session
picu login --profile work --cookie-file cookies.txt  # or import Netscape/CDP JSON
picu profiles
picu --profile every --renderer chrome "app behind your login"
picu logout --profile work
```

the import copies session cookies plus DOM storage off the live
profile (SQLite journals included, so nothing freshly written is
lost) and reads them through a headless Chrome on the copy — the
original is never touched and Chrome never quits. password stores
are never copied. fresh cookies flush on Chrome's own cadence, so
the import re-copies until they appear (seconds, not minutes).

skip per-site login entirely with your everyday profile:

```bash
picu --use-real-profile --renderer chrome "app behind your login"
```

this temp-copies session cookies plus DOM storage (never password
stores, never the live dir) and renders as your logged-in self, so
OAuth walls pass silently. no need to quit Chrome. cached
separately under `chrome-real`.

renderers: static (fast, no JS), lightpanda (cheap JS, cookies via
--cookie), chrome (real Chromium + saved cookies and DOM storage — the
one for signed-in SPAs, with desktop UA and no automation flag so bot
walls treat it as a normal browser). login snapshots session files for
the chrome renderer automatically. if a wall still holds, the chrome
renderer opens the session visibly once for a manual solve
(`--no-assist` disables that). auto mode (the default) reads with
lightpanda first using the profile session, falling back to chrome on
a bot challenge — solve once in chrome, read fast in lightpanda.

## search

```bash
picu search lightpanda browser --count 3
picu search "site:github.com lightpanda" --json
```

keyless discovery over DuckDuckGo HTML: titles, direct URLs (redirect
unwrapped), and snippets as markdown or a JSON array. no results is an
empty list, never an error. pair it with fetch: search for the link,
then `picu` the URL.

## interact

```bash
picu --profile g --renderer chrome --do "type #q hello; click #go; wait .results" example.com
```

`--do` runs scripted steps in one Chrome session before the dump:
`click`, `type` (text never appears in logs), `select` (dropdown option
by index, value, or visible-text prefix), `read` (full element text, no
truncation), `wait` (selector or duration), `scroll` (selector, top,
bottom), `press` (Enter, Escape, Tab…), `back`.
steps split on `;`, the first token after the verb is always the
selector (quote it when it holds spaces), each step logs to stderr, and
a selector matching nothing fails the run. combine with `--profile` or
`--use-real-profile` for login-then-act flows, and `--extract` for the
resulting document. snapshots show each dropdown's current choice.

`--engine google` renders Google in headless Chrome behind the same
output contract instead. Google consent-walls unknown sessions, so it
needs an accepted session (`--profile` with a prior login or
`--use-real-profile` with Chrome quit); a wall returns an explicit
error, never wall text as results. heavy automated use trips Google's
unusual-traffic rate limit, which needs a human CAPTCHA solve or simply
waiting it out — the error says which. accepted-state parsing is the
live check still open: run it once the limit lifts and compare.

## agent loop (persistent session)

```bash
picu open --profile g example.com/form        # snapshot with [ref]s
picu snap --session default                   # re-snapshot the live tab
picu act --session default type 0 hello       # verbs: click|type|select|read|wait|scroll|press|back
picu act --session default "select 2 T10; type 0 query"   # one quoted ;-script, same language as --do
picu act --session default read 5             # full element text, no truncation (prints only the text)
picu snap --session default --json            # {"url":…, "elements":[{ref,role,name,selector}]}
picu act --session default click 1 --json     # {"url":…, "results":[…], "elements":[…]}
picu watch --session default --interval 10s   # live-show feed: one JSON line per state change
picu bid --session default 150                # one explicit bid at or under 150, never auto-confirms
picu sessions                                 # named sessions on disk
picu close --session default                  # close tab, drop session
```

`open` keeps one tab alive on the profile daemon and prints the same
ref snapshot every command; `snap`/`act` reattach to it, so page state
(cookies, inputs, navigation) survives between invocations and the
agent — not picu — is the loop brain. detach-on-cancel leaves the tab
open; `close` is the only command that kills it. refs are per
snapshot: re-read them after every command that changes the page.
each command is its own process with JSON-friendly stdout and exit
0/1/2. session daemons live up to a day idle (fetch daemons minutes);
past that, or after a rebuild or reboot, a dead tab reports
`session expired`, and the loop re-opens (login persists in the
profile, so re-opening is a new tab, not a re-login).
`watch` polls read-only on a slow ticker for live-shopping tabs
(stream, product, price, winner — Whatnot-shaped today); `bid` clicks
the live bid button only at or under your max and leaves any
confirmation dialog to you — picu never confirms purchases.

## daemon (opt-in speed)

`PICU_DAEMON=1` keeps one headless Chrome alive across runs, so repeat
chrome fetches skip the spawn (~0.9s all-in for a simple page). records
live under the cache dir; stale, idle (>5min), and post-upgrade
browsers are reaped automatically, a lock held by a live daemon is
reused instead of fought, and every failure falls back to a cold
launch. `picu doctor` shows daemon state per profile.

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

## crawl

```bash
./picu crawl example.com --depth 1 --pages 30 --json --cache 0
```

BFS from the seed in visit order: `--depth N` link hops beyond the
seed (`0` = seed only), `--pages M` total cap, `--jobs N` workers per
level. same host only, repeats and cycles skipped, each page carries
its `depth` in JSON. politeness is on by default: one `robots.txt`
per host, `Disallow` prefixes honored, missing file means allow-all.

## proxy

```bash
./picu --proxy http://127.0.0.1:8080 example.com --cache 0
./picu --proxy-file proxies.txt example.com --cache 0      # host:port:user:pass or URL per line
./picu --proxy-file https://provider/.../list.txt crawl example.com --depth 1
```

`--proxy` (http/https only) routes static, lightpanda, robots, and
chrome fetches through the proxy; without it the standard
`HTTP_PROXY`/`HTTPS_PROXY` env still applies to plain fetches.
localhost and loopback always bypass. `--proxy-file` takes a local
file or a provider download URL and rotates one proxy per fetch
(batch and crawl spread automatically); an explicit `--proxy` wins
for a single fetch. proxy URLs carry credentials, so they never
appear in output or logs — only the pool size on stderr. chrome
renders get a cold proxied launch on a temp session copy (auth
preserved through jar injection) — profile daemons and interactive
login stay direct. chrome never gets credentials in its flags
(unsupported); proxy logins are answered over CDP instead.
cache keys include the proxy URL.

sessions pin one proxy for life: `open --proxy URL` (or one pool
draw) records it, and every later `snap`/`act` reuses the same
proxied daemon — rotation would hop browsers and break reattach, so
it never applies mid-session. `sessions` marks them `via proxy`
without ever printing the URL.

## agents

`--json` returns one envelope per document: `url, title, markdown,
chars, cached, renderer, profile` (batch: an array; failures become
`{"url":…, "error":…}`). exit 0 means every item succeeded, 1 means at
least one failed, 2 means bad flags. `--extract article` narrows output
to main content, `--extract metadata` returns title/description/author
lines. `picu doctor` reports what a fetch depends on, one fact per line.

## flags

`--select CSS` scopes extraction (JS renderers), `--extract
article|metadata` narrows output before the budget applies, `--schema
FILE` (or `default`) validates the output envelope and refetches once
on failure, `--settle 2s` shortens the stream tail, `--budget` caps
output tokens, `--jobs N` sizes the batch pool, `--version` prints the
version, `doctor` checks the setup.

## gate

```bash
gofmt -l . && go vet ./... && go test -count=1 ./... && go build -o picu .
PICU_LIVE=1 go test -count=1 -run Live .  # live sites, opt-in only
```

the default gate stays hermetic: unit tests plus a local fixture
server that plays static, JS-rendered, login, filter, popup,
paginated, and robots-blocked pages (`e2e_test.go`). the `Live`
canaries hit real sites with structural asserts only (they must
survive site copy edits) and run on schedule via `.github/workflows/
nightly.yml`, which also installs Chrome — a red nightly means picu
broke, not the internet.
