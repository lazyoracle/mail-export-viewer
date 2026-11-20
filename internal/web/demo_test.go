package web

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
	"time"

	"github.com/lazyoracle/mail-export-viewer/internal/index"
	"github.com/lazyoracle/mail-export-viewer/internal/server"
	"github.com/lazyoracle/mail-export-viewer/internal/source"
	_ "github.com/lazyoracle/mail-export-viewer/internal/source/mbox"
	_ "github.com/lazyoracle/mail-export-viewer/internal/source/olm"
	"github.com/lazyoracle/mail-export-viewer/internal/testutil"
)

// A tiny PNG (1x1, transparent) for inline and attached images.
var demoPNG, _ = base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==")

var demoPeople = []string{
	"Ada Example <ada@example.com>", "Bruno Sample <bruno@example.org>", "Chen Placeholder <chen@example.com>",
	"Dana Fictional <dana@example.org>", "Eli Invented <eli@example.com>", "Fatima Madeup <fatima@example.net>",
	"Newsletter <news@example.net>", "Billing <billing@example.com>",
}

var demoSubjects = []string{
	"Quarterly report draft", "Lunch on Friday?", "Flight itinerary", "Invoice 2041", "Garden club minutes",
	"Re: budget questions", "Photos from the trip", "Weekly digest", "Contract renewal", "Meeting notes",
}

func demoMsg(i int, extra, body string) string {
	from := demoPeople[i%len(demoPeople)]
	subj := demoSubjects[i%len(demoSubjects)]
	d := time.Date(2019+i%6, time.Month(1+(i*7)%12), 1+(i*3)%27, 9+i%9, (i*13)%60, 0, 0, time.UTC)
	return fmt.Sprintf("From: %s\nTo: Me <me@example.com>\nSubject: %s\nDate: %s\nMessage-ID: <demo%d@example.com>\n%s\n%s\n",
		from, subj, d.Format(time.RFC1123Z), i, extra, body)
}

// writeDemoCorpus writes a synthetic archive: a Gmail Takeout mbox with
// labels and a thread, Thunderbird folders with flags and tags, an HTML
// message with a remote image, a cid image and attachments, and an OLM
// export with categories. n scales the number of messages.
func writeDemoCorpus(t testing.TB, dir string, n int) {
	gmailLabels := []string{"Inbox", "Inbox,Work", "Travel,Starred", "Category Updates", "Inbox,Unread,Family", "Sent", "Work,Projects/2024"}
	var gmail []string
	for i := 0; i < n; i++ {
		gmail = append(gmail, demoMsg(i, "X-Gmail-Labels: "+gmailLabels[i%len(gmailLabels)],
			fmt.Sprintf("Hello,\n\nThis is synthetic message %d for the demo. See https://example.com/page%d for details.\n\n-- \nA sender\n", i, i)))
	}
	// A conversation of four messages.
	for k := 0; k < 4; k++ {
		ref := ""
		if k > 0 {
			ref = fmt.Sprintf("In-Reply-To: <thread%d@example.com>\nReferences: <thread0@example.com>\n", k-1)
		}
		gmail = append(gmail, fmt.Sprintf("From: %s\nTo: Me <me@example.com>\nSubject: %sPlanning the offsite\n"+
			"Date: Mon, %d Mar 2024 10:00:00 +0000\nMessage-ID: <thread%d@example.com>\n%sX-Gmail-Labels: Inbox,Work\n\nPart %d of the plan.\n",
			demoPeople[k], map[bool]string{true: "Re: ", false: ""}[k > 0], 4+k, k, ref, k+1))
	}
	gmail = append(gmail, demoHTML())
	testutil.WriteMbox(t, filepath.Join(dir, "Takeout", "All mail Including Spam and Trash.mbox"), gmail...)

	statuses := []string{"0001", "0000", "0005", "0003", "1001", "0009", "0001"}
	keys := []string{"", "$label1", "$label2", "", "$label4", "", "$label3"}
	var inbox, archive []string
	for i := 0; i < n/2; i++ {
		m := demoMsg(i+1000, "X-Mozilla-Status: "+statuses[i%len(statuses)]+"\nX-Mozilla-Keys: "+keys[i%len(keys)],
			"Thunderbird local folder message with <angle brackets> & ampersands.\n")
		if i%3 == 0 {
			inbox = append(inbox, m)
		} else {
			archive = append(archive, m)
		}
	}
	testutil.WriteMbox(t, filepath.Join(dir, "Thunderbird", "Inbox"), inbox...)
	testutil.WriteMbox(t, filepath.Join(dir, "Thunderbird", "Archives.sbd", "2021"), archive...)

	testutil.WriteOLM(t, filepath.Join(dir, "outlook-backup.olm"), testutil.OLMMessageEntries(
		"Accounts/me@example.com/com.microsoft.__Messages/Inbox/msg1.olk15Message",
		testutil.OLMMsg{Subject: "Outlook budget review", MessageID: "<o1@example.org>",
			From: testutil.OLMAddr{Name: "Frank Example", Email: "frank@example.org"},
			To:   []testutil.OLMAddr{{Name: "Me", Email: "me@example.com"}}, Body: "Budget numbers inside",
			HTML: `<p>Budget numbers <b>inside</b>.</p><p><img src="cid:chart@example.org" alt="chart"></p>`,
			Sent: "2024-03-08T10:00:00", Unread: true, Flagged: true, Categories: []string{"Finance", "Red category"},
			Priority: "1",
			Attachments: []testutil.OLMAtt{
				{Name: "budget.csv", Type: "text/csv", Data: []byte("a,b\n1,2\n")},
				{Name: "chart.png", Type: "image/png", CID: "chart@example.org", Data: demoPNG},
			}},
		"Accounts/me@example.com/com.microsoft.__Attachments")...)
}

