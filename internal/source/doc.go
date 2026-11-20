// Package source defines what every input format provides and how inputs
// are found: the Entry/Source contract, content sniffing, a format
// registry, and Discover.
//
// # Layering
//
//	internal/mail            parsing (no knowledge of formats)
//	internal/source          this contract; imports only mail
//	internal/source/<fmt>    mbox, msf, olm, pst: implement Source, Register in init
//	internal/index, server   consume Source/Entry; never import format packages
//	cmd                      blank-imports the format packages, then Discover + Open
//
// Wiring in cmd:
//
//	import _ ".../internal/source/mbox" // and olm, pst
//	d, err := source.Discover(path, source.Limits{})   // err: unusable root or unknown single file
//	for _, c := range d.Candidates {
//		src, err := c.Open()   // per-candidate failure: report, continue
//		...
//	}
//
// # Registering a format
//
//	func init() {
//		source.Register(source.Format{Name: source.OLM, Sniff: source.IsOLM, Open: open})
//	}
//
// Name is one of the kind constants (Mbox, PST, OLM, EML). Sniff decides by
// content only; use the Is* helpers so that detection rules live here and
// stay mutually exclusive. An eml reader registers with PerDir: true and
// gets one Candidate per directory (Path = directory, Files = its .eml
// files). Open must not write anything anywhere and should only validate
// (e.g. read a PST header); a failing Open affects only that candidate.
//
// # Discover and Sniff
//
// Detection is by content, never by extension:
//
//	pst   "!BDN" magic (PST and OST)
//	olm   a zip with an entry under "Local/" or "Accounts/"
//	mork  "// <!-- <mdb:mork" (Thunderbird .msf; a sidecar, never a Candidate)
//	mbox  first non-blank line (after an optional BOM) starts with "From "
//	eml   only header lines before the first blank line, at least one of
//	      From/Sender/Date/Message-ID/Received/Return-Path/MIME-Version/...
//
// A single file must match a registered format (else ErrUnknownFormat); a
// ".msf" file stands for its mbox (the same path without ".msf"). A
// directory walk follows symlinks only when their target resolves under the
// root (a link leaving the root is skipped with a warning; the root path
// itself may be a symlink), never enters a directory twice (the first path
// visited wins), reports each real file once, skips hidden entries, Mork
// sidecars, special files (never opened, so FIFOs cannot block) and
// unclaimed files, and stops at Limits (default depth 32, 1,000,000
// entries) with a warning. Candidate.Rel is the raw '/'-separated path
// below the root: "" for the root directory itself, the base name for a
// single file. It is made valid UTF-8 by replacing each invalid byte with
// U+FFFD (as encoding/json shows it), so two names differing only in
// invalid bytes ("caf\xe9" and "caf\xe8") get the same Rel and their
// folders merge in the tree; their Paths, and so their Sources, stay
// distinct.
//
// # Implementing Source
//
// Scan streams Entries in a stable order. Per-item and per-folder problems
// (corrupt message, unreadable folder, skipped non-mail items: count them
// and warn once with a summary) go to warn and scanning continues; recover
// from panics per item. Scan returns an error only if the whole input is
// unreadable, ctx is done, or emit failed (return emit's error as is).
// Scan is never called concurrently with itself, but Load, Raw and
// Attachment must be safe for concurrent use with each other and with a
// running Scan (the server reads while the index scans). There is no
// progress hook: consumers measure progress in bytes from Entry.Loc
// offsets for mbox (Offset+Length against the file size) and in entry
// counts otherwise.
//
// Load, Raw and Attachment refuse messages larger than MaxMessageBytes
// (256 MiB, raw or synthesised size) with an error wrapping ErrTooLarge,
// checking the size before reading; Scan still lists them.
//
// Entry fields:
//
//   - Folder: full path in the one merged folder tree, '/'-separated, never
//     empty, no empty segments. Formats derive it from Candidate.Rel and
//     their own tree: mbox drops known extensions (.mbox, .mbx, .mbs) and
//     turns Thunderbird "X.sbd/Y" into "X/Y"; folders inside a PST/OLM are
//     prefixed with Rel minus its extension ("backup.pst" + "Inbox" ->
//     "backup/Inbox"); eml uses Rel, or the base name of Path when Rel is
//     "". The index groups by Source as well, so equal Folders of
//     different sources stay distinguishable.
//   - Labels: Gmail labels for mbox (X-Gmail-Labels, via mail.ParseLabels)
//     or Outlook categories for olm/pst; the index and UI tell them apart
//     by Source.Kind() (a "Labels" section for mbox, "Categories" for
//     olm/pst). Tags: Thunderbird tag display names. No duplicates.
//   - Flags: the zero value is read and unflagged. Map Gmail system labels
//     (Unread, Starred, Important), X-Mozilla-Status bits, PST/OLM
//     read/flag/importance properties. HasAttachment is set by Resolve.
//   - Props: everything format-specific not folded into the above, in
//     order (e.g. unmapped X- headers, OLM elements, PST properties).
//   - Loc: see Locator. The index stores Loc.String() and parses it back
//     with UnmarshalText.
//   - Msg or Raw+Date: PST and OLM build *mail.Summary themselves. mbox and
//     eml set Raw (the message, or a bounded prefix of a huge one: headers
//     and the first MiBs of body suffice) and Date (the From_ line date)
//     and leave Msg nil; the index calls Entry.Resolve in parallel workers.
//     With a prefix, attachments starting after it are not seen, so
//     HasAttachment and AttachmentNames are best-effort for huge messages.
//
// Locator per format: mbox {Offset, Length} of the message including its
// From_ line; eml {Name} the file name inside Candidate.Path; olm {Name} the
// zip entry; pst {ID} the message node id. Load, Raw and Attachment
// validate what they can (From_ line at Offset, entry/node still present)
// and return an error wrapping ErrStale otherwise; they never serve a
// different message.
//
// Raw-backed formats implement Load as mail.Parse(raw, fallbackDate) and
// Attachment as mail.AttachmentData(raw, i). Other formats build the
// mail.Message themselves (Envelope; Header from transport headers via
// mail.ParseHeader when present; Text/HTML; Attachments with Index equal to
// their position) and implement Raw with mail.WriteEML, passing their
// attachment reader. An attachment index out of range is an error wrapping
// mail.ErrNotFound.
//
// Inputs lists every file the source depends on (including sidecars such
// as a Thunderbird .msf or prefs.js), so that the index cache, keyed by
// path+size+mtime of each, notices changes. The index also compares the
// set of input paths with the cached one, so an eml directory (PerDir) is
// re-scanned when files are added or removed, not only when one changes.
//
// The server serves cid: images by FindCID-ing the reference in
// Load(loc).Attachments and then calling Attachment(loc, a.Index), which
// works the same for raw-backed and synthesising formats.
//
// # Optional interfaces
//
// FolderLister: sources with their own folder tree (PST, OLM) list every
// folder, including empty ones, so they appear in the UI. The index calls
// it after Scan and adds the folders to those seen in Entries.
//
// TagStyler: sources that know tag colours (mbox with a Thunderbird
// sidecar: the sidecar's TagInfo values, one per display name, coloured
// ones preferred) return them; the index calls it after Scan and keeps the
// colours with that source, so they go away when the source changes or
// disappears.
//
// # Sidecars (Thunderbird .msf)
//
// The mbox and msf packages never import each other. The msf package calls
// RegisterSidecarProvider from init with a function that, given an mbox
// path, opens the sidecar "<mbox>.msf" (and finds prefs.js for tag names
// and colours). It returns (nil, nil) when there is no sidecar, (nil, err)
// when it is unusable (corrupt, oversized, unreadable: the mbox must not
// fail, it keeps header-derived metadata and reports err as a warning), and
// (sc, err) with both non-nil when usable but degraded (report err as a
// warning, use sc). The mbox source calls SidecarFor(path) once per mbox
// file in Open or Scan, then for every message calls Lookup(Message-ID,
// From_ line offset) and, when ok, replaces its header-derived flags by
// SidecarMeta.Flags (keeping HasAttachment) and appends Tags and
// Props (dropping duplicates). It adds Sidecar.Inputs() to its own Inputs.
// SidecarMeta.Tags are display names already. TagInfo maps tag keys (as
// written in X-Mozilla-Keys and the .msf "keywords" cell) to display name
// and colour, so the mbox source also names keywords found in headers of
// messages without a sidecar row. Colours reach the index through
// TagStyler.
package source
