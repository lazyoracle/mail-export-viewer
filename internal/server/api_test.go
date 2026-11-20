package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lazyoracle/mail-export-viewer/internal/index"
	"github.com/lazyoracle/mail-export-viewer/internal/mail"
	"github.com/lazyoracle/mail-export-viewer/internal/source"
)

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

type tagJ struct{ Name, Color string }

type itemJ struct {
	ID, ThreadID, SourceID, FolderID int64
	ThreadCount                      int
	SourceKind, Folder, Subject      string
	From                             addrJ
	ToText                           string
	Date                             *string
	Flags                            []string
	HasAttachment                    bool
	Labels, Categories               []string
	Tags                             []tagJ
	Snippet, HighlightHTML           string
}

type addrJ struct{ Name, Email string }

type listJ struct {
	Items      []itemJ
	Total      int64
	NextCursor string
}

type attJ struct {
	Index                            int
	Filename, ContentType, ContentID string
	Size                             int64
	Inline                           bool
	URL                              string
}

type detailJ struct {
	ID, ThreadID, FolderID int64
	Source                 struct {
		ID         int64
		Name, Kind string
	}
	Folder, Subject, MessageID, InReplyTo string
	References                            []string
	From                                  addrJ
	To, Cc, Bcc                           []addrJ
	Date                                  *string
	Flags                                 []string
	Labels, Categories                    []string
	Tags                                  []tagJ
	Props                                 []struct{ Key, Value string }
	Headers                               [][2]string
	Text, BodyHTML                        string
	RemoteContentBlocked                  bool
	TextTruncated, HTMLTruncated          bool
	Attachments                           []attJ
	RawURL                                string
	ParseError                            bool
}

func TestStatusEndpoint(t *testing.T) {
	f := newFixture(t)
	var s statusJSON
	rec := f.getJSON("/api/status", &s)
	noPaths(t, rec.Body.String(), f.dir)
	if s.Version != "v-test" || s.Indexing.Running || !s.Indexing.Finished || s.IncludeDeleted {
		t.Fatalf("status = %+v", s)
	}
	names := map[string]statusSource{}
	for _, src := range s.Sources {
		names[src.Name] = src
	}
	for name, kind := range map[string]string{"Inbox.mbox": "mbox", "Archive": "mbox", "backup.olm": "olm"} {
		src, ok := names[name]
		if !ok || src.Kind != kind || src.ID == 0 || src.State != index.StateDone || !src.Complete || src.Count == 0 {
			t.Errorf("source %s = %+v (all %+v)", name, src, s.Sources)
		}
	}
	if names["Archive"].Count != 5 {
		t.Errorf("Archive count %d, want 5 (deleted hidden)", names["Archive"].Count)
	}
}

func TestStatusInputWarnings(t *testing.T) {
	root := "/data/exports"
	var warns []string
	for i := 0; i < 30; i++ {
		warns = append(warns, fmt.Sprintf("skipped %s/messy/fake%d.pst: pst: not a PST file %s", root, i, strings.Repeat("y", 2000)))
	}
	s := &Server{root: root, inputWarnings: warns}
	got := s.status(index.Progress{}, nil)
	if got.InputWarningCount != 30 || len(got.InputWarnings) != maxStatusWarnings {
		t.Fatalf("input warnings: count %d, listed %d", got.InputWarningCount, len(got.InputWarnings))
	}
	for i, w := range got.InputWarnings {
		if len(w) > maxWarningLen+len("…") || strings.Contains(w, root) || !strings.HasPrefix(w, fmt.Sprintf("skipped messy/fake%d.pst: pst:", i)) {
			t.Fatalf("input warning not bounded/scrubbed: %q", w)
		}
	}
	// None: an empty list, never null.
	if got := (&Server{root: root}).status(index.Progress{}, nil); got.InputWarnings == nil || got.InputWarningCount != 0 {
		t.Fatalf("no input warnings: %+v", got)
	}
}

func TestStatusMergeBoundsAndScrubs(t *testing.T) {
	root := "/data/exports"
	p := index.Progress{Running: true, Messages: 7, Warnings: 300, Sources: []index.SourceProgress{
		{Path: root + "/a.mbox", Kind: "mbox", State: index.StateIndexing, Unit: index.UnitBytes, Done: 10, Total: 100, Messages: 7, Warnings: 300},
		{Path: root + "/new.olm", Kind: "olm", State: index.StateQueued, Unit: index.UnitMessages},
	}}
	var warns []string
	for i := 0; i < 50; i++ {
		warns = append(warns, fmt.Sprintf("cannot parse %s/a.mbox at %d: %s", root, i, strings.Repeat("x", 2000)))
	}
	infos := []index.SourceInfo{
		{ID: 1, Path: root + "/a.mbox", Kind: "mbox", Count: 3, WarningCount: 300, Warnings: warns},
		{ID: 2, Path: "/elsewhere/old.pst", Kind: "pst", Complete: true, Count: 9, Error: "pst: open /elsewhere/old.pst: bad"},
	}
	s := &Server{root: root}
	got := s.status(p, infos)
	if !got.Indexing.Running || got.Indexing.Messages != 7 || got.Indexing.Warnings != 300 || len(got.Sources) != 3 {
		t.Fatalf("status = %+v", got)
	}
	a := got.Sources[0]
	if a.Name != "a.mbox" || a.ID != 1 || a.State != index.StateIndexing || a.Done != 10 || a.Total != 100 ||
		a.Count != 3 || a.WarningCount != 300 || len(a.Warnings) != maxStatusWarnings {
		t.Fatalf("a = %+v", a)
	}
	for _, w := range a.Warnings {
		if len(w) > maxWarningLen+len("…") || strings.Contains(w, root) || !strings.Contains(w, "a.mbox") {
			t.Fatalf("warning not bounded/scrubbed: %q", w)
		}
	}
	if n := got.Sources[1]; n.Name != "new.olm" || n.ID != 0 || n.State != index.StateQueued {
		t.Fatalf("queued source = %+v", n)
	}
	if o := got.Sources[2]; o.Name != "old.pst" || o.State != index.StateFailed || o.Count != 9 ||
		strings.Contains(o.Error, "/elsewhere") || !strings.Contains(o.Error, "old.pst") {
		t.Fatalf("persisted-only source = %+v", o)
	}
}

