// Package mail is the single RFC 5322 / MIME implementation of
// mail-export-viewer: header parsing, RFC 2047 encoded words, RFC 2231
// parameters, charset conversion, tolerant address and date parsing, the
// MIME tree walk, body selection, html-to-text, attachments, and a writer
// that synthesises .eml files.
//
// # Model
//
//   - Envelope: the header data every message has (ids, subject, people,
//     date).
//   - Summary: Envelope plus what the index stores and searches (plain text
//     capped at MaxSummaryText, attachment names, HasAttachment). Formats
//     that do not hold raw messages (PST, OLM) fill it directly.
//   - Message: Envelope plus the ordered Header, Text, HTML and Attachments
//     (metadata only; bytes come from AttachmentData or the source).
//
// Parse(raw, fallbackDate) builds a Message; Message.Summary and Summarize
// derive the Summary. ParseHeader parses only the header block (cheap
// metadata such as X-Gmail-Labels or X-Mozilla-Status). Header values keep
// RFC 2047 words encoded (Field.Value) so that list headers can be split
// before decoding (ParseLabels); Field.Decoded and Header.Decoded decode
// them.
//
// # Guarantees
//
// Nothing here panics or fails on bad input (entry points recover and
// degrade); every returned string is valid UTF-8; bodies have LF line
// endings. Attachment indices are assigned in one shared tree walk, so Parse,
// AttachmentData, AttachmentsData and CIDData always agree.
//
// Limits on hostile input: multipart nesting 20 levels, 10000 MIME entities
// per message, header field values 1 MiB, a nameless message/rfc822 part is
// scanned 8 KiB deep for its Subject, Summary.Text 256 KiB.
//
// # Decisions
//
// Further behaviour: the header field cap
// (linear-time folding), host-independent obsolete zone names and
// "GMT+0530" dates, weekday-less "Mar 5 10:00:00 2024" dates,
// NameGenerated, signature parts not counting as attachments, rejecting
// boundaries holding CR/LF, and its extra html block tags.
//
//   - Charsets: unknown or uninformative labels fall back to "UTF-8 if
//     valid, else windows-1252". Text declared UTF-8 that has no valid
//     multi-byte sequence is read as windows-1252 (mislabelled Latin-1)
//     rather than replaced with U+FFFD as before; UTF-8 declared as
//     Latin-1 is kept as UTF-8. Raw 8-bit header bytes use the message's
//     declared body charset first.
//   - Attachment.Inline follows the disposition (inline, or none with a
//     Content-ID). CountsAsAttachment (used for HasAttachment) excludes
//     signatures/delivery reports and Content-ID inline parts, so a PDF sent
//     "inline" without Content-ID still counts.
//   - A text/plain or text/html part is a body unless it has a filename or
//     an attachment disposition. Text is only text/plain; PlainText falls
//     back to TextFromHTML(HTML).
//   - Generated names: "attachment-<index><ext>" or "<inner subject>.eml";
//     Summary.AttachmentNames leaves generated names out.
//   - Base64 skips every non-alphabet byte; quoted-printable keeps invalid
//     escapes literally; unknown transfer encodings pass through.
//   - Broken multiparts (no boundary, boundary never found) are shown as
//     text; a missing close delimiter ends the last part at the end of data.
//   - Dates outside 1970-2200 are treated as missing.
//   - WriteEML writes multipart/mixed (not related) with Content-IDs kept,
//     which every reader resolves; message/rfc822 parts are not
//     transfer-encoded.
package mail
