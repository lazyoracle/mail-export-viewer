// Package mbox implements the mbox and loose-.eml formats of package source
// and registers both from init ("mbox", and "eml" with PerDir).
//
// # Format notes
//
//   - Separators are "From " lines: a strict one (weekday, h:m:s, year) always
//     counts; a looser date-shaped one only after a blank line (or at the
//     start of the file); lines over 1024 bytes never do. Content-Length is
//     ignored (mboxo, mboxrd, mboxcl all split on separators). LF and CRLF,
//     a leading UTF-8 BOM and leading blank lines are accepted.
//   - Message bytes are unquoted on read: one '>' is removed from every line
//     matching ^>+From (a bare "From " in mboxo is quoted the same way). The
//     one blank line that precedes the next separator is not part of the
//     message. Locator{Offset, Length} covers the From_ line and that blank
//     line, exactly.
//   - Scan keeps at most scanPrefix (4 MiB) of each message in Entry.Raw
//     while streaming, so memory is bounded by that and one 64 KiB buffer; a
//     bigger message is still listed, from its prefix. Load, Raw and
//     Attachment read the whole range, refusing above source.MaxMessageBytes
//     (ErrTooLarge, before reading).
//   - Load, Raw and Attachment check that the locator still delimits a
//     message: a separator line at Offset, and the range ending at the end
//     of the file or right before another separator line; otherwise ErrStale.
//     A short read is ErrStale too. Residual risk (accepted, see
//     docs/design.md): the Locator carries no identity of the message, so
//     if the file is rewritten while the viewer runs and a separator lands
//     at the same offset with the next one at the same end, the message
//     now in that range is served in place of the indexed one (wrong
//     content, not an error) until the next start re-indexes the file,
//     whose size/mtime fingerprint changed. Only
//     regular files are opened (stat before open, then fstat), so a FIFO
//     cannot block.
//     Files are opened per call, never held, so thousands of mbox files can
//     be open as Sources at once.
//
// # Metadata
//
//   - Gmail X-Gmail-Labels (mail.ParseLabels) become Labels; Unread, Starred,
//     Important, Spam (Junk) and Drafts/Draft (Draft) (case-insensitive)
//     also set flags and stay as labels. Trash sets no flag: it is an
//     ordinary label, shown by default like Outlook's Deleted Items and
//     Thunderbird's Trash folders (only data the client itself marked
//     deleted but kept is hidden as Deleted).
//   - X-Mozilla-Status: no read bit is Unread; 0x2 Answered; 0x4 Starred; 0x8
//     (expunged, not yet compacted) Deleted; 0x1000 Forwarded. X-Mozilla-Status2
//     0x00200000 (IMAP deleted) is Deleted; X-Mozilla-Draft-Info marks a Draft.
//     Deleted messages are listed with the flag (the UI hides them). A message
//     without these headers counts as read.
//   - X-Mozilla-Keys become Tags: names come from the sidecar's TagInfo, else
//     Thunderbird's defaults for $label1..5 (Important, Work, Personal, To Do,
//     Later), else the key itself. $Forwarded and Junk/$Junk set flags;
//     NonJunk, $NotJunk, $MDNSent and other "$" keywords are no tags.
//   - Every other X- header is a Prop (decoded; at most 64 per message, 2 KiB
//     each); the consumed ones above are not repeated.
//
// # Sidecar
//
// Open asks source.SidecarFor(path) once. For each message Scan calls
// Lookup(Message-ID, From_ offset); a hit replaces the header-derived flags
// except Draft, which the .msf cannot hold (HasAttachment is left to
// Entry.Resolve) and appends Tags, Labels and
// Props without duplicates. Inputs includes the sidecar's inputs. A provider
// result of (nil, nil) means no sidecar; (nil, err) keeps header metadata
// only; (sc, err) uses sc; in the last two cases every Scan reports err
// through warn first.
//
// # Folders
//
// Folder is derived from Candidate.Rel: Thunderbird "X.sbd" directories
// become "X" ("Archive.sbd/2019" is "Archive/2019"), and a .mbox/.mbx/.mbs
// extension of the file name is dropped; Apple Mail's "Name.mbox/mbox" is
// "Name", and its nested sub-mailboxes "A.mbox/B.mbox/mbox" are "A/B".
// Loose .eml files are the messages of their directory's folder (Rel, or its base name when Rel is empty); a
// Locator names the file within the directory, and only files the Candidate
// listed can be loaded. Their fallback date is the file's modification time.
package mbox
