// Package olm reads Outlook for Mac archives (.olm: a zip of XML files)
// as a source.Source (Kind "olm"). It registers itself in init; cmd links
// it in with a blank import.
//
// # Status: layouts are assumptions
//
// No real Outlook export was available when this was written. The layouts
// and element names below come from public descriptions of OLM files and
// from the synthetic archives in internal/testutil (olmgen.go); they are
// UNVERIFIED against real exports. The reader is tolerant by design, but
// every item under "Unverified" should be checked against a real archive.
//
// # Assumed layouts
//
//	Local/Messages/<folders...>/message_NNNNN.xml                          Outlook 2011/2016
//	Accounts/<acct>/com.microsoft.__Messages/<folders...>/*.olk15Message   new Outlook
//
// A file is a message document when its extension is .xml or .olk15Message
// and some directory above it is named "Messages" or
// "com.microsoft.__Messages" (case-insensitive); the directories between
// that one and the file are the folder path. Directory entries of the zip
// below such a directory make empty folders (FolderLister). Everything
// else is ignored: attachments (com.microsoft.__Attachments,
// Local/Attachments), Categories.xml, folder tree files, signatures,
// settings. Folder tree and Categories files are not parsed: folders come
// from paths, and categories from each message.
//
// Entry.Folder is "<file stem>/<folders>" (Candidate.Rel minus its
// extension, "backup.olm" + Inbox -> "backup/Inbox"); a message directly
// below the Messages directory gets just the stem. When the archive has
// several accounts (Accounts/<acct>/...) the account name is inserted
// after the stem so that equal folder names of different accounts do not
// merge. Segments are cleaned: empty, "." and ".." are dropped, invalid
// UTF-8 is replaced.
//
// # Message document
//
// The first element (depth first) that has a child whose name starts with
// "OPFMessage" is the message; documents without one (contacts, events)
// are non-mail. Element names are matched case-insensitively with the
// OPFMessage/Copy/Get decoration stripped, so OPFMessageCopySubject,
// opfmessagegetsubject and Subject all match. Recognised:
//
//	subject, messageid, inreplyto, references
//	fromaddresses (else senderaddress), toaddresses, ccaddresses, bccaddresses
//	    children carry OPFContactEmailAddressAddress / ...Name attributes
//	    (or child elements with such suffixes); a text-only element is
//	    parsed as an address list; displayto is the fallback for To
//	body, htmlbody, hashtml: a body that looks like HTML (and no HTML
//	    element) is taken as HTML
//	senttime (Date), receivedtime (Date when no sent time, else a Prop);
//	    formats RFC 3339, "2006-01-02T15:04:05" (no zone is UTC), RFC 1123
//	isread (false -> Unread), isflagged/flagstatus (-> Starred),
//	priority/importance (high, 1 or 2 -> Important), isdraft, replied,
//	forwarded, isdeleted, isjunk
//	any element with "categor" in its name: names from attributes
//	    containing "name", leaf texts (split at , or ;) -> Entry.Labels
//	any element with "attachment" in its name and children: each child is
//	    an attachment with OPFAttachmentName, ...ContentType, ...ContentID,
//	    ...URL, ...ContentExtension (attributes or child elements)
//
// Remaining childless, attribute-less elements with text up to 4 KiB
// become Entry.Props (original names, document order). Unknown elements
// are ignored.
//
// Attachments: the URL names a zip entry (matched case-insensitively after
// dropping empty and "." segments; also tried relative to the message's
// directory and percent-unescaped; URLs with ".." never resolve). Size is
// the entry's uncompressed size. An attachment whose entry is missing is
// still listed with Size 0 and reads as empty. Inline is set when a
// Content-ID is present. Attachment indices are the document order.
//
// Messages are synthesised: Load builds a mail.Message (no transport
// headers; Header is empty), Raw uses mail.WriteEML. Loc.Name is the zip
// entry name; it is stale (ErrStale) when the entry is gone or no longer
// holds a message. Each Load re-reads and re-parses one entry, so memory
// is bounded by one document; nothing is ever extracted to disk and entry
// names never become file paths.
//
// # Limits (hostile archives)
//
// More than 5,000,000 zip entries: Open fails. A message document over
// MaxMessageBytes (also counted while decompressing, not just by the
// declared size) is listed by Scan as "(message too large to read)" and
// Load, Raw and Attachment return ErrTooLarge; attachments over
// MaxMessageBytes likewise. Scan stops with a warning after 64 GiB of
// message XML. XML documents are limited to 200,000 elements and depth 64;
// invalid control characters are blanked, non-UTF-8 encodings converted.
// A broken document is a warning, the scan goes on; panics are recovered
// per item.
//
// Non-mail documents (contacts, calendar, tasks, notes, recognised by
// .olk15Contact/Event/Task/Note extension, by a directory name containing
// contact/calendar/event/task/note, or by lacking message fields inside a
// Messages directory) are counted and reported in one warning at the end
// of Scan.
//
// # Unverified
//
//   - the layouts above, and whether Outlook 2011 stores one message per
//     file as message_*.xml under Local/Messages or elsewhere
//   - element and attribute names, in particular categories, flag status,
//     priority values (1/2 = high is a guess), reply/forward markers
//   - the date format and its time zone (assumed UTC when none is given)
//   - how attachment URLs are spelled (assumed archive-relative)
//   - whether bodies with hashtml=1 sit in the body or htmlbody element
//   - folder tree files are ignored, so a folder with neither messages nor
//     a zip directory entry is not listed, and folder display names are
//     the directory names
package olm