func TestFacetsEndpoint(t *testing.T) {
	f := newFixture(t)
	var fc facetsJSON
	rec := f.getJSON("/api/facets", &fc)
	noPaths(t, rec.Body.String(), f.dir)
	if len(fc.Sources) != 3 {
		t.Fatalf("sources = %+v", fc.Sources)
	}
	paths := map[string]folderJSON{}
	var walk func([]folderJSON)
	walk = func(ns []folderJSON) {
		for _, n := range ns {
			paths[n.Path] = n
			walk(n.Children)
		}
	}
	walk(fc.Folders)
	if n := paths["Inbox"]; n.Count != 3 || len(n.Sources) != 1 || n.Sources[0].FolderID == 0 || n.Sources[0].Source != "Inbox.mbox" {
		t.Errorf("Inbox node = %+v", n)
	}
	if n := paths["Archive"]; n.Count != 5 {
		t.Errorf("Archive node = %+v", n)
	}
	if n := paths["backup/Inbox"]; n.Count != 1 || n.Sources[0].SourceKind != "olm" {
		t.Errorf("olm node = %+v (all %v)", n, paths)
	}
	terms := func(ts []termJSON) string {
		var s []string
		for _, x := range ts {
			s = append(s, fmt.Sprintf("%s:%d", x.Name, x.Count))
		}
		return strings.Join(s, ",")
	}
	if got := terms(fc.Labels); got != "Important:1,Inbox:3,Travel:1" {
		t.Errorf("labels = %s", got)
	}
	if got := terms(fc.Categories); got != "Finance:1" {
		t.Errorf("categories = %s", got)
	}
	if got := terms(fc.Tags); got != "Important:5" {
		t.Errorf("tags = %s", got)
	}
	flags := map[string]int64{}
	for _, fl := range fc.Flags {
		flags[fl.Name] = fl.Count
	}
	if len(fc.Flags) != 9 || flags["unread"] != 1 || flags["starred"] != 1 || flags["attachment"] != 2 || flags["deleted"] != 1 {
		t.Errorf("flags = %+v", fc.Flags)
	}
}

func TestMessagesListAndPaging(t *testing.T) {
	f := newFixture(t)
	var all listJ
	f.getJSON("/api/messages", &all)
	if all.Total != 9 || len(all.Items) != 9 {
		t.Fatalf("all: total %d items %d", all.Total, len(all.Items))
	}
	if all.Items[0].Subject != "Outlook budget" { // newest first
		t.Errorf("first = %q", all.Items[0].Subject)
	}
	seen := map[int64]bool{}
	cursor := ""
	var subjects []string
	for page := 0; ; page++ {
		target := "/api/messages?limit=2&sort=date_asc"
		if cursor != "" {
			target += "&cursor=" + url.QueryEscape(cursor)
		}
		var l listJ
		f.getJSON(target, &l)
		if page == 0 && l.Total != 9 || page > 0 && l.Total != -1 {
			t.Fatalf("page %d total = %d", page, l.Total)
		}
		for _, it := range l.Items {
			if seen[it.ID] {
				t.Fatalf("duplicate id %d", it.ID)
			}
			seen[it.ID] = true
			subjects = append(subjects, it.Subject)
			if it.Date == nil || it.From.Email == "" || it.Labels == nil || it.Categories == nil || it.Tags == nil || it.Flags == nil ||
				it.SourceID == 0 || it.FolderID == 0 || it.SourceKind == "" || it.Folder == "" {
				t.Fatalf("bad item %+v", it)
			}
		}
		if l.NextCursor == "" {
			break
		}
		cursor = l.NextCursor
		if page > 10 {
			t.Fatal("paging does not terminate")
		}
	}
	if len(subjects) != 9 || subjects[0] != "Plain hello" || subjects[8] != "Outlook budget" {
		t.Fatalf("subjects = %v", subjects)
	}
}