func demoHTML() string {
	b64 := base64.StdEncoding.EncodeToString(demoPNG)
	return "From: Newsletter <news@example.net>\nTo: Me <me@example.com>\nSubject: Spring sale newsletter\n" +
		"Date: Wed, 10 Apr 2024 08:00:00 +0000\nMessage-ID: <html1@example.net>\nX-Gmail-Labels: Category Promotions\n" +
		"MIME-Version: 1.0\nContent-Type: multipart/mixed; boundary=\"OUTER\"\n\n" +
		"--OUTER\nContent-Type: multipart/related; boundary=\"REL\"\n\n" +
		"--REL\nContent-Type: text/html; charset=utf-8\n\n" +
		"<html><body style=\"background:#f4f4f4\"><h1>Spring sale</h1><p onclick=\"x()\">Everything must go.</p>" +
		"<script>alert('no')</script><img src=\"http://tracker.example/pixel.gif\" width=1 height=1>" +
		"<p><img src=\"cid:logo@example.net\" alt=\"logo\"></p><p><a href=\"https://shop.example/\">Visit the shop</a></p></body></html>\n" +
		"--REL\nContent-Type: image/png\nContent-ID: <logo@example.net>\nContent-Transfer-Encoding: base64\n" +
		"Content-Disposition: inline; filename=\"logo.png\"\n\n" + b64 + "\n--REL--\n" +
		"--OUTER\nContent-Type: application/pdf\nContent-Disposition: attachment; filename=\"catalogue.pdf\"\n" +
		"Content-Transfer-Encoding: base64\n\n" + base64.StdEncoding.EncodeToString([]byte("%PDF-1.4 fake")) + "\n" +
		"--OUTER\nContent-Type: image/png\nContent-Disposition: attachment; filename=\"coupon.png\"\n" +
		"Content-Transfer-Encoding: base64\n\n" + b64 + "\n--OUTER--\n"
}

