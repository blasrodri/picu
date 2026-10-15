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

skip per-site login entirely with your everyday profile:

```bash
picu --use-real-profile --renderer chrome "app behind your login"
```

this temp-copies session cookies plus DOM storage (never password
stores, never the live dir) and renders as your logged-in self, so
OAuth walls pass silently. requires quitting Chrome first since it
owns its profile exclusively. cached separately under `chrome-real`.

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
`click`, `type`, `wait` (selector or duration), `scroll` (selector, top,
bottom), `press` (Enter, Escape, Tab…), `back`. steps split on `;`, the
first token after the verb is always the selector (quote it with
spaces), each step logs to stderr, and a selector matching nothing
fails the run. combine with `--profile` or `--use-real-profile` for
login-then-act flows, and `--extract` for the resulting document.

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
picu act --session default type 0 hello       # one step: click|type|wait|scroll|press|back
picu close --session default                  # close tab, drop session
```

`open` keeps one tab alive on the profile daemon and prints the same
ref snapshot every command; `snap`/`act` reattach to it, so page state
(cookies, inputs, navigation) survives between invocations and the
agent — not picu — is the loop brain. detach-on-cancel leaves the tab
open; `close` is the only command that kills it. each command is its
own process with JSON-friendly stdout and exit 0/1/2.

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