func TestMessagesScopesAndOperators(t *testing.T) {
	f := newFixture(t)
	var fc facetsJSON
	f.getJSON("/api/facets", &fc)
	var archFolder, olmSource int64
	for _, n := range fc.Folders {
		if n.Path == "Archive" {
			archFolder = n.Sources[0].FolderID
		}
	}
	for _, s := range fc.Sources {
		if s.Kind == "olm" {
			olmSource = s.ID
		}
	}
	count := func(query string) (int, listJ) {
		t.Helper()
		var l listJ
		f.getJSON("/api/messages?"+query, &l)
		return len(l.Items), l
	}
	for q, want := range map[string]int{
		"folderId=" + itoa(archFolder):                   5,
		"source=" + itoa(olmSource):                      1,
		"folder=Inbox":                                   3,
		"folder=" + url.QueryEscape("backup/Inbox"):      1,
		"folderId=987654":                                0,
		"q=" + url.QueryEscape("from:alice"):             1,
		"q=" + url.QueryEscape(`label:"Important"`):      1,
		"q=" + url.QueryEscape("category:finance"):       1,
		"q=" + url.QueryEscape("tag:important"):          5,
		"q=" + url.QueryEscape("is:unread"):              1,
		"q=" + url.QueryEscape("is:starred"):             1,
		"q=" + url.QueryEscape("has:attachment"):         2,
		"q=" + url.QueryEscape("filename:budget"):        1,
		"q=" + url.QueryEscape("before:2023-01-03"):      1,
		"q=" + url.QueryEscape("folder:inbox -from:bob"): 3,
		"q=" + url.QueryEscape("is:deleted"):             1,
		"deleted=only":                                   1,
		"deleted=only&q=" + url.QueryEscape("archive"):   0,
		"q=archived&folder=Archive":                      5,
	} {
		if n, l := count(q); n != want {
			t.Errorf("%s: %d items, want %d (%+v)", q, n, want, l.Items)
		}
	}
	_, l := count("q=" + url.QueryEscape("subject:newsletter"))
	if len(l.Items) != 1 || !l.Items[0].HasAttachment || !strings.Contains(l.Items[0].HighlightHTML, "<mark>") {
		t.Fatalf("search = %+v", l)
	}
	_, l = count("q=" + url.QueryEscape("Alice") + "&sort=relevance")
	for _, it := range l.Items {
		if it.Subject == "Plain hello" && it.ThreadCount != 2 {
			t.Errorf("threadCount = %d, want 2", it.ThreadCount)
		}
	}
	_, l = count("q=budget")
	if len(l.Items) != 1 || l.Items[0].Categories[0] != "Finance" || !contains(l.Items[0].Flags, "unread") || !contains(l.Items[0].Flags, "starred") {
		t.Fatalf("olm item = %+v", l.Items)
	}
	_, l = count("folderId=" + itoa(archFolder) + "&limit=1")
	if it := l.Items[0]; len(it.Tags) != 1 || it.Tags[0].Name != "Important" {
		t.Errorf("tags = %+v", it.Tags)
	}
	for _, sort := range []string{"date_desc", "date_asc", "from", "subject", "relevance"} {
		if n, _ := count("sort=" + sort); n != 9 {
			t.Errorf("sort %s: %d items", sort, n)
		}
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// The snippet is plain text: markup in a message body stays literal text
// there, and only highlightHtml carries (escaped) HTML.
func TestSnippetIsTextHighlightIsEscaped(t *testing.T) {
	dir := t.TempDir()
	writeMbox(t, filepath.Join(dir, "Box"), "From: A <a@example.com>\nSubject: Markup\nDate: Mon, 2 Jan 2023 10:00:00 +0000\n\n"+
		"<img src=x onerror=alert(1)> zebra &amp; co\n")
	f := fixtureFor(t, dir)
	var l listJ
	f.getJSON("/api/messages?q=zebra", &l)
	if len(l.Items) != 1 {
		t.Fatalf("items = %+v", l.Items)
	}
	it := l.Items[0]
	if !strings.Contains(it.Snippet, "<img src=x") {
		t.Errorf("snippet = %q", it.Snippet)
	}
	if strings.Contains(it.HighlightHTML, "<img") || !strings.Contains(it.HighlightHTML, "&lt;img") ||
		!strings.Contains(it.HighlightHTML, "<mark>zebra</mark>") {
		t.Errorf("highlightHtml = %q", it.HighlightHTML)
	}
}

func TestMessagesBadParams(t *testing.T) {
	f := newFixture(t)
	for _, q := range []string{
		"limit=0", "limit=-1", "limit=1001", "limit=abc", "limit=1e3", "sort=bogus", "sort=date_desc;drop",
		"source=x", "source=-1", "source=0", "folderId=1%20OR%201=1", "cursor=garbage", "deleted=all", "deleted=1",
		"cursor=" + strings.Repeat("A", maxCursorLen+1), "q=" + strings.Repeat("a", maxQueryLen+1),
		"folder=" + strings.Repeat("a", maxFolderLen+1), "q=a&q=b",
	} {
		f.wantError(f.do("GET", "/api/messages?"+q), http.StatusBadRequest)
	}
	var l listJ
	f.getJSON("/api/messages?limit=1&sort=date_asc", &l)
	f.wantError(f.do("GET", "/api/messages?limit=1&sort=subject&cursor="+url.QueryEscape(l.NextCursor)), http.StatusBadRequest)
}

func TestMessageDetailPlain(t *testing.T) {
	f := newFixture(t)
	id := f.id("Plain hello")
	var d detailJ
	rec := f.getJSON("/api/messages/"+itoa(id), &d)
	noPaths(t, rec.Body.String(), f.dir)
	if d.ID != id || d.Source.Name != "Inbox.mbox" || d.Source.Kind != "mbox" || d.Source.ID == 0 || d.Folder != "Inbox" ||
		d.Subject != "Plain hello" || d.From != (addrJ{"Alice Example", "alice@example.com"}) ||
		len(d.To) != 1 || d.To[0].Email != "bob@example.com" || len(d.Cc) != 1 || d.Cc[0].Name != "Carol" ||
		d.Bcc == nil || d.Date == nil || *d.Date != "2023-01-02T10:00:00Z" || !strings.Contains(d.Text, "this is plain text") ||
		d.BodyHTML != "" || d.RemoteContentBlocked || len(d.Attachments) != 0 || d.Attachments == nil ||
		d.MessageID != "plain1@example.com" || d.ThreadID == 0 || d.RawURL != "/api/messages/"+itoa(id)+"/raw" {
		t.Fatalf("detail = %+v", d)
	}
	if strings.Join(d.Labels, ",") != "Inbox,Important,Travel" || d.Categories == nil || d.Tags == nil || d.Flags == nil {
		t.Errorf("labels = %v / %v / %v / %v", d.Labels, d.Categories, d.Tags, d.Flags)
	}
	var subj, prop bool
	for _, h := range d.Headers {
		subj = subj || h[0] == "Subject" && h[1] == "Plain hello"
	}
	for _, p := range d.Props {
		prop = prop || strings.EqualFold(p.Key, "X-Custom-Thing") && p.Value == "kept as a prop"
	}
	if !subj || !prop {
		t.Errorf("headers %v props %v", d.Headers, d.Props)
	}
	var r detailJ
	f.getJSON("/api/messages/"+itoa(f.id("Re: Plain hello")), &r)
	if r.InReplyTo != "plain1@example.com" || len(r.References) != 1 {
		t.Errorf("reply = %+v", r)
	}
}

func TestMessageDetailHTML(t *testing.T) {
	f := newFixture(t)
	id := f.id("Fancy newsletter")
	var d detailJ
	f.getJSON("/api/messages/"+itoa(id), &d)
	if strings.Contains(strings.ToLower(d.BodyHTML), "<script") || strings.Contains(d.BodyHTML, "onclick") || strings.Contains(d.BodyHTML, "pwn") {
		t.Errorf("html not sanitised: %s", d.BodyHTML)
	}
	assertSafeHTML(t, d.BodyHTML)
	if !d.RemoteContentBlocked || !strings.Contains(d.BodyHTML, `data-blocked-src="http://tracker.example/pixel.gif"`) {
		t.Errorf("remote image not blocked: %v %s", d.RemoteContentBlocked, d.BodyHTML)
	}
	if want := `src="data:image/png;base64,` + base64.StdEncoding.EncodeToString(pngBytes) + `"`; !strings.Contains(d.BodyHTML, want) {
		t.Errorf("cid not inlined (want %s): %s", want, d.BodyHTML)
	}
	if !strings.Contains(d.BodyHTML, `target="_blank"`) || !strings.Contains(d.BodyHTML, `rel="noopener noreferrer"`) {
		t.Errorf("link not external: %s", d.BodyHTML)
	}
	if !strings.Contains(d.Text, "Big sale today") || strings.Contains(d.Text, "<p") {
		t.Errorf("text from html = %q", d.Text)
	}
	if len(d.Attachments) != 5 {
		t.Fatalf("attachments = %+v", d.Attachments)
	}
	for i, a := range d.Attachments {
		if a.Index != i || a.URL != fmt.Sprintf("/api/messages/%d/attachments/%d", id, i) {
			t.Errorf("attachment %d = %+v", i, a)
		}
	}
	if a := d.Attachments[0]; a.Filename != "logo.png" || a.ContentType != "image/png" || !a.Inline ||
		a.Size != int64(len(pngBytes)) || a.ContentID != "logo@example.com" {
		t.Errorf("logo attachment = %+v", a)
	}
	if a := d.Attachments[1]; a.Filename != "résumé €.pdf" {
		t.Errorf("pdf attachment = %+v", a)
	}
}

func TestMessageDetailOLM(t *testing.T) {
	f := newFixture(t)
	id := f.id("Outlook budget")
	var d detailJ
	f.getJSON("/api/messages/"+itoa(id), &d)
	if d.Source.Kind != "olm" || d.Source.Name != "backup.olm" || strings.Join(d.Categories, ",") != "Finance" ||
		!contains(d.Flags, "unread") || !contains(d.Flags, "starred") || d.From.Email != "frank@example.org" {
		t.Fatalf("olm detail = %+v", d)
	}
	assertSafeHTML(t, d.BodyHTML)
	if strings.Contains(d.BodyHTML, "bad()") || !strings.Contains(d.BodyHTML, "<b>inside</b>") {
		t.Errorf("olm html = %s", d.BodyHTML)
	}
	if want := `src="data:image/png;base64,` + base64.StdEncoding.EncodeToString(pngBytes) + `"`; !strings.Contains(d.BodyHTML, want) {
		t.Errorf("olm cid not inlined: %s", d.BodyHTML)
	}
	if len(d.Attachments) != 2 || d.Attachments[0].Filename != "budget.csv" || d.Attachments[1].ContentID != "chart@example.org" {
		t.Errorf("olm attachments = %+v", d.Attachments)
	}
	rec := f.do("GET", d.Attachments[0].URL)
	if rec.Code != 200 || rec.Body.String() != "a,b\n1,2\n" || rec.Header().Get("Content-Type") != "text/csv" {
		t.Errorf("olm attachment: %d %q %q", rec.Code, rec.Header().Get("Content-Type"), rec.Body)
	}
	rec = f.do("GET", "/api/messages/"+itoa(id)+"/cid/chart@example.org")
	if rec.Code != 200 || !bytes.Equal(rec.Body.Bytes(), pngBytes) {
		t.Errorf("olm cid: %d", rec.Code)
	}
}

func TestAttachmentDownload(t *testing.T) {
	f := newFixture(t)
	id := f.id("Fancy newsletter")
	base := fmt.Sprintf("/api/messages/%d/attachments/", id)

	rec := f.do("GET", base+"1")
	if rec.Code != 200 || rec.Body.String() != "%PDF-1.4 fake" {
		t.Fatalf("pdf: %d %q", rec.Code, rec.Body)
	}
	cd := rec.Header().Get("Content-Disposition")
	if cd != `attachment; filename="r_sum_ _.pdf"; filename*=UTF-8''r%C3%A9sum%C3%A9%20%E2%82%AC.pdf` {
		t.Errorf("pdf Content-Disposition = %q", cd)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" || rec.Header().Get("Content-Type") != "application/pdf" {
		t.Errorf("pdf headers = %v", rec.Header())
	}
	if rec.Header().Get("Content-Length") != strconv.Itoa(len("%PDF-1.4 fake")) {
		t.Errorf("Content-Length = %q", rec.Header().Get("Content-Length"))
	}
	// CRLF in the filename must not inject headers.
	rec = f.do("GET", base+"2")
	cd = rec.Header().Get("Content-Disposition")
	if rec.Code != 200 || strings.ContainsAny(cd, "\r\n") || rec.Header().Get("Set-Cookie") != "" ||
		!strings.HasPrefix(cd, "attachment; ") || strings.Count(cd, `"`) != 2 {
		t.Errorf("crlf attachment: %d %q %v", rec.Code, cd, rec.Header())
	}
	if !strings.Contains(cd, `filename*=UTF-8''evilSet-Cookie_%20a%3Db_.txt`) {
		t.Errorf("crlf filename* = %q", cd)
	}
	// SVG is never inline, even when asked, and never served as SVG.
	rec = f.do("GET", base+"3?inline=1")
	if cd := rec.Header().Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment;") ||
		rec.Header().Get("Content-Type") != "application/octet-stream" {
		t.Errorf("svg inline: %q %q", cd, rec.Header().Get("Content-Type"))
	}
	// PNG inline when asked, attachment otherwise; path components stripped.
	rec = f.do("GET", base+"4?inline=1")
	if cd := rec.Header().Get("Content-Disposition"); cd != `inline; filename="photo.png"; filename*=UTF-8''photo.png` ||
		rec.Header().Get("Content-Type") != "image/png" || !bytes.Equal(rec.Body.Bytes(), pngBytes) {
		t.Errorf("png inline: %q %q", cd, rec.Header().Get("Content-Type"))
	}
	if cd := f.do("GET", base+"4").Header().Get("Content-Disposition"); !strings.HasPrefix(cd, "attachment;") {
		t.Errorf("png default: %q", cd)
	}
	f.wantError(f.do("GET", base+"99"), http.StatusNotFound)
	for _, bad := range []string{"x", "-1", "1.0", "%2e%2e", "1;2", "+1", "99999999999999999999"} {
		f.wantError(f.do("GET", base+bad), http.StatusBadRequest)
	}
	f.wantError(f.do("GET", base+"1?inline=yes"), http.StatusBadRequest)
}

func TestServedType(t *testing.T) {
	for ct, want := range map[string]string{
		"image/svg+xml": "application/octet-stream", "text/html; charset=utf-8": "application/octet-stream",
		"application/xhtml+xml": "application/octet-stream", "application/javascript": "application/octet-stream",
		"image/JPEG": "image/jpeg", "application/zip": "application/zip", "garbage": "application/octet-stream",
		"application/vnd.openxmlformats-officedocument.wordprocessingml.document": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		"application/vnd.openxmlformats-officedocument.x+xml":                     "application/octet-stream",
		"video/mp4": "video/mp4", "audio/mpeg": "audio/mpeg", "text/xml": "application/octet-stream", "": "application/octet-stream",
		"text/plain; charset=iso-8859-1": "text/plain",
	} {
		if got := servedType(ct); got != want {
			t.Errorf("servedType(%q) = %q, want %q", ct, got, want)
		}
	}
}

func TestContentDisposition(t *testing.T) {
	for _, c := range []struct{ kind, name, def, want string }{
		{"attachment", "report.pdf", "x", `attachment; filename="report.pdf"; filename*=UTF-8''report.pdf`},
		{"attachment", `..\..\win\evil.exe`, "x", `attachment; filename="evil.exe"; filename*=UTF-8''evil.exe`},
		{"attachment", "a\"b;c%d.txt", "x", `attachment; filename="a_b_c_d.txt"; filename*=UTF-8''a_b_c%25d.txt`},
		{"attachment", "con<>:|?*.txt", "x", `attachment; filename="con______.txt"; filename*=UTF-8''con______.txt`},
		{"attachment", "‮gnp.exe", "x", `attachment; filename="gnp.exe"; filename*=UTF-8''gnp.exe`},
		{"attachment", " ...  ", "fallback.bin", `attachment; filename="fallback.bin"; filename*=UTF-8''fallback.bin`},
		{"inline", "日本.png", "x", `inline; filename="__.png"; filename*=UTF-8''%E6%97%A5%E6%9C%AC.png`},
	} {
		if got := contentDisposition(c.kind, c.name, c.def); got != c.want {
			t.Errorf("contentDisposition(%q) = %q, want %q", c.name, got, c.want)
		}
	}
	long := contentDisposition("attachment", strings.Repeat("é", 300)+".pdf", "x")
	if len(long) > 2000 || strings.Count(long, `"`) != 2 {
		t.Errorf("long name: %d bytes", len(long))
	}
}

func TestCIDEndpoint(t *testing.T) {
	f := newFixture(t)
	id := f.id("Fancy newsletter")
	rec := f.do("GET", fmt.Sprintf("/api/messages/%d/cid/logo@example.com", id))
	if rec.Code != 200 || !bytes.Equal(rec.Body.Bytes(), pngBytes) || rec.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("cid: %d %q", rec.Code, rec.Header())
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.HasPrefix(cd, "inline;") {
		t.Errorf("cid Content-Disposition %q", cd)
	}
	if rec := f.do("GET", fmt.Sprintf("/api/messages/%d/cid/%%3Clogo@example.com%%3E", id)); rec.Code != 200 {
		t.Errorf("bracketed cid: %d", rec.Code)
	}
	f.wantError(f.do("GET", fmt.Sprintf("/api/messages/%d/cid/nope@x", id)), http.StatusNotFound)
	f.wantError(f.do("GET", fmt.Sprintf("/api/messages/%d/cid/a%%2F..%%2F..%%2Fetc", id)), http.StatusNotFound)
	f.wantError(f.do("GET", fmt.Sprintf("/api/messages/%d/cid/%s", id, strings.Repeat("a", maxCIDLen+1))), http.StatusBadRequest)
}

// The sandboxed message iframe has an opaque origin, so images it loads are
// cross-origin requests: the tokenised cid URL the sanitiser emits must
// allow that (CORP cross-origin), and only with a valid per-message token.
func TestCIDTokenAllowsSandboxedFrame(t *testing.T) {
	f := newFixture(t, func(c *Config) { c.inlinePart, c.inlineTotal = 4, 4 })
	id := f.id("Fancy newsletter")
	var d detailJ
	f.getJSON("/api/messages/"+itoa(id), &d)
	m := regexp.MustCompile(`src="(/api/messages/\d+/cid/([0-9a-f]{32})/logo@example.com)"`).FindStringSubmatch(d.BodyHTML)
	if m == nil {
		t.Fatalf("no tokenised cid URL in %s", d.BodyHTML)
	}
	rec := f.do("GET", m[1])
	if rec.Code != 200 || !bytes.Equal(rec.Body.Bytes(), pngBytes) {
		t.Fatalf("tokenised cid: %d", rec.Code)
	}
	if got := rec.Header().Get("Cross-Origin-Resource-Policy"); got != "cross-origin" {
		t.Errorf("CORP = %q, want cross-origin", got)
	}
	bad := strings.Replace(m[1], m[2], strings.Repeat("0", 32), 1)
	f.wantError(f.do("GET", bad), http.StatusNotFound)
	other := f.id("Plain hello")
	f.wantError(f.do("GET", fmt.Sprintf("/api/messages/%d/cid/%s/logo@example.com", other, m[2])), http.StatusNotFound)
	rec = f.do("GET", fmt.Sprintf("/api/messages/%d/cid/logo@example.com", id))
	if rec.Code != 200 || rec.Header().Get("Cross-Origin-Resource-Policy") != "same-origin" {
		t.Errorf("plain cid: %d CORP %q", rec.Code, rec.Header().Get("Cross-Origin-Resource-Policy"))
	}
	// Another server instance (another process) has another key.
	g := newFixture(t)
	g.wantError(g.do("GET", m[1]), http.StatusNotFound)
}

func TestRawEndpoint(t *testing.T) {
	f := newFixture(t)
	id := f.id("Plain hello")
	rec := f.do("GET", "/api/messages/"+itoa(id)+"/raw")
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "message/rfc822" {
		t.Fatalf("raw: %d %v", rec.Code, rec.Header())
	}
	if cd := rec.Header().Get("Content-Disposition"); cd != `attachment; filename="Plain hello.eml"; filename*=UTF-8''Plain%20hello.eml` {
		t.Errorf("raw Content-Disposition %q", cd)
	}
	if !strings.HasPrefix(rec.Body.String(), "From: Alice Example") || !strings.Contains(rec.Body.String(), "this is plain text") {
		t.Errorf("raw body %q", rec.Body)
	}
	// OLM messages are synthesised.
	rec = f.do("GET", "/api/messages/"+itoa(f.id("Outlook budget"))+"/raw")
	m := mail.Parse(rec.Body.Bytes(), time.Time{})
	if rec.Code != 200 || m.Subject != "Outlook budget" || len(m.Attachments) != 2 {
		t.Errorf("olm raw: %d %+v", rec.Code, m.Envelope)
	}
}

func TestThreadEndpoint(t *testing.T) {
	f := newFixture(t)
	id := f.id("Re: Plain hello")
	var th struct{ Items []itemJ }
	f.getJSON("/api/threads/"+itoa(id), &th)
	if len(th.Items) != 2 || th.Items[0].Subject != "Plain hello" || th.Items[1].Subject != "Re: Plain hello" ||
		th.Items[0].ThreadCount != 2 {
		t.Fatalf("thread = %+v", th)
	}
	f.wantError(f.do("GET", "/api/threads/987654"), http.StatusNotFound)
	f.wantError(f.do("GET", "/api/threads/abc"), http.StatusBadRequest)
}

func TestNotFoundAndInjection(t *testing.T) {
	f := newFixture(t)
	for _, p := range []string{"/api/messages/987654", "/api/messages/987654/raw", "/api/messages/987654/attachments/0",
		"/api/messages/987654/cid/x", "/api/nope", "/api/messages/1/nope", "/api", "/api/", "/api/status/x"} {
		f.wantError(f.do("GET", p), http.StatusNotFound)
	}
	for _, p := range []string{"/api/messages/abc", "/api/messages/0", "/api/messages/-1", "/api/messages/+1",
		"/api/messages/1;drop", "/api/messages/1%20OR%201=1", "/api/messages/0x10", "/api/messages/99999999999999999999",
		"/api/messages/%2e%2e%2f%2e%2e%2fetc%2fpasswd"} {
		f.wantError(f.do("GET", p), http.StatusBadRequest)
	}
}

// Message ids change when the index is rebuilt: an old deep link must get a
// clean 404, not another message or a 500.
func TestStaleIDsAfterReset(t *testing.T) {
	f := newFixture(t)
	id := f.id("Plain hello")
	if err := f.st.Reset(); err != nil {
		t.Fatal(err)
	}
	if err := f.st.IndexSources(context.Background(), f.srcs, nil); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"", "/raw", "/attachments/0", "/cid/x"} {
		rec := f.do("GET", "/api/messages/"+itoa(id)+p)
		f.wantError(rec, http.StatusNotFound)
		if !strings.Contains(rec.Body.String(), `"code":"not_found"`) {
			t.Errorf("%s: %s", p, rec.Body)
		}
	}
	f.wantError(f.do("GET", "/api/threads/"+itoa(id)), http.StatusNotFound)
}

func TestSourceFileChangedOrGone(t *testing.T) {
	f := newFixture(t)
	id := f.id("Fancy newsletter")
	inbox := filepath.Join(f.dir, "Inbox.mbox")
	if err := os.WriteFile(inbox, []byte(strings.Repeat("garbage\n", 500)), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"", "/raw", "/attachments/0", "/cid/logo@example.com"} {
		rec := f.do("GET", fmt.Sprintf("/api/messages/%d%s", id, p))
		f.wantError(rec, http.StatusGone)
		if !strings.Contains(rec.Body.String(), `"code":"stale"`) {
			t.Errorf("%s: %s", p, rec.Body)
		}
	}
	if err := os.Remove(inbox); err != nil {
		t.Fatal(err)
	}
	f.wantError(f.do("GET", fmt.Sprintf("/api/messages/%d", id)), http.StatusGone)
	var l listJ
	f.getJSON("/api/messages", &l) // listing still works from the index
}

// A message whose source is not among Config.Sources (left over in the
// index from an input no longer opened) is reported gone.
func TestSourceNotOpened(t *testing.T) {
	f := newFixture(t)
	var keep []source.Source
	for _, s := range f.srcs {
		if s.Kind() != "olm" {
			keep = append(keep, s)
		}
	}
	srv, err := New(Config{Store: f.st, Sources: keep, Root: f.dir, Logger: quietLog})
	if err != nil {
		t.Fatal(err)
	}
	rec := serve(srv, "GET", "/api/messages/"+itoa(f.id("Outlook budget")), "localhost")
	f.wantError(rec, http.StatusGone)
	if !strings.Contains(rec.Body.String(), `"code":"gone"`) {
		t.Errorf("body %s", rec.Body)
	}
}

func TestTooLargeMapsTo413(t *testing.T) {
	f := newFixture(t)
	for err, code := range map[error]int{
		fmt.Errorf("x: %w", source.ErrTooLarge):               http.StatusRequestEntityTooLarge,
		fmt.Errorf("x: %w", source.ErrStale):                  http.StatusGone,
		fmt.Errorf("x: %w", mail.ErrNotFound):                 http.StatusNotFound,
		index.ErrNotFound:                                     http.StatusNotFound,
		index.ErrBadCursor:                                    http.StatusBadRequest,
		errors.New("open " + f.dir + "/x: permission denied"): http.StatusInternalServerError,
	} {
		rec := serve(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { f.srv.fail(w, err) }), "GET", "/", "localhost")
		f.wantError(rec, code)
	}
}