func demoServer(t testing.TB, dir string, background bool) (*server.Server, func()) {
	d, err := source.Discover(dir, source.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	var srcs []source.Source
	for _, c := range d.Candidates {
		src, err := c.Open()
		if err != nil {
			t.Fatalf("open %s: %v", c.Rel, err)
		}
		srcs = append(srcs, src)
	}
	st, err := index.Open(filepath.Join(t.TempDir(), "index.db"), index.Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	run := func() {
		defer close(done)
		if err := st.IndexSources(ctx, srcs, nil); err != nil && ctx.Err() == nil {
			t.Errorf("index: %v", err)
		}
	}
	if background {
		go run()
	} else {
		run()
	}
	srv, err := server.New(server.Config{Store: st, Sources: srcs, Root: dir, UI: FS(), Version: "demo",
		Logger: log.New(io.Discard, "", 0)})
	if err != nil {
		t.Fatal(err)
	}
	return srv, func() {
		cancel()
		<-done
		st.Close()
		for _, s := range srcs {
			s.Close()
		}
	}
}

func get(t *testing.T, h http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.Host = "127.0.0.1:8080"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestUIAgainstRealServer serves the embedded UI and the API together over
// a real index, as the binary does, and makes the requests the UI makes.
func TestUIAgainstRealServer(t *testing.T) {
	dir := t.TempDir()
	writeDemoCorpus(t, dir, 14)
	srv, stop := demoServer(t, dir, false)
	defer stop()

	rec := get(t, srv, "/")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `id="message-list"`) {
		t.Fatalf("GET /: %d", rec.Code)
	}
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") {
		t.Errorf("UI CSP: %q", csp)
	}
	for _, p := range []string{"/app.css", "/js/app.js", "/js/theme-init.js", "/icon.svg"} {
		if rec := get(t, srv, p); rec.Code != 200 {
			t.Errorf("GET %s: %d", p, rec.Code)
		}
	}
	var facets struct {
		Sources []struct {
			ID   int64  `json:"id"`
			Kind string `json:"kind"`
		} `json:"sources"`
		Folders    []json.RawMessage       `json:"folders"`
		Labels     []struct{ Name string } `json:"labels"`
		Categories []struct{ Name string } `json:"categories"`
		Flags      []struct {
			Name  string `json:"name"`
			Count int    `json:"count"`
		} `json:"flags"`
	}
	for _, p := range []string{"/api/facets", "/api/facets?deleted=include", "/api/facets?deleted=hide"} {
		rec := get(t, srv, p)
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &facets) != nil {
			t.Fatalf("GET %s: %d %s", p, rec.Code, rec.Body)
		}
	}
	kinds := map[string]bool{}
	for _, s := range facets.Sources {
		kinds[s.Kind] = true
	}
	if !kinds["mbox"] || !kinds["olm"] || len(facets.Labels) == 0 || len(facets.Categories) == 0 || len(facets.Flags) != 9 {
		t.Fatalf("facets: %+v", facets)
	}
	var page struct {
		Items []struct {
			ID       int64 `json:"id"`
			ThreadID int64 `json:"threadId"`
		} `json:"items"`
		Total      int64  `json:"total"`
		NextCursor string `json:"nextCursor"`
	}
	for _, p := range []string{
		"/api/messages?limit=100&sort=date_desc",
		"/api/messages?limit=100&q=label%3AWork+is%3Aunread&sort=relevance&deleted=include",
		"/api/messages?limit=100&deleted=only",
		fmt.Sprintf("/api/messages?limit=100&source=%d&sort=from", facets.Sources[0].ID),
		"/api/messages?limit=100&q=offsite&sort=relevance",
	} {
		rec := get(t, srv, p)
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &page) != nil {
			t.Fatalf("GET %s: %d %s", p, rec.Code, rec.Body)
		}
	}
	if len(page.Items) == 0 {
		t.Fatal("search for offsite found nothing")
	}
	id := page.Items[0].ID
	for _, p := range []string{fmt.Sprintf("/api/messages/%d", id), fmt.Sprintf("/api/threads/%d?deleted=include", id),
		fmt.Sprintf("/api/messages/%d/raw", id), "/api/stats", "/api/status"} {
		if rec := get(t, srv, p); rec.Code != 200 {
			t.Errorf("GET %s: %d %s", p, rec.Code, rec.Body)
		}
	}
}

// TestServeDemo serves the UI over a synthetic archive for manual and
// browser checks. It only runs when MEV_DEMO_ADDR is set, for example:
//
//	MEV_DEMO_ADDR=127.0.0.1:8099 go test ./internal/web -run TestServeDemo -timeout 0
//
// MEV_DEMO_SIZE scales the corpus (default 120); MEV_DEMO_SLOW=1 indexes
// in the background so the progress banner shows.
func TestServeDemo(t *testing.T) {
	addr := os.Getenv("MEV_DEMO_ADDR")
	if addr == "" {
		t.Skip("set MEV_DEMO_ADDR to serve the demo UI")
	}
	n := 120
	fmt.Sscanf(os.Getenv("MEV_DEMO_SIZE"), "%d", &n)
	dir := t.TempDir()
	writeDemoCorpus(t, dir, n)
	srv, stop := demoServer(t, dir, os.Getenv("MEV_DEMO_SLOW") == "1")
	defer stop()
	l, err := server.Listen(addr, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("serving the demo UI on http://%s/", l.Addr())
	if err := srv.HTTPServer().Serve(l); err != nil {
		t.Fatal(err)
	}
}
