// Package pst reads Outlook PST files ([MS-PST]) in pure Go and registers
// the "pst" source format. It is written from the specification; no other
// PST implementation was used.
//
// # Layers
//
//	ndb.go        header, NBT/BBT B-trees, blocks, XBLOCK/XXBLOCK data
//	              trees, SLBLOCK/SIBLOCK subnode trees, permute/cyclic
//	              decoding (internal/pstcrypt)
//	ltp.go        heap-on-node, BTH, property contexts, table contexts
//	messaging.go  message store, folders, messages, recipients, attachments
//	rtf.go        compressed RTF (LZFu) and RTF to HTML/text
//	source.go     the source.Source implementation
//
// The test-only writer in pstgen produces synthetic Unicode PSTs for end
// to end tests. TestReal* validate the reader against third-party samples
// when PST_SAMPLES names a directory of .pst files (never committed).
//
// # Supported files
//
// Unicode (wVer 21-23) and ANSI (wVer 14/15) PSTs, unencoded or with
// compressible (permute) or cyclic encoding. 4K-page OSTs (wVer 36) and
// high-encryption files are refused by Open. CRCs and signatures are not
// verified (page trailers' type and BID are); allocation maps and the
// deleted-items list are ignored, so deleted data is never recovered.
//
// # Mapping
//
// Folders are the normal folders below the IPM subtree (the store's
// PidTagIpmSubTreeEntryId; without it, the root folder's "Top of ..."
// child, else the root), walked depth-first in hierarchy table order.
// Search folders are skipped. Entry.Folder is Candidate.Rel without its
// extension ("pst" if nothing is left) followed by the display names, with
// '/' replaced by '_' and empty names shown as "(unnamed)"; messages
// directly in the IPM subtree get the bare prefix. Sibling folders with the
// same name merge in the tree.
//
// Messages are listed in contents table order; Locator.ID is the message
// NID. Classes IPM.Note*, IPM.Post*, REPORT.*, IPM.Schedule.Meeting*,
// IPM.(Outlook.)Recall* and empty/"IPM" are mail; everything else
// (contacts, appointments, tasks, notes, ...) is counted and reported in
// one warning. Unreadable messages are counted and reported in one warning
// with the first error.
//
//   - Subject: PR_SUBJECT without its prefix marker (U+0001 and a length).
//   - From: PR_SENT_REPRESENTING_*, else PR_SENDER_*; To/Cc/Bcc: the
//     recipient table. Emails come from the SMTP address properties, or the
//     email address when it holds an '@' and the address type is SMTP;
//     Exchange (X.500) addresses without an SMTP form keep only the name.
//     Transport headers (PR_TRANSPORT_MESSAGE_HEADERS) become
//     Message.Header and fill what the properties lack (From/To/Cc/Bcc
//     without emails, Message-ID, In-Reply-To, References, Date).
//   - Date: PR_CLIENT_SUBMIT_TIME, else PR_MESSAGE_DELIVERY_TIME, else the
//     Date header, else PR_CREATION_TIME; times outside 1970-2200 are
//     missing.
//   - Bodies: PR_BODY as Text and PR_HTML as HTML (binary HTML is decoded
//     with PR_INTERNET_CPID, else the message code page). Only when both
//     are empty is PR_RTF_COMPRESSED used: RTF made from HTML (\fromhtml1)
//     yields the de-encapsulated HTML ([MS-OXRTFEX]; \*\mhtmltag groups
//     are dropped), other RTF its plain text. PtypString8 values use
//     PR_MESSAGE_CODEPAGE (else PR_INTERNET_CPID), falling back to "UTF-8
//     if valid, else windows-1252". Scan reads bounded prefixes (1 MiB of
//     text, 4 MiB of HTML or compressed RTF); Load reads everything.
//   - Flags: Unread when PR_MESSAGE_FLAGS lacks MSGFLAG_READ (absent flags
//     mean read), Draft for MSGFLAG_UNSENT, Starred for PR_FLAG_STATUS 2,
//     Important for PR_IMPORTANCE 2, Answered/Forwarded from
//     PR_LAST_VERB_EXECUTED (102/103, 104).
//   - Labels: the categories in the named property "Keywords"
//     (PS_PUBLIC_STRINGS), resolved through the name-to-ID map; trimmed,
//     without empties and duplicates.
//   - Props: "Importance: low", "Follow-up: complete", and the message class
//     when it is not IPM.Note.
//   - Attachments, in attachment table order (unreadable ones are left out
//     everywhere, so indices stay stable): name from the long filename,
//     short filename or display name; type from PR_ATTACH_MIME_TAG (else
//     application/octet-stream); Inline when PR_ATTACH_CONTENT_ID is set.
//     Embedded messages (method 5) are message/rfc822, named "<name>.eml"
//     (the inner subject, NameGenerated, when unnamed), sized by
//     PR_ATTACH_SIZE, and synthesised with mail.WriteEML (nested up to 4
//     levels). Other unnamed attachments are "attachment-<i><ext>".
//
// Raw synthesises the message with mail.WriteEML. Load and Raw refuse
// messages whose bodies plus attachment table sizes exceed
// source.MaxMessageBytes; Attachment checks the size of that attachment.
// Load, Raw and Attachment return ErrStale for locators that are not a
// message node of the file.
//
// # Limits on hostile input
//
// Every offset and size is bounds-checked against the file and its
// structure. B-trees are at most 16 levels with strictly decreasing levels;
// data trees have the XBLOCK/XXBLOCK shape; a heap is at most 64 MiB, a
// table row matrix 128 MiB and 1M rows, a subnode tree 1M entries, a BTH 8
// levels and 1M records, a folder tree 64 levels and 100000 folders, RTF
// group nesting 1000 and its output 64 MiB. Each message is read under
// recover, so a corrupt one fails alone.
//
// # Concurrency
//
// The file is read with ReadAt only; the B-tree page cache (4096 pages) is
// guarded by a mutex and the store is opened once, so Load, Raw and
// Attachment are safe alongside each other and a running Scan.
package pst
