package olm

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lazyoracle/mail-export-viewer/internal/source"
	"github.com/lazyoracle/mail-export-viewer/internal/source/sourcetest"
	"github.com/lazyoracle/mail-export-viewer/internal/testutil"
)

func fuzzOpen(t *testing.T, data []byte) {
	path := filepath.Join(t.TempDir(), "backup.olm")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	src, err := source.Candidate{Format: source.OLM, Path: path, Rel: "backup.olm"}.Open()
	if err != nil {
		return
	}
	defer src.Close()
	sourcetest.Within(t, 10*time.Second, func() {
		if _, panics := sourcetest.Exercise(src, 50); len(panics) > 0 {
			t.Errorf("recovered panics: %q", panics)
		}
	})
}

// FuzzArchive feeds arbitrary bytes as an .olm file (seeded with valid
// archives).
func FuzzArchive(f *testing.F) {
	b, err := testutil.OLMBytes(fixture()...)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(b)
	f.Add([]byte("PK\x03\x04"))
	f.Fuzz(fuzzOpen)
}

// FuzzMessageXML puts arbitrary bytes as the message document (and as an
// attachment) of an otherwise valid archive in both layouts.
func FuzzMessageXML(f *testing.F) {
	for _, e := range fixture() {
		if filepath.Ext(e.Name) != ".png" && filepath.Ext(e.Name) != ".csv" {
			f.Add(e.Data)
		}
	}
	f.Add([]byte(`<?xml version="1.0" encoding="windows-1252"?><emails><email><OPFMessageCopySubject>caf` + "\xe9" + `</OPFMessageCopySubject></email></emails>`))
	f.Add([]byte("\xff\xfe<\x00e\x00m\x00a\x00i\x00l\x00>\x00"))
	f.Fuzz(func(t *testing.T, doc []byte) {
		b, err := testutil.OLMBytes(
			testutil.OLMEntry{Name: "Accounts/me@example.com/com.microsoft.__Messages/Inbox/m.olk15Message", Data: doc},
			testutil.OLMEntry{Name: "Local/Messages/Inbox/message_00001.xml", Data: doc},
			testutil.OLMEntry{Name: "Accounts/me@example.com/com.microsoft.__Attachments/0-logo.png", Data: doc},
		)
		if err != nil {
			t.Fatal(err)
		}
		fuzzOpen(t, b)
	})
}
