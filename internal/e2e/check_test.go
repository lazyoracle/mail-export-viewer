package e2e

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lazyoracle/mail-export-viewer/internal/source"
	"github.com/lazyoracle/mail-export-viewer/internal/testutil/corpus"
)

// checker compares a running viewer with a corpus manifest.
type checker struct {
	t *testing.T
	v *viewer
	m *corpus.Manifest
	// unknown: sources whose message count is not ground truth (expected
	// to fail or partially fail); their listed messages are folded into the
	// expectations as they are.
	unknown     map[string]bool
	unknownMsgs []apiItem
	sourceIDs   map[string]int64 // name -> id
	full        bool             // per-message detail checks
}

func flagNames(f source.Flags) []string {
	n := f.Names()
	if n == nil {
		n = []string{}
	}
	return n
}

func sortedSet(s []string) []string {
	out := append([]string{}, s...)
	sort.Strings(out)
	return slices.Compact(out)
}

func tagNames(ts []apiTag) []string {
	var out []string
	for _, t := range ts {
		out = append(out, t.Name)
	}
	return out
}

func (c *checker) visible(m corpus.Msg) bool { return !m.Flags.Has(source.Deleted) }

// msgs returns the manifest messages of sources whose counts are known.
func (c *checker) msgs() []corpus.Msg {
	var out []corpus.Msg
	for _, m := range c.m.Messages {
		if !c.unknown[m.Source] {
			out = append(out, m)
		}
	}
	return out
}

func (c *checker) run() {
	c.checkStatus()
	c.checkFacets()
	c.checkList()
	c.checkMessages()
	c.checkSearches()
	c.checkThreads()
	c.checkStats()
	c.checkSecurity()
}

func (c *checker) checkStatus() {
	t := c.t
	var st struct {
		Sources []struct {
			ID                int64
			Name, Kind, State string
			Count             int64
			Error             string
			Warnings          []string
		}
	}
	c.v.getJSON("/api/status", &st)
	c.sourceIDs = map[string]int64{}
	got := map[string]int{}
	for i, s := range st.Sources {
		c.sourceIDs[s.Name] = s.ID
		got[s.Name] = i
	}
	want := map[string]int64{}
	for _, m := range c.msgs() {
		if c.visible(m) {
			want[m.Source]++
		}
	}
	c.unknown = map[string]bool{}
	for _, s := range c.m.Sources {
		if s.Failed {
			c.unknown[s.Name] = true
		}
	}
	for _, s := range c.m.Sources {
		i, ok := got[s.Name]
		if !ok {
			if !s.Failed {
				t.Errorf("status: source %q (%s) missing", s.Name, s.Kind)
			}
			continue
		}
		gs := st.Sources[i]
		if gs.Kind != s.Kind {
			t.Errorf("status: source %q kind %q, want %q", s.Name, gs.Kind, s.Kind)
		}
		if s.Failed {
			continue
		}
		if gs.State != "done" && gs.State != "cached" {
			t.Errorf("status: source %q state %q (error %q)", s.Name, gs.State, gs.Error)
		}
		if gs.Count != want[s.Name] {
			t.Errorf("status: source %q count %d, want %d", s.Name, gs.Count, want[s.Name])
		}
		for _, w := range gs.Warnings {
			if strings.Contains(w, c.m.Root) || strings.Contains(w, "panic") {
				t.Errorf("status: source %q warning leaks a path or a panic: %q", s.Name, w)
			}
		}
	}
	known := map[string]bool{}
	for _, s := range c.m.Sources {
		known[s.Name] = true
	}
	for name := range got {
		if !known[name] {
			t.Errorf("status: unexpected source %q", name)
		}
	}
	// Messages of unknown-count sources, as listed.
	for name := range c.unknown {
		if id, ok := c.sourceIDs[name]; ok && id != 0 {
			items, _ := c.v.list("source", strconv.FormatInt(id, 10), "deleted", "include")
			c.unknownMsgs = append(c.unknownMsgs, items...)
		}
	}
}

type termCount struct {
	Name, Color   string
	Count, Unread int64
}

type folderNode struct {
	Name, Path    string
	Count, Unread int64
	Children      []folderNode
}

