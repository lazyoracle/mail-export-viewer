package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/lazyoracle/mail-export-viewer/internal/index"
	"github.com/lazyoracle/mail-export-viewer/internal/source"
	_ "github.com/lazyoracle/mail-export-viewer/internal/source/mbox"
	_ "github.com/lazyoracle/mail-export-viewer/internal/source/olm"
	"github.com/lazyoracle/mail-export-viewer/internal/testutil"
)

var pngBytes = []byte("\x89PNG\r\n\x1a\nfake-png-data")

func plainMsg() string {
	return "From: Alice Example <alice@example.com>\nTo: Bob <bob@example.com>\nCc: Carol <carol@example.com>\n" +
		"Subject: Plain hello\nDate: Mon, 2 Jan 2023 10:00:00 +0000\nMessage-ID: <plain1@example.com>\n" +
		"X-Gmail-Labels: Inbox,Important,Travel\nX-Custom-Thing: kept as a prop\n\nHello Bob, this is plain text.\n"
}

func replyMsg() string {
	return "From: Bob <bob@example.com>\nTo: Alice Example <alice@example.com>\n" +
		"Subject: Re: Plain hello\nDate: Tue, 3 Jan 2023 10:00:00 +0000\nMessage-ID: <reply1@example.com>\n" +
		"In-Reply-To: <plain1@example.com>\nReferences: <plain1@example.com>\nX-Gmail-Labels: Inbox\n\nThanks Alice.\n"
}

func deletedMsg() string {
	return "From: Dora <dora@example.com>\nTo: Bob <bob@example.com>\nSubject: Thrown away\n" +
		"Date: Thu, 5 Jan 2023 10:00:00 +0000\nMessage-ID: <del1@example.com>\nX-Mozilla-Status: 0009\n\nGone.\n"
}

func htmlMsg() string {
	b64 := base64.StdEncoding.EncodeToString(pngBytes)
	return "From: Mallory <mallory@example.com>\nTo: Bob <bob@example.com>\n" +
		"Subject: Fancy newsletter\nDate: Wed, 4 Jan 2023 10:00:00 +0000\nMessage-ID: <html1@example.com>\n" +
		"X-Gmail-Labels: Inbox\nMIME-Version: 1.0\nContent-Type: multipart/mixed; boundary=\"OUTER\"\n\n" +
		"--OUTER\nContent-Type: multipart/related; boundary=\"REL\"\n\n" +
		"--REL\nContent-Type: text/html; charset=utf-8\n\n" +
		"<html><body><p onclick=\"x()\">Big sale today</p><script>alert('pwn')</script>" +
		"<img src=\"http://tracker.example/pixel.gif\"><img src=\"cid:logo@example.com\">" +
		"<a href=\"https://shop.example/\">Shop</a></body></html>\n" +
		"--REL\nContent-Type: image/png\nContent-ID: <logo@example.com>\nContent-Transfer-Encoding: base64\n" +
		"Content-Disposition: inline; filename=\"logo.png\"\n\n" + b64 + "\n" +
		"--REL--\n" +
		"--OUTER\nContent-Type: application/pdf\nContent-Disposition: attachment; filename*=UTF-8''r%C3%A9sum%C3%A9%20%E2%82%AC.pdf\n" +
		"Content-Transfer-Encoding: base64\n\n" + base64.StdEncoding.EncodeToString([]byte("%PDF-1.4 fake")) + "\n" +
		"--OUTER\nContent-Type: text/plain\nContent-Disposition: attachment; filename*=UTF-8''evil%0D%0ASet-Cookie%3A%20a%3Db%0D%0A%22.txt\n\n" +
		"injected?\n" +
		"--OUTER\nContent-Type: image/svg+xml\nContent-Disposition: attachment; filename=\"pic.svg\"\n\n" +
		"<svg xmlns=\"http://www.w3.org/2000/svg\"><script>alert(1)</script></svg>\n" +
		"--OUTER\nContent-Type: image/png\nContent-Disposition: attachment; filename=\"../../etc/photo.png\"\n" +
		"Content-Transfer-Encoding: base64\n\n" + b64 + "\n" +
		"--OUTER--\n"
}

func archiveMsg(i int) string {
	return fmt.Sprintf("From: Sender %d <s%d@example.com>\nTo: Bob <bob@example.com>\nSubject: Archive item %d\n"+
		"Date: Mon, 6 Feb 2023 10:00:%02d +0000\nMessage-ID: <arch%d@example.com>\nX-Mozilla-Keys: $label1\n\nArchived body %d.\n", i, i, i, i, i, i)
}

// olmMsg is an Outlook for Mac message with a category, flags, an HTML body
// with an inline cid image, and an attachment.
func olmMsg() testutil.OLMMsg {
	return testutil.OLMMsg{Subject: "Outlook budget", MessageID: "<o1@example.org>",
		From: testutil.OLMAddr{Name: "Frank", Email: "frank@example.org"},
		To:   []testutil.OLMAddr{{Name: "Bob", Email: "bob@example.com"}}, Body: "Budget numbers inside",
		HTML: `<p>Budget numbers <b>inside</b></p><img src="cid:chart@example.org"><script>bad()</script>`,
		Sent: "2023-03-08T10:00:00", Unread: true, Flagged: true, Categories: []string{"Finance"},
		Attachments: []testutil.OLMAtt{
			{Name: "budget.csv", Type: "text/csv", Data: []byte("a,b\n1,2\n")},
			{Name: "chart.png", Type: "image/png", CID: "chart@example.org", Data: pngBytes},
		}}
}

