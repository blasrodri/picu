package main

// watch streams a live-shopping tab as JSON lines: one line per state
// change (product, price, bids, status, winner). Read-only DOM polls on
// a slow ticker — it never clicks, bids, or navigates. Bidding stays a
// separate explicit command (runBid), which never auto-confirms.

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// whatnotStateJS reads a Whatnot live-show tab into a JSON object. All
// lookups are text-anchored (button captions, footer copy), never ref
// numbers, because the element list shifts as chat and lots move.
const whatnotStateJS = `(() => {
const txt = el => (el && el.innerText || '').trim().replace(/\s+/g, ' ');
const first = els => { for (const el of els) { const s = txt(el); if (s) return s; } return ''; };
const sellerLink = document.querySelector('section header div a');
const seller = sellerLink ? txt(sellerLink) : '';
let viewers = '';
const hdrBtns = document.querySelectorAll('section header section button');
if (hdrBtns.length > 0) viewers = txt(hdrBtns[0]);
const footBtn = document.querySelector('section footer section button');
const foot = txt(footBtn);
let bidPrice = '', canBid = false;
for (const b of document.querySelectorAll('button')) {
  const m = txt(b).match(/^Bid:\s*(.+)$/);
  if (m) { bidPrice = m[1]; canBid = true; break; }
}
let winner = '';
const sold = foot.match(/[Ss]old\s+(?:to\s+)?(\S.{0,40}?)(?:\s{2,}|$)/);
if (sold) winner = sold[1].trim();
const cleanChat = t => {
  const lines = (t || '').split('\n').map(s => s.trim())
    .filter(s => s && s.length > 1 && !/^(Follow|Say something\.\.\.)$/i.test(s));
  const out = [];
  for (const l of lines) {
    if (out.length === 0 || out[out.length - 1] !== l) out.push(l);
  }
  return out.slice(-8);
};
const vis = el => { try { return el.getClientRects().length > 0; } catch (e) { return false; } };
// Chat rows carry data-testid="chat-message" (avatar, username, text
// in innerText). Hidden mobile duplicates are skipped by visibility.
let chat = [];
{
  const rows = document.querySelectorAll('[data-testid="chat-message"]');
  const seen = [];
  for (let i = 0; i < rows.length; i++) {
    if (!vis(rows[i])) continue;
    const t = (rows[i].innerText || '').trim().replace(/\s+/g, ' ');
    if (t) seen.push(t.slice(0, 220));
  }
  chat = cleanChat(seen.slice(-6).join('\n'));
}
if (!winner) {
  for (let i = chat.length - 1; i >= 0; i--) {
    const m = chat[i].match(/^(.{2,30}?)\s+won\b/i) || chat[i].match(/congrats\s+(\S.{0,30}?)\s*(!|$)/i);
    if (m) { winner = m[1].trim(); break; }
  }
}
let status = 'off';
if (/auction hasn't started/i.test(foot)) status = 'pre';
else if (canBid) status = 'live';
else if (winner) status = 'sold';
return JSON.stringify({seller, viewers, product: foot.slice(0, 300),
  price: bidPrice, canBid, status, winner, chat});
})()`

type watchState struct {
	Seller  string   `json:"seller"`
	Viewers string   `json:"viewers"`
	Product string   `json:"product"`
	Price   string   `json:"price"`
	CanBid  bool     `json:"canBid"`
	Status  string   `json:"status"`
	Winner  string   `json:"winner"`
	Chat    []string `json:"chat"`
}

func (w watchState) key() string {
	return strings.Join(append([]string{w.Seller, w.Viewers, w.Product, w.Price, w.Status, w.Winner}, w.Chat...), "|")
}

// runWatch implements `picu watch [--session S] [--interval D]`: attach
// to the session tab and print one JSON object per state change.
func runWatch(o fetchOpts, interval time.Duration) {
	name := *sessionFlg
	s, err := loadSession(name)
	if err != nil {
		fatal(err.Error())
	}
	_, eval, cancelTask, cancelAlloc, cancelParent, err := sessionCtx(s, o)
	if err != nil {
		fatal(err.Error())
	}
	defer cancelParent()
	defer cancelAlloc()
	defer cancelTask()
	if interval < 5*time.Second {
		interval = 5 * time.Second
	}
	var last string
	first := true
	for {
		raw, err := eval(whatnotStateJS)
		if err != nil {
			fmt.Fprintf(os.Stderr, "watch: read failed (%v) — retrying\n", err)
			time.Sleep(interval)
			continue
		}
		var st watchState
		if jerr := json.Unmarshal([]byte(raw), &st); jerr != nil {
			fmt.Fprintf(os.Stderr, "watch: parse failed — retrying\n")
			time.Sleep(interval)
			continue
		}
		if first || st.key() != last {
			last = st.key()
			first = false
			if st.Chat == nil {
				st.Chat = []string{}
			}
			doc := map[string]any{
				"ts": time.Now().UTC().Format(time.RFC3339), "url": sessionURL(eval),
				"seller": st.Seller, "viewers": st.Viewers, "product": st.Product,
				"price": st.Price, "canBid": st.CanBid, "status": st.Status, "winner": st.Winner,
				"chat": st.Chat,
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetEscapeHTML(false)
			_ = enc.Encode(doc)
		}
		time.Sleep(interval)
	}
}

// bidJS clicks the button captioned "Bid: <price>" when that price is at
// or under the caller's max. It never touches confirmation dialogs: if
// one appears the bid is left pending for the user.
const bidJS = `(max => {
for (const b of document.querySelectorAll('button')) {
  const m = (b.innerText || '').trim().replace(/\s+/g, ' ').match(/^Bid:\s*€?\s*([\d.,]+)/);
  if (!m) continue;
  const price = parseFloat(m[1].replace(/,/g, ''));
  if (price <= max) { b.click(); return JSON.stringify({clicked: true, price: m[1]}); }
  return JSON.stringify({clicked: false, price: m[1], reason: 'over max'});
}
return JSON.stringify({clicked: false, reason: 'no live bid button'});
})`

// runBid implements `picu bid --session S MAX`: one explicit bid at or
// under MAX on the current lot. Each invocation is one consent.
func runBid(o fetchOpts, max float64) {
	name := *sessionFlg
	s, err := loadSession(name)
	if err != nil {
		fatal(err.Error())
	}
	_, eval, cancelTask, cancelAlloc, cancelParent, err := sessionCtx(s, o)
	if err != nil {
		fatal(err.Error())
	}
	defer cancelParent()
	defer cancelAlloc()
	defer cancelTask()
	raw, err := eval("(" + bidJS + ")(" + strconv.FormatFloat(max, 'f', -1, 64) + ")")
	if err != nil {
		fatal(fmt.Sprintf("bid: %v", err))
	}
	fmt.Println(raw)
}
