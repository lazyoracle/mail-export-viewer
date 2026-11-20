package corpus

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/lazyoracle/mail-export-viewer/internal/source"
	"github.com/lazyoracle/mail-export-viewer/internal/source/pst/pstgen"
	"github.com/lazyoracle/mail-export-viewer/internal/testutil"
)

// Bulk search words with known frequencies.
const (
	BulkPhrase = "quarterly budget review" // in 1% of bulk bodies
	BulkWord   = "budget"                  // in 5% (incl. the phrase ones)
)

// bulk writes Options.Bulk messages: 8 Gmail-style mbox files (75%), 4
// Thunderbird-style ones (15%), one PST (5%) and one OLM (5%), plus a
// reply chain of Options.BulkThread messages.
func (g *gen) bulk() error {
	n := g.o.Bulk
	nPST, nOLM := n/20, n/20
	nTB := n * 15 / 100
	nGM := n - nPST - nOLM - nTB
	labels := g.o.BulkLabels
	if labels <= 0 {
		labels = 40
	}
	labelName := func(i int) string {
		if i%10 == 0 {
			return fmt.Sprintf("Projekt %d Ω", i)
		}
		return fmt.Sprintf("Label %d", i)
	}
	i := 0
	body := func(tok string) (string, string) {
		extra := ""
		switch {
		case i%100 == 0:
			extra = " " + BulkPhrase
		case i%20 == 0:
			extra = " " + BulkWord
		}
		return g.words(40+g.r.IntN(120)) + extra + " " + tok + "\n", extra
	}
	// mbox files.
	writeMbox := func(rel, folder string, count int, gmail bool, pad int64) error {
		p := filepath.Join(g.root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		fh, err := os.Create(p)
		if err != nil {
			return err
		}
		w := bufio.NewWriterSize(fh, 1<<20)
		var written int64
		for k := 0; k < count; k++ {
			i++
			from, to := g.person(), g.person()
			d := g.date()
			tok := g.token()
			id := fmt.Sprintf("bulk%d@example.com", i)
			subject := fmt.Sprintf("%s %s %d", strings.Title(g.vocab[g.r.IntN(200)]), g.vocab[g.r.IntN(len(g.vocab))], i)
			h := envelope(from, []person{to}, subject, d, id)
			var flags source.Flags
			var ls, tags, atts []string
			if gmail {
				ls = []string{labelName(g.r.IntN(labels)), labelName(g.r.IntN(labels))}
				if ls[0] == ls[1] {
					ls = ls[:1]
				}
				if i%5 == 0 {
					ls = append(ls, "Unread")
					flags |= source.Unread
				}
				if i%20 == 1 {
					ls = append(ls, "Starred")
					flags |= source.Starred
				}
				h = append(h, [2]string{"X-Gmail-Labels", labelHeader(ls)})
			} else {
				st := 1
				if i%5 == 0 {
					st = 0
					flags |= source.Unread
				}
				keys := ""
				if i%7 == 0 {
					keys = "$label1"
					tags = []string{"Important"}
				}
				h = append(h, [2]string{"X-Mozilla-Status", fmt.Sprintf("%04x", st)}, [2]string{"X-Mozilla-Keys", keys})
			}
			b, _ := body(tok)
			if pad > 0 && written < pad {
				b += strings.Repeat(g.words(12)+"\n", 400)
			}
			m := &msg{headers: h, body: b}
			if i%10 == 3 {
				name := "doc" + strconv.Itoa(i) + ".pdf"
				m.parts = []mpart{textPart(b), attPart(name, "application/pdf", blob(2000+g.r.IntN(20000), uint64(i)))}
				atts = []string{name}
				flags |= source.HasAttachment
			}
			mf := &mboxFile{}
			mf.add("bulk@example.com", d, m.bytes())
			nb, err := w.Write(mf.b.Bytes())
			if err != nil {
				fh.Close()
				return err
			}
			written += int64(nb)
			g.add(Msg{Source: rel, Kind: source.Mbox, Folder: folder, Subject: subject, MessageID: id, From: from.Email, Date: d,
				Labels: ls, Tags: tags, Flags: flags, Atts: atts, Token: tok, Exact: true, Note: "bulk"})
		}
		if err := w.Flush(); err != nil {
			fh.Close()
			return err
		}
		g.source(rel, source.Mbox, false)
		return fh.Close()
	}
	for f := 0; f < 8; f++ {
		cnt := nGM / 8
		if f == 7 {
			cnt = nGM - 7*(nGM/8)
		}
		var pad int64
		if f == 0 {
			pad = g.o.BulkFileBytes
		}
		if err := writeMbox(fmt.Sprintf("bulk/takeout/box%d.mbox", f), fmt.Sprintf("bulk/takeout/box%d", f), cnt, true, pad); err != nil {
			return err
		}
	}
	for f := 0; f < 4; f++ {
		cnt := nTB / 4
		if f == 3 {
			cnt = nTB - 3*(nTB/4)
		}
		if err := writeMbox(fmt.Sprintf("bulk/tb/Mail/Local Folders/Archives.sbd/%d", 2015+f), fmt.Sprintf("bulk/tb/Mail/Local Folders/Archives/%d", 2015+f), cnt, false, 0); err != nil {
			return err
		}
	}
	// PST.
	if nPST > 0 {
		inbox := &pstgen.Folder{Name: "Inbox"}
		for k := 0; k < nPST; k++ {
			i++
			from, to := g.person(), g.person()
			d := g.date()
			tok := g.token()
			id := fmt.Sprintf("bulk%d@example.com", i)
			subject := fmt.Sprintf("Outlook %s %d", g.vocab[g.r.IntN(len(g.vocab))], i)
			b, _ := body(tok)
			m := pstMsg(subject, from, to, d, id, b)
			var flags source.Flags
			if i%5 == 0 {
				setProp(m, pstgen.I32(0x0E07, 0))
				flags |= source.Unread
			}
			inbox.Messages = append(inbox.Messages, m)
			g.add(Msg{Source: "bulk/outlook.pst", Kind: source.PST, Folder: "bulk/outlook/Inbox", Subject: subject, MessageID: id,
				From: from.Email, Date: d, Flags: flags, Token: tok, Exact: true, Note: "bulk pst"})
		}
		f := &pstgen.File{Crypt: pstgen.CryptPermute, StoreName: "Bulk", Folders: []*pstgen.Folder{inbox}}
		if err := g.write("bulk/outlook.pst", f.Bytes()); err != nil {
			return err
		}
		g.source("bulk/outlook.pst", source.PST, false)
	}
	// OLM.
	if nOLM > 0 {
		var entries []testutil.OLMEntry
		for k := 0; k < nOLM; k++ {
			i++
			from := g.person()
			d := g.date()
			tok := g.token()
			subject := fmt.Sprintf("Mac %s %d", g.vocab[g.r.IntN(len(g.vocab))], i)
			b, _ := body(tok)
			m := testutil.OLMMsg{Subject: subject, MessageID: fmt.Sprintf("<bulk%d@example.com>", i),
				From: testutil.OLMAddr{Name: from.Name, Email: from.Email}, Body: b, Sent: d.Format(olmTime), Categories: []string{"Bulk"}}
			entries = append(entries, testutil.OLMMessageEntries(fmt.Sprintf("Accounts/me@example.com/com.microsoft.__Messages/Inbox/m%d.olk15Message", k), m, "Accounts/me@example.com/com.microsoft.__Attachments")...)
			g.add(Msg{Source: "bulk/mac.olm", Kind: source.OLM, Folder: "bulk/mac/Inbox", Subject: subject, MessageID: fmt.Sprintf("bulk%d@example.com", i),
				From: from.Email, Date: d, Cats: []string{"Bulk"}, Token: tok, Exact: true, Note: "bulk olm"})
		}
		b, err := testutil.OLMBytes(entries...)
		if err != nil {
			return err
		}
		if err := g.write("bulk/mac.olm", b); err != nil {
			return err
		}
		g.source("bulk/mac.olm", source.OLM, false)
	}
	// Long reply chain.
	if g.o.BulkThread > 0 {
		f := &mboxFile{}
		var refs []string
		for k := 0; k < g.o.BulkThread; k++ {
			d := g.date()
			id := fmt.Sprintf("chain%05d@example.com", k)
			subject := "Endless thread"
			if k > 0 {
				subject = "Re: Endless thread"
			}
			from := people[k%len(people)]
			h := envelope(from, []person{people[(k+3)%len(people)]}, subject, d, id)
			if k > 0 {
				r := refs
				if len(r) > 20 {
					r = append([]string{refs[0]}, refs[len(refs)-19:]...)
				}
				h = append(h, [2]string{"In-Reply-To", "<" + refs[len(refs)-1] + ">"}, [2]string{"References", "<" + strings.Join(r, "> <") + ">"})
			}
			refs = append(refs, id)
			f.add("chain@example.com", d, (&msg{headers: h, body: g.words(10)}).bytes())
			g.add(Msg{Source: "bulk/chain.mbox", Kind: source.Mbox, Folder: "bulk/chain", Subject: subject, MessageID: id, From: from.Email,
				Date: d, Thread: "chain", Exact: true, Note: "bulk chain"})
		}
		if err := g.write("bulk/chain.mbox", f.b.Bytes()); err != nil {
			return err
		}
		g.source("bulk/chain.mbox", source.Mbox, false)
		if g.o.BulkThread > g.m.LongThreadLen {
			g.m.LongThread, g.m.LongThreadLen = "chain", g.o.BulkThread
		}
	}
	return nil
}