// slowSource emits one message, then blocks until released, so handlers can
// be exercised while indexing runs.
type slowSource struct {
	path    string
	raw     string
	started chan struct{}
	release chan struct{}
}

func (s *slowSource) Kind() string     { return source.Mbox }
func (s *slowSource) Path() string     { return s.path }
func (s *slowSource) Inputs() []string { return nil }
func (s *slowSource) Scan(ctx context.Context, emit func(source.Entry) error, warn func(error)) error {
	if err := emit(source.Entry{Folder: "Slow", Raw: []byte(s.raw), Loc: source.Locator{Name: "one"}}); err != nil {
		return err
	}
	warn(errors.New("slow: odd thing in " + s.path))
	close(s.started)
	select {
	case <-s.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
}
func (s *slowSource) Load(source.Locator) (*mail.Message, error) {
	m := mail.Parse([]byte(s.raw), time.Time{})
	return m, nil
}
func (s *slowSource) Raw(source.Locator) ([]byte, error) { return []byte(s.raw), nil }
func (s *slowSource) Attachment(loc source.Locator, i int) (mail.Attachment, []byte, error) {
	return mail.AttachmentData([]byte(s.raw), i)
}
func (s *slowSource) Close() error { return nil }

func TestHandlersWhileIndexing(t *testing.T) {
	dir := t.TempDir()
	slow := &slowSource{path: filepath.Join(dir, "slow.mbox"), started: make(chan struct{}), release: make(chan struct{}),
		raw: "From: S <s@example.com>\nSubject: Early bird\nDate: Mon, 2 Jan 2023 10:00:00 +0000\n\nfirst\n"}
	st, err := index.Open(filepath.Join(t.TempDir(), "index.db"), index.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	srv, err := New(Config{Store: st, Sources: []source.Source{slow}, Root: dir, Logger: quietLog})
	if err != nil {
		t.Fatal(err)
	}
	// Before any run, everything answers too.
	for _, p := range []string{"/api/status", "/api/facets", "/api/messages"} {
		if rec := serve(srv, "GET", p, "localhost"); rec.Code != 200 {
			t.Fatalf("before run %s: %d %s", p, rec.Code, rec.Body)
		}
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := st.IndexSources(context.Background(), []source.Source{slow}, nil); err != nil {
			t.Error(err)
		}
	}()
	<-slow.started
	defer func() { close(slow.release); wg.Wait() }()
	for _, p := range []string{"/api/status", "/api/facets", "/api/messages", "/api/messages?q=early"} {
		rec := serve(srv, "GET", p, "localhost")
		if rec.Code != 200 {
			t.Fatalf("during run %s: %d %s", p, rec.Code, rec.Body)
		}
		noPaths(t, rec.Body.String(), dir)
	}
	var s statusJSON
	if err := jsonDecode(serve(srv, "GET", "/api/status", "localhost").Body.Bytes(), &s); err != nil {
		t.Fatal(err)
	}
	if !s.Indexing.Running || len(s.Sources) != 1 || s.Sources[0].State != index.StateIndexing || s.Sources[0].Name != "slow.mbox" {
		t.Fatalf("status during run = %+v", s)
	}
}

// deleted=only is enforced outside the query text: no query (open quote,
// 4 KiB, 64 items) can bring normal messages in, and the default view never
// shows deleted ones.
func TestDeletedParam(t *testing.T) {
	f := newFixture(t)
	queries := []string{"", "bob", `"bob`, `subject:"x`, `subject:"thrown`, strings.Repeat("bob ", 1024),
		strings.TrimSpace(strings.Repeat("bob ", 64)), "-is:deleted", `label:"`}
	for _, q := range queries {
		if len(q) > maxQueryLen {
			t.Fatalf("query of %d bytes over the cap", len(q))
		}
		for _, mode := range []string{"only", "", "hide", "include"} {
			target := "/api/messages?limit=1000&q=" + url.QueryEscape(q)
			if mode != "" {
				target += "&deleted=" + mode
			}
			var l listJ
			f.getJSON(target, &l)
			var del, normal int
			for _, it := range l.Items {
				if contains(it.Flags, "deleted") {
					del++
				} else {
					normal++
				}
			}
			switch mode {
			case "only":
				if normal != 0 {
					t.Errorf("q=%.20q only: %d normal messages", q, normal)
				}
			case "", "hide":
				if del != 0 {
					t.Errorf("q=%.20q %s: %d deleted messages", q, mode, del)
				}
			}
		}
	}
	count := func(target string) int {
		var l listJ
		f.getJSON(target, &l)
		return len(l.Items)
	}
	if n := count("/api/messages?q=bob&deleted=only"); n != 1 {
		t.Errorf("only: %d", n)
	}
	if n := count("/api/messages?q=" + url.QueryEscape(strings.Repeat("bob ", 1024)) + "&deleted=only"); n != 1 {
		t.Errorf("only, 4 KiB query: %d", n)
	}
	if a, b := count("/api/messages?deleted=include"), count("/api/messages"); a != 10 || b != 9 {
		t.Errorf("include %d, default %d", a, b)
	}
	var fc facetsJSON
	f.getJSON("/api/facets?deleted=include", &fc)
	for _, n := range fc.Folders {
		if n.Path == "Archive" && n.Count != 6 {
			t.Errorf("facets include: Archive %d", n.Count)
		}
	}
	f.getJSON("/api/facets", &fc)
	for _, n := range fc.Folders {
		if n.Path == "Archive" && n.Count != 5 {
			t.Errorf("facets default: Archive %d", n.Count)
		}
	}
	var th struct{ Items []itemJ }
	f.getJSON("/api/threads/"+itoa(f.id("Thrown away"))+"?deleted=hide", &th)
	if len(th.Items) != 1 {
		t.Errorf("thread of a deleted message: %+v", th.Items)
	}
	for _, p := range []string{"/api/messages?deleted=all", "/api/facets?deleted=1", "/api/threads/1?deleted=x", "/api/facets?deleted=only"} {
		f.wantError(f.do("GET", p), http.StatusBadRequest)
	}
	// The global default is reported and followed.
	g := newFixture(t, func(c *Config) { c.IncludeDeleted = true })
	var s statusJSON
	g.getJSON("/api/status", &s)
	if !s.IncludeDeleted {
		t.Error("includeDeleted not reported")
	}
}

func TestThreadCapped(t *testing.T) {
	if testing.Short() {
		t.Skip("indexes 6000 messages")
	}
	dir := t.TempDir()
	msgs := make([]string, 6000)
	for i := range msgs {
		irt := ""
		if i > 0 {
			irt = fmt.Sprintf("In-Reply-To: <c%d@example.com>\n", i-1)
		}
		msgs[i] = fmt.Sprintf("From: P <p@example.com>\nSubject: chain %d\nMessage-ID: <c%d@example.com>\n%s"+
			"Date: %s\n\nx\n", i, i, irt, time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(i)*time.Minute).Format(time.RFC1123Z))
	}
	writeMbox(t, filepath.Join(dir, "Chain"), msgs...)
	f := fixtureFor(t, dir)
	var th struct {
		Items     []itemJ
		Truncated bool
	}
	last := f.id("chain 5999")
	f.getJSON("/api/threads/"+itoa(last), &th)
	if len(th.Items) != maxThreadRows || !th.Truncated || th.Items[len(th.Items)-1].ID != last ||
		th.Items[0].Subject != "chain 0" || th.Items[0].ThreadCount != 6000 {
		t.Fatalf("capped thread: %d items, truncated %v, last %d, first %+v", len(th.Items), th.Truncated, th.Items[len(th.Items)-1].ID, th.Items[0])
	}
	f.getJSON("/api/threads/"+itoa(f.id("chain 3")), &th)
	if len(th.Items) != maxThreadRows || th.Items[3].Subject != "chain 3" {
		t.Fatalf("early member: %d items", len(th.Items))
	}
}

func TestTagColorsValidated(t *testing.T) {
	got := tagsJSON([]index.Tag{{Name: "a", Color: "#FF9900"}, {Name: "b", Color: "#ff99aa"}, {Name: "c", Color: "red"}, {Name: "d", Color: "#fff"}, {Name: "e", Color: "#12345G"},
		{Name: "f", Color: "#FF9900;background:url(x)"}, {Name: "g", Color: ""}})
	want := []string{"#FF9900", "#ff99aa", "", "", "", "", ""}
	for i, tg := range got {
		if tg.Color != want[i] {
			t.Errorf("%s: colour %q, want %q", tg.Name, tg.Color, want[i])
		}
	}
	if c := terms([]index.TermCount{{Name: "x", Color: "javascript:x"}})[0].Color; c != "" {
		t.Errorf("term colour %q", c)
	}
}

func TestLoadWeight(t *testing.T) {
	for _, c := range []struct{ length, factor, want int64 }{
		{1000, 1, 1000}, {1000, 3, 3000}, {0, 1, unknownSize}, {0, 3, 3 * unknownSize},
	} {
		if got := loadWeight(c.length, c.factor); got != c.want {
			t.Errorf("loadWeight(%d, %d) = %d, want %d", c.length, c.factor, got, c.want)
		}
	}
}