func flatten(ns []folderNode, out map[string][2]int64) {
	for _, n := range ns {
		if n.Count > 0 || n.Unread > 0 {
			out[n.Path] = [2]int64{n.Count, n.Unread}
		}
		flatten(n.Children, out)
	}
}

func (c *checker) checkFacets() {
	t := c.t
	var f struct {
		Folders                  []folderNode
		Labels, Categories, Tags []termCount
		Flags                    []struct {
			Name  string
			Count int64
		}
	}
	c.v.getJSON("/api/facets", &f)
	// Folders.
	gotF := map[string][2]int64{}
	flatten(f.Folders, gotF)
	wantF := map[string][2]int64{}
	add := func(m map[string][2]int64, k string, unread bool) {
		v := m[k]
		v[0]++
		if unread {
			v[1]++
		}
		m[k] = v
	}
	for _, m := range c.msgs() {
		if c.visible(m) {
			add(wantF, m.Folder, m.Flags.Has(source.Unread))
		}
	}
	for _, it := range c.unknownMsgs {
		if !slices.Contains(it.Flags, "deleted") {
			add(wantF, it.Folder, slices.Contains(it.Flags, "unread"))
		}
	}
	compareCounts(t, "folder", gotF, wantF)
	// Labels, categories, tags.
	terms := func(kind string, got []termCount, pick func(corpus.Msg) []string, pickItem func(apiItem) []string) {
		g := map[string][2]int64{}
		for _, tc := range got {
			g[tc.Name] = [2]int64{tc.Count, tc.Unread}
			if kind == "tag" && c.m.TagColors[tc.Name] != "" && tc.Color != c.m.TagColors[tc.Name] {
				t.Errorf("tag %q colour %q, want %q", tc.Name, tc.Color, c.m.TagColors[tc.Name])
			}
		}
		w := map[string][2]int64{}
		for _, m := range c.msgs() {
			if c.visible(m) {
				for _, n := range sortedSet(pick(m)) {
					add(w, n, m.Flags.Has(source.Unread))
				}
			}
		}
		for _, it := range c.unknownMsgs {
			if !slices.Contains(it.Flags, "deleted") {
				for _, n := range sortedSet(pickItem(it)) {
					add(w, n, slices.Contains(it.Flags, "unread"))
				}
			}
		}
		// Terms whose every message is deleted are listed with count 0.
		for k, v := range g {
			if v == [2]int64{} {
				delete(g, k)
			}
		}
		compareCounts(t, kind, g, w)
	}
	terms("label", f.Labels, func(m corpus.Msg) []string { return m.Labels }, func(i apiItem) []string { return i.Labels })
	terms("category", f.Categories, func(m corpus.Msg) []string { return m.Cats }, func(i apiItem) []string { return i.Categories })
	terms("tag", f.Tags, func(m corpus.Msg) []string { return m.Tags }, func(i apiItem) []string { return tagNames(i.Tags) })
	// Flags: deleted counts all, the others visible messages only.
	wantFl := map[string]int64{}
	for _, m := range c.msgs() {
		for _, n := range m.Flags.Names() {
			if c.visible(m) || n == "deleted" {
				wantFl[n]++
			}
		}
	}
	for _, it := range c.unknownMsgs {
		del := slices.Contains(it.Flags, "deleted")
		for _, n := range it.Flags {
			if !del || n == "deleted" {
				wantFl[n]++
			}
		}
	}
	for _, fl := range f.Flags {
		if fl.Count != wantFl[fl.Name] {
			t.Errorf("flag %q count %d, want %d", fl.Name, fl.Count, wantFl[fl.Name])
		}
	}
}

func compareCounts(t *testing.T, kind string, got, want map[string][2]int64) {
	t.Helper()
	var bad []string
	for k, w := range want {
		if got[k] != w {
			bad = append(bad, fmt.Sprintf("%q: got %v want %v", k, got[k], w))
		}
	}
	for k, g := range got {
		if _, ok := want[k]; !ok {
			bad = append(bad, fmt.Sprintf("%q: got %v, not expected", k, g))
		}
	}
	sort.Strings(bad)
	if len(bad) > 0 {
		if len(bad) > 15 {
			bad = append(bad[:15], fmt.Sprintf("... and %d more", len(bad)-15))
		}
		t.Errorf("%s counts differ (%d entries):\n  %s", kind, len(want), strings.Join(bad, "\n  "))
	}
}

