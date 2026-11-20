package testutil

import (
	"os"
	"path/filepath"
	"testing"
)

// MixedDirMessages is the number of messages in MixedDir.
const MixedDirMessages = 6

// MixedDir writes a synthetic directory with Gmail-style and Thunderbird
// mbox files (one nested in .sbd), a loose .eml and a synthetic OLM
// archive: MixedDirMessages messages in total.
func MixedDir(t testing.TB) string {
	t.Helper()
	dir := t.TempDir()
	WriteMbox(t, filepath.Join(dir, "Takeout", "All mail.mbox"),
		"From: Alice <alice@example.com>\nTo: Bob <bob@example.org>\nSubject: Gmail hello\n"+
			"Date: Mon, 6 Mar 2023 10:00:00 +0000\nMessage-ID: <g1@example.com>\nX-Gmail-Labels: Inbox,Travel\n\nPacking list for the trip.\n",
		"From: Bob <bob@example.org>\nTo: Alice <alice@example.com>\nSubject: Re: Gmail hello\n"+
			"Date: Mon, 6 Mar 2023 11:00:00 +0000\nMessage-ID: <g2@example.org>\nIn-Reply-To: <g1@example.com>\n\nSounds good.\n")
	WriteMbox(t, filepath.Join(dir, "Local Folders", "Inbox"),
		"From: Carol <carol@example.net>\nTo: Dan <dan@example.net>\nSubject: Thunderbird note\n"+
			"Date: Tue, 7 Mar 2023 09:00:00 +0000\nMessage-ID: <t1@example.net>\n\nBody text.\n")
	WriteMbox(t, filepath.Join(dir, "Local Folders", "Inbox.sbd", "Archive"),
		"From: Carol <carol@example.net>\nTo: Dan <dan@example.net>\nSubject: Archived note\n"+
			"Date: Wed, 1 Feb 2023 09:00:00 +0000\nMessage-ID: <t2@example.net>\n\nOld note.\n")
	emlDir := filepath.Join(dir, "loose")
	if err := os.MkdirAll(emlDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(emlDir, "a.eml"), []byte("From: Erin <erin@example.com>\nTo: Dan <dan@example.net>\n"+
		"Subject: Loose eml\nDate: Thu, 2 Mar 2023 08:00:00 +0000\nMessage-ID: <e1@example.com>\n\nA loose message.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := OLMMsg{Subject: "Outlook budget", MessageID: "<o1@example.org>",
		From: OLMAddr{Name: "Frank", Email: "frank@example.org"},
		To:   []OLMAddr{{Name: "Dan", Email: "dan@example.net"}}, Body: "Budget numbers inside",
		Sent: "2023-03-08T10:00:00", Categories: []string{"Finance"}}
	WriteOLM(t, filepath.Join(dir, "backup.olm"), OLMMessageEntries(
		"Accounts/me@example.com/com.microsoft.__Messages/Inbox/msg1.olk15Message", m,
		"Accounts/me@example.com/com.microsoft.__Attachments")...)
	return dir
}