type fixture struct {
	t    *testing.T
	dir  string
	st   *index.Store
	srcs []source.Source
	srv  *Server
	h    http.Handler
}

// quietLog keeps expected internal-error logs out of the test output.
var quietLog = log.New(io.Discard, "", 0)

var testUI = fstest.MapFS{
	"index.html":       {Data: []byte("<!doctype html><title>ui</title>")},
	"app.js":           {Data: []byte("console.log(1)")},
	"css/style.css":    {Data: []byte("body{}")},
	"img/sub/logo.svg": {Data: []byte("<svg xmlns='http://www.w3.org/2000/svg'/>")},
	".hidden":          {Data: []byte("secret")},
}

// writeCorpus writes an mbox directory (Gmail Inbox, Thunderbird Archive
// with a tag and a deleted message) and an OLM archive.
func writeCorpus(t testing.TB) string {
	dir := t.TempDir()
	testutil.WriteMbox(t, filepath.Join(dir, "Inbox.mbox"), plainMsg(), replyMsg(), htmlMsg())
	var arch []string
	for i := 0; i < 5; i++ {
		arch = append(arch, archiveMsg(i))
	}
	arch = append(arch, deletedMsg())
	testutil.WriteMbox(t, filepath.Join(dir, "Archive"), arch...)
	testutil.WriteOLM(t, filepath.Join(dir, "backup.olm"), testutil.OLMMessageEntries(
		"Accounts/me@example.com/com.microsoft.__Messages/Inbox/msg1.olk15Message", olmMsg(),
		"Accounts/me@example.com/com.microsoft.__Attachments")...)
	return dir
}

func openSources(t testing.TB, root string) []source.Source {
	t.Helper()
	d, err := source.Discover(root, source.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	var out []source.Source
	for _, c := range d.Candidates {
		src, err := c.Open()
		if err != nil {
			t.Fatalf("open %s: %v", c.Rel, err)
		}
		t.Cleanup(func() { src.Close() })
		out = append(out, src)
	}
	return out
}

func newFixture(t *testing.T, mod ...func(*Config)) *fixture {
	t.Helper()
	return fixtureFor(t, writeCorpus(t), mod...)
}

// fixtureFor indexes the sources found in dir and serves them.
func fixtureFor(t *testing.T, dir string, mod ...func(*Config)) *fixture {
	t.Helper()
	srcs := openSources(t, dir)
	st, err := index.Open(filepath.Join(t.TempDir(), "index.db"), index.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.IndexSources(context.Background(), srcs, nil); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Store: st, Sources: srcs, Root: dir, UI: testUI, Version: "v-test", Logger: quietLog}
	for _, m := range mod {
		m(&cfg)
	}
	srv, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{t: t, dir: dir, st: st, srcs: srcs, srv: srv, h: srv}
}

func writeMbox(t testing.TB, path string, msgs ...string) { testutil.WriteMbox(t, path, msgs...) }

func jsonDecode(b []byte, v any) error { return json.Unmarshal(b, v) }

// id returns the id of the message with the given subject.
func (f *fixture) id(subject string) int64 {
	f.t.Helper()
	res, err := f.st.Search(index.SearchParams{Q: `subject:"` + subject + `" is:deleted`, Limit: 10})
	if err == nil {
		for _, it := range res.Items {
			if it.Subject == subject {
				return it.ID
			}
		}
	}
	res, err = f.st.Search(index.SearchParams{Q: `subject:"` + subject + `"`, Limit: 10})
	if err != nil {
		f.t.Fatal(err)
	}
	for _, it := range res.Items {
		if it.Subject == subject {
			return it.ID
		}
	}
	f.t.Fatalf("no message with subject %q", subject)
	return 0
}

func (f *fixture) do(method, target string) *httptest.ResponseRecorder {
	f.t.Helper()
	return serve(f.h, method, target, "127.0.0.1:8080")
}

func serve(h http.Handler, method, target, host string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	req.Host = host
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func (f *fixture) getJSON(target string, v any) *httptest.ResponseRecorder {
	f.t.Helper()
	rec := f.do("GET", target)
	if rec.Code != http.StatusOK {
		f.t.Fatalf("GET %s: status %d body %s", target, rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		f.t.Fatalf("GET %s: Content-Type %q", target, ct)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		f.t.Fatalf("GET %s: decode: %v\n%s", target, err, rec.Body)
	}
	return rec
}

// wantError asserts a JSON error response with the given status that does
// not reveal filesystem paths.
func (f *fixture) wantError(rec *httptest.ResponseRecorder, code int) {
	f.t.Helper()
	wantErrorIn(f.t, rec, code, f.dir)
}

func wantErrorIn(t testing.TB, rec *httptest.ResponseRecorder, code int, dir string) {
	t.Helper()
	if rec.Code != code {
		t.Fatalf("status %d, want %d; body %s", rec.Code, code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Fatalf("error Content-Type %q", ct)
	}
	var e struct{ Error, Code string }
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil || e.Error == "" || e.Code == "" {
		t.Fatalf("error body %q (%v)", rec.Body, err)
	}
	noPaths(t, rec.Body.String(), dir)
}

// noPaths fails if body mentions the corpus directory or a temp path.
func noPaths(t testing.TB, body, dir string) {
	t.Helper()
	tmp := os.TempDir()
	if (dir != "" && strings.Contains(body, dir)) || strings.Contains(body, tmp+"/") ||
		strings.Contains(body, "/home/") || strings.Contains(body, "/Users/") {
		t.Fatalf("response leaks a path: %s", body)
	}
}