func (c *checker) visibleCount() int64 {
	n := int64(0)
	for _, m := range c.msgs() {
		if c.visible(m) {
			n++
		}
	}
	for _, it := range c.unknownMsgs {
		if !slices.Contains(it.Flags, "deleted") {
			n++
		}
	}
	return n
}

func (c *checker) checkList() {
	t := c.t
	want := c.visibleCount()
	for _, sortBy := range []string{"date_desc", "date_asc", "from", "subject"} {
		items, total := c.v.list("sort", sortBy)
		if total != want || int64(len(items)) != want {
			t.Errorf("list %s: total %d, %d items, want %d", sortBy, total, len(items), want)
		}
		ids := map[int64]bool{}
		for i, it := range items {
			if ids[it.ID] {
				t.Errorf("list %s: id %d listed twice", sortBy, it.ID)
			}
			ids[it.ID] = true
			if i == 0 {
				continue
			}
			p := items[i-1]
			ok := true
			switch sortBy {
			case "date_desc":
				ok = p.Date != nil && (it.Date == nil || !it.Date.After(*p.Date)) || p.Date == nil && it.Date == nil
			case "date_asc":
				ok = p.Date != nil && (it.Date == nil || !it.Date.Before(*p.Date)) || p.Date == nil && it.Date == nil
			}
			if !ok {
				t.Errorf("list %s: item %d (%v) out of order after %v", sortBy, i, it.Date, p.Date)
				break
			}
		}
	}
	// Every exact (folder, subject) is listed as often as expected.
	items, _ := c.v.list("deleted", "include")
	got := map[[2]string]int{}
	for _, it := range items {
		got[[2]string{it.Folder, it.Subject}]++
	}
	wantN := map[[2]string]int{}
	for _, m := range c.msgs() {
		if m.Exact {
			wantN[[2]string{m.Folder, m.Subject}]++
		}
	}
	var bad []string
	for k, n := range wantN {
		if got[k] < n {
			bad = append(bad, fmt.Sprintf("%q in %q: %d listed, want %d", k[1], k[0], got[k], n))
		}
	}
	sort.Strings(bad)
	if len(bad) > 0 {
		if len(bad) > 15 {
			bad = append(bad[:15], "...")
		}
		t.Errorf("subjects missing from the list:\n  %s", strings.Join(bad, "\n  "))
	}
}

// find returns the one message whose body holds token (deleted included).
func (c *checker) find(token string) (apiItem, bool) {
	var l apiList
	c.v.getJSON("/api/messages?"+q("q", token, "deleted", "include"), &l)
	if len(l.Items) != 1 {
		c.t.Errorf("search %q: %d results, want 1", token, len(l.Items))
		return apiItem{}, false
	}
	return l.Items[0], true
}

func (c *checker) checkMessages() {
	t := c.t
	n := 0
	for _, m := range c.msgs() {
		if !m.Exact || m.Token == "" {
			continue
		}
		n++
		if !c.full && n%50 != 1 {
			continue
		}
		it, ok := c.find(m.Token)
		if !ok {
			t.Logf("  (message %q in %q, %s)", m.Subject, m.Folder, m.Note)
			continue
		}
		what := fmt.Sprintf("message %q (%s, %s)", m.Subject, m.Folder, m.Note)
		if it.Subject != m.Subject {
			t.Errorf("%s: subject %q", what, it.Subject)
		}
		if it.Folder != m.Folder || it.SourceKind != m.Kind {
			t.Errorf("%s: folder %q kind %q", what, it.Folder, it.SourceKind)
		}
		if m.From != "" && !strings.EqualFold(it.From.Email, m.From) {
			t.Errorf("%s: from %q", what, it.From.Email)
		}
		if m.FromName != "" && it.From.Name != m.FromName {
			t.Errorf("%s: from name %q, want %q", what, it.From.Name, m.FromName)
		}
		switch {
		case m.Undated && it.Date != nil:
			t.Errorf("%s: date %v, want none", what, it.Date)
		case !m.Undated && !m.Date.IsZero() && (it.Date == nil || !it.Date.Equal(m.Date)):
			t.Errorf("%s: date %v, want %v", what, it.Date, m.Date)
		}
		if g, w := sortedSet(it.Flags), sortedSet(flagNames(m.Flags)); !slices.Equal(g, w) {
			t.Errorf("%s: flags %v, want %v", what, g, w)
		}
		for _, cmp := range []struct {
			name      string
			got, want []string
		}{{"labels", it.Labels, m.Labels}, {"categories", it.Categories, m.Cats}, {"tags", tagNames(it.Tags), m.Tags}} {
			if g, w := sortedSet(cmp.got), sortedSet(cmp.want); !slices.Equal(g, w) {
				if len(g) > 12 || len(w) > 12 {
					t.Errorf("%s: %s differ (%d vs %d)", what, cmp.name, len(g), len(w))
				} else {
					t.Errorf("%s: %s %q, want %q", what, cmp.name, g, w)
				}
			}
		}
		// Deleted messages are hidden by default.
		if m.Flags.Has(source.Deleted) {
			var l apiList
			c.v.getJSON("/api/messages?"+q("q", m.Token), &l)
			if len(l.Items) != 0 {
				t.Errorf("%s: deleted message shown by default", what)
			}
		}
		c.checkDetail(it.ID, m, what)
	}
	if n == 0 && len(c.m.Messages) > 0 {
		t.Errorf("no searchable exact messages in the manifest")
	}
}

var cidURLRE = regexp.MustCompile(`/api/messages/\d+/cid/[^"' )]+`)

func (c *checker) checkDetail(id int64, m corpus.Msg, what string) {
	t := c.t
	start := time.Now()
	var d apiDetail
	c.v.getJSON(fmt.Sprintf("/api/messages/%d", id), &d)
	if el := time.Since(start); el > 5*time.Second {
		t.Errorf("%s: detail took %v", what, el)
	}
	if d.MessageID != m.MessageID {
		t.Errorf("%s: messageId %q, want %q", what, d.MessageID, m.MessageID)
	}
	if m.Atts != nil {
		var names []string
		for _, a := range d.Attachments {
			if !a.Inline {
				names = append(names, a.Filename)
			}
		}
		if !slices.Equal(names, m.Atts) {
			t.Errorf("%s: attachments %q, want %q", what, names, m.Atts)
		}
	}
	for _, s := range m.TextHas {
		if !strings.Contains(d.Text, s) {
			t.Errorf("%s: text lacks %q (text starts %q)", what, s, trunc(d.Text, 200))
		}
	}
	if m.Token != "" && !strings.Contains(d.Text, m.Token) && !strings.Contains(d.BodyHTML, m.Token) {
		t.Errorf("%s: body lacks its token %s", what, m.Token)
	}
	if strings.Contains(strings.ToLower(d.BodyHTML), "<script") || strings.Contains(strings.ToLower(d.BodyHTML), "javascript:") ||
		regexp.MustCompile(`(?i)\son[a-z]+\s*=`).MatchString(d.BodyHTML) {
		t.Errorf("%s: bodyHtml not sanitised: %q", what, trunc(d.BodyHTML, 300))
	}
	// Attachments: download, check the envelope.
	for _, a := range d.Attachments {
		resp, body := c.v.get(a.URL)
		if resp.StatusCode != 200 {
			t.Errorf("%s: attachment %d: %d", what, a.Index, resp.StatusCode)
			continue
		}
		// An embedded message (synthesised .eml) is listed with the size
		// its PST/OLM container claims (documented).
		if int64(len(body)) != a.Size && !strings.HasSuffix(a.Filename, ".eml") {
			t.Errorf("%s: attachment %d: %d bytes, listed %d", what, a.Index, len(body), a.Size)
		}
		checkDisposition(t, what, resp.Header.Get("Content-Disposition"))
		checkHeaders(t, what+" attachment", resp, "sandbox")
		if a.Filename == "big.bin" || a.Filename == "disk image.bin" {
			sum := sha256.Sum256(body)
			if hex.EncodeToString(sum[:]) != c.m.BigAttSHA256 {
				t.Errorf("%s: big attachment content differs", what)
			}
		}
	}
	// cid URLs in the HTML resolve.
	for _, u := range cidURLRE.FindAllString(d.BodyHTML, -1) {
		u = strings.ReplaceAll(u, "&amp;", "&")
		resp, _ := c.v.get(u)
		if resp.StatusCode != 200 {
			t.Errorf("%s: cid %s: %d", what, u, resp.StatusCode)
		}
		checkHeaders(t, what+" cid", resp, "sandbox")
	}
	if len(m.RawHas) > 0 || c.full {
		resp, raw := c.v.get(d.RawURL)
		if resp.StatusCode != 200 || len(raw) == 0 {
			t.Errorf("%s: raw: %d, %d bytes", what, resp.StatusCode, len(raw))
		}
		checkHeaders(t, what+" raw", resp, "sandbox")
		checkDisposition(t, what+" raw", resp.Header.Get("Content-Disposition"))
		for _, s := range m.RawHas {
			if !strings.Contains(string(raw), s) {
				t.Errorf("%s: raw lacks %q", what, s)
			}
		}
	}
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// checkDisposition: attachment; filename="ascii"; filename*=UTF-8”...,
// without path separators, controls or bidi characters.
func checkDisposition(t *testing.T, what, cd string) {
	t.Helper()
	if !strings.HasPrefix(cd, "attachment") && !strings.HasPrefix(cd, "inline") {
		t.Errorf("%s: Content-Disposition %q", what, cd)
	}
	for _, r := range cd {
		if r < 0x20 || r == 0x7f || r > 0x7e {
			t.Errorf("%s: Content-Disposition has a non-ASCII or control character: %q", what, cd)
			break
		}
	}
	if i := strings.Index(cd, "filename*=UTF-8''"); i >= 0 {
		name, err := url.PathUnescape(strings.SplitN(cd[i+len("filename*=UTF-8''"):], ";", 2)[0])
		if err != nil {
			t.Errorf("%s: bad filename*: %q", what, cd)
		}
		if strings.ContainsAny(name, "/\\\x00\r\n\x1b") || strings.ContainsAny(name, "‮‭⁦⁧⁨") || name == ".." {
			t.Errorf("%s: unsafe file name %q", what, name)
		}
	}
	if i := strings.Index(cd, `filename="`); i >= 0 {
		name := cd[i+len(`filename="`):]
		if j := strings.Index(name, `"`); j >= 0 {
			name = name[:j]
		}
		if strings.ContainsAny(name, "/\\") {
			t.Errorf("%s: unsafe ASCII file name %q", what, name)
		}
	}
}

const (
	cspUI  = "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; object-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
	cspAPI = "default-src 'none'; img-src 'self' data:; style-src 'unsafe-inline'"
)

// checkHeaders checks the security envelope; kind is "ui", "api" or
// "sandbox" (raw, attachment, cid).
func checkHeaders(t *testing.T, what string, resp *http.Response, kind string) {
	t.Helper()
	h := resp.Header
	for k, v := range map[string]string{"X-Content-Type-Options": "nosniff", "Referrer-Policy": "no-referrer",
		"X-Frame-Options": "DENY", "Cross-Origin-Opener-Policy": "same-origin"} {
		if h.Get(k) != v {
			t.Errorf("%s: %s %q, want %q", what, k, h.Get(k), v)
		}
	}
	if c := h.Get("Cross-Origin-Resource-Policy"); c != "same-origin" && c != "cross-origin" {
		t.Errorf("%s: Cross-Origin-Resource-Policy %q", what, c)
	}
	csp := h.Values("Content-Security-Policy")
	switch kind {
	case "ui":
		if len(csp) != 1 || csp[0] != cspUI {
			t.Errorf("%s: CSP %q", what, csp)
		}
	case "api":
		if len(csp) != 1 || csp[0] != cspAPI {
			t.Errorf("%s: CSP %q", what, csp)
		}
	case "sandbox":
		if len(csp) != 2 || csp[0] != cspAPI || csp[1] != "sandbox" {
			t.Errorf("%s: CSP %q", what, csp)
		}
	}
}

// queryCount checks that q matches exactly the messages pick selects
// (among visible ones, unless deleted is set).
func (c *checker) queryCount(qs string, deleted string, pick func(m corpus.Msg) bool, pickUnknown func(apiItem) bool) {
	t := c.t
	want := int64(0)
	for _, m := range c.msgs() {
		if (deleted == "include" || c.visible(m) || strings.Contains(qs, "is:deleted")) && pick(m) {
			want++
		}
	}
	for _, it := range c.unknownMsgs {
		del := slices.Contains(it.Flags, "deleted")
		if (deleted == "include" || !del || strings.Contains(qs, "is:deleted")) && pickUnknown != nil && pickUnknown(it) {
			want++
		}
	}
	p := []string{"q", qs, "limit", "1"}
	if deleted != "" {
		p = append(p, "deleted", deleted)
	}
	var l apiList
	c.v.getJSON("/api/messages?"+q(p...), &l)
	if l.Total != want {
		t.Errorf("query %q (deleted=%q): total %d, want %d", qs, deleted, l.Total, want)
	}
}

func (c *checker) checkSearches() {
	hasFlag := func(f source.Flags) func(corpus.Msg) bool {
		return func(m corpus.Msg) bool { return m.Flags.Has(f) }
	}
	itemFlag := func(n string) func(apiItem) bool { return func(i apiItem) bool { return slices.Contains(i.Flags, n) } }
	for _, f := range []string{"unread", "starred", "answered", "forwarded", "draft", "junk", "important", "attachment", "deleted"} {
		fl, _ := source.ParseFlag(f)
		c.queryCount("is:"+f, "", hasFlag(fl), itemFlag(f))
	}
	c.queryCount("has:attachment", "", hasFlag(source.HasAttachment), itemFlag("attachment"))
	c.queryCount("is:read", "", func(m corpus.Msg) bool { return !m.Flags.Has(source.Unread) }, func(i apiItem) bool { return !slices.Contains(i.Flags, "unread") })
	c.queryCount("is:unread", "include", hasFlag(source.Unread), itemFlag("unread"))
	// Tokens: the prefix "qzt" matches every tokenised message.
	tok := func(m corpus.Msg) bool { return m.Token != "" }
	if c.unknownMsgsEmpty() {
		c.queryCount("qzt", "", tok, nil)
		c.queryCount("-is:unread qzt", "", func(m corpus.Msg) bool { return tok(m) && !m.Flags.Has(source.Unread) }, nil)
		c.queryCount("has:attachment qzt", "", func(m corpus.Msg) bool { return tok(m) && m.Flags.Has(source.HasAttachment) }, nil)
	}
	// Labels, categories, tags (each, up to 30 of each kind), and negations.
	seen := map[string]bool{}
	for _, m := range c.msgs() {
		for _, kv := range []struct {
			op    string
			names []string
		}{{"label", append(append([]string{}, m.Labels...), m.Cats...)}, {"category", m.Cats}, {"tag", m.Tags}} {
			for _, n := range kv.names {
				key := kv.op + ":" + n
				if seen[key] || len(seen) > 90 {
					continue
				}
				seen[key] = true
				op, name := kv.op, n
				has := func(m corpus.Msg) bool {
					var names []string
					switch op {
					case "label":
						names = append(append(names, m.Labels...), m.Cats...)
					case "category":
						names = m.Cats
					default:
						names = m.Tags
					}
					return slices.ContainsFunc(names, func(s string) bool { return strings.EqualFold(s, name) })
				}
				hasItem := func(i apiItem) bool {
					var names []string
					switch op {
					case "label":
						names = append(append(names, i.Labels...), i.Categories...)
					case "category":
						names = i.Categories
					default:
						names = tagNames(i.Tags)
					}
					return slices.ContainsFunc(names, func(s string) bool { return strings.EqualFold(s, name) })
				}
				qv := op + ":" + quote(name)
				c.queryCount(qv, "", has, hasItem)
				c.queryCount("-"+qv, "", func(m corpus.Msg) bool { return !has(m) }, func(i apiItem) bool { return !hasItem(i) })
			}
		}
	}
	// Folders (exact path through folder:, which also matches last segments;
	// use full paths only where no other folder ends with the same path).
	folders := map[string]bool{}
	for _, m := range c.msgs() {
		folders[m.Folder] = true
	}
	for f := range folders {
		ambiguous := false
		for g := range folders {
			if g != f && strings.HasSuffix(strings.ToLower(g), "/"+strings.ToLower(f)) {
				ambiguous = true
			}
		}
		if ambiguous {
			continue
		}
		fo := f
		c.queryCount("folder:"+quote(f), "", func(m corpus.Msg) bool { return m.Folder == fo }, func(i apiItem) bool { return i.Folder == fo })
		var items []apiItem
		items, _ = c.v.list("folder", fo)
		n := 0
		for _, m := range c.msgs() {
			if m.Folder == fo && c.visible(m) {
				n++
			}
		}
		for _, it := range c.unknownMsgs {
			if it.Folder == fo && !slices.Contains(it.Flags, "deleted") {
				n++
			}
		}
		if len(items) != n {
			c.t.Errorf("list folder=%q: %d items, want %d", fo, len(items), n)
		}
	}
	// Senders.
	for _, p := range []string{"alice@example.com", "bob@example.org", "pat@example.com"} {
		addr := p
		c.queryCount("from:"+quote(addr), "", func(m corpus.Msg) bool { return strings.EqualFold(m.From, addr) },
			func(i apiItem) bool { return strings.EqualFold(i.From.Email, addr) })
	}
	// Dates.
	for _, y := range []int{2013, 2015, 2018} {
		cut := time.Date(y, 1, 1, 0, 0, 0, 0, time.UTC)
		dated := func(m corpus.Msg) bool { return m.Exact && !m.Date.IsZero() && !m.Undated }
		if !c.allDatedExact() {
			continue
		}
		c.queryCount(fmt.Sprintf("before:%d", y), "", func(m corpus.Msg) bool { return dated(m) && m.Date.Before(cut) }, nil)
		c.queryCount(fmt.Sprintf("after:%d", y), "", func(m corpus.Msg) bool { return dated(m) && !m.Date.Before(cut) }, nil)
	}
	// Known-result searches.
	for _, s := range c.m.Searches {
		p := []string{"q", s.Q}
		if s.Deleted != "" {
			p = append(p, "deleted", s.Deleted)
		}
		items, _ := c.v.list(p...)
		var subj []string
		for _, it := range items {
			subj = append(subj, it.Subject)
		}
		sort.Strings(subj)
		if !slices.Equal(subj, sortedSet(s.Subjects)) {
			c.t.Errorf("search %q: subjects %q, want %q", s.Q, subj, s.Subjects)
		}
	}
	// A token and its negation exclude each other.
	for _, m := range c.msgs() {
		if m.Token != "" && m.Exact && c.visible(m) {
			var l apiList
			c.v.getJSON("/api/messages?"+q("q", m.Token+" -"+m.Token), &l)
			if l.Total != 0 {
				c.t.Errorf("query %q: %d results", m.Token+" -"+m.Token, l.Total)
			}
			c.v.getJSON("/api/messages?"+q("q", "subject:"+quote(m.Subject)+" "+m.Token, "sort", "relevance"), &l)
			if m.Subject != "" && l.Total != 1 {
				c.t.Errorf("query subject+token for %q: %d results", m.Subject, l.Total)
			}
			break
		}
	}
}

func (c *checker) unknownMsgsEmpty() bool { return len(c.unknownMsgs) == 0 }

// allDatedExact: every message has a known date (no inexact messages), so
// date-range totals can be computed.
func (c *checker) allDatedExact() bool {
	for _, m := range c.msgs() {
		if !m.Exact || m.Date.IsZero() {
			return false
		}
	}
	return len(c.unknownMsgs) == 0
}

func quote(s string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`) + `"`
}

func (c *checker) checkThreads() {
	groups := map[string][]corpus.Msg{}
	for _, m := range c.msgs() {
		if m.Thread != "" {
			groups[m.Thread] = append(groups[m.Thread], m)
		}
	}
	for key, ms := range groups {
		// Find a member by its subject.
		var l apiList
		c.v.getJSON("/api/messages?"+q("q", "subject:"+quote(ms[len(ms)-1].Subject), "deleted", "include", "limit", "1"), &l)
		if len(l.Items) == 0 {
			c.t.Errorf("thread %q: member %q not found", key, ms[len(ms)-1].Subject)
			continue
		}
		var th struct {
			Items     []apiItem
			Truncated bool
		}
		c.v.getJSON(fmt.Sprintf("/api/threads/%d?deleted=include", l.Items[0].ID), &th)
		want := len(ms)
		if want > 5000 {
			want = 5000
		}
		if len(th.Items) != want {
			c.t.Errorf("thread %q: %d messages, want %d", key, len(th.Items), want)
		}
		for i := 1; i < len(th.Items); i++ {
			a, b := th.Items[i-1].Date, th.Items[i].Date
			if a != nil && b != nil && b.Before(*a) {
				c.t.Errorf("thread %q: not oldest first at %d", key, i)
				break
			}
		}
		if len(th.Items) > 0 && th.Items[0].ThreadCount != len(ms) {
			c.t.Errorf("thread %q: threadCount %d, want %d", key, th.Items[0].ThreadCount, len(ms))
		}
	}
}

func (c *checker) checkStats() {
	var s struct {
		Total, Undated, OutOfRange, Unread, WithAttachments int64
		ByFolder                                            []struct {
			Path  string
			Count int64
		}
		Partial bool
	}
	c.v.getJSON("/api/stats", &s)
	if s.Partial {
		c.t.Errorf("stats partial after indexing")
	}
	if want := c.visibleCount(); s.Total != want {
		c.t.Errorf("stats total %d, want %d", s.Total, want)
	}
	var unread, att, out int64
	now := time.Now()
	for _, m := range c.msgs() {
		if !c.visible(m) {
			continue
		}
		if m.Flags.Has(source.Unread) {
			unread++
		}
		if m.Flags.Has(source.HasAttachment) {
			att++
		}
		if m.Exact && !m.Date.IsZero() && (m.Date.Year() < 1980 || m.Date.After(now.AddDate(1, 0, 0))) {
			out++
		}
	}
	for _, it := range c.unknownMsgs {
		if slices.Contains(it.Flags, "deleted") {
			continue
		}
		if slices.Contains(it.Flags, "unread") {
			unread++
		}
		if it.HasAttachment {
			att++
		}
	}
	if s.Unread != unread || s.WithAttachments != att {
		c.t.Errorf("stats unread %d / attachments %d, want %d / %d", s.Unread, s.WithAttachments, unread, att)
	}
	if c.allDatedExact() && s.OutOfRange != out {
		c.t.Errorf("stats outOfRange %d, want %d", s.OutOfRange, out)
	}
}

func (c *checker) checkSecurity() {
	t := c.t
	resp, _ := c.v.get("/")
	if resp.StatusCode != 200 {
		t.Errorf("GET /: %d", resp.StatusCode)
	}
	checkHeaders(t, "ui /", resp, "ui")
	for _, p := range []string{"/api/status", "/api/facets", "/api/messages?limit=1", "/api/stats"} {
		resp, _ := c.v.get(p)
		checkHeaders(t, "api "+p, resp, "api")
		if ct := resp.Header.Get("Content-Type"); ct != "application/json; charset=utf-8" {
			t.Errorf("%s: Content-Type %q", p, ct)
		}
	}
	for _, p := range []string{"/api/messages/999999999", "/api/nope", "/nope.html", "/api/messages/1/attachments/9999"} {
		resp, body := c.v.get(p)
		if resp.StatusCode != 404 {
			t.Errorf("%s: %d", p, resp.StatusCode)
		}
		if strings.Contains(string(body), c.m.Root) {
			t.Errorf("%s: error leaks a path", p)
		}
		if strings.HasPrefix(p, "/api/") {
			kind := "api"
			if strings.Contains(p, "/attachments/") {
				kind = "sandbox"
			}
			checkHeaders(t, "error "+p, resp, kind)
		}
	}
	for _, p := range []string{"/api/messages/1/../../etc/passwd", "/%2e%2e/etc/passwd", "/api/messages/1%00"} {
		resp, _ := c.v.get(p)
		if resp.StatusCode != 400 && resp.StatusCode != 404 {
			t.Errorf("%s: %d", p, resp.StatusCode)
		}
	}
	// Host check (DNS rebinding).
	req, _ := http.NewRequest("GET", c.v.base+"api/status", nil)
	req.Host = "attacker.example.com"
	if r, err := client.Do(req); err != nil || r.StatusCode != 403 {
		t.Errorf("foreign Host header accepted: %v %v", r, err)
	} else {
		r.Body.Close()
	}
	// Writes are refused.
	if r, err := client.Post(c.v.base+"api/messages", "text/plain", strings.NewReader("x")); err != nil || r.StatusCode != 405 {
		t.Errorf("POST accepted: %v %v", r, err)
	} else {
		r.Body.Close()
	}
}
