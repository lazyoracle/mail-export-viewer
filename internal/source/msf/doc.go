// Package msf reads Thunderbird ".msf" summary files (Mork) and offers them
// to the mbox source as a source.Sidecar. It registers a provider with
// source.RegisterSidecarProvider from init; it does not import the mbox
// package and registers no source.Format (Mork is a sidecar, never a
// Candidate).
//
// # Direct .msf input
//
// source.Discover already resolves a ".msf" given as input to its sibling
// mbox (same path without the extension), so the user gets that mbox with
// this sidecar applied; no mechanism is needed here.
//
// # Mork notes (parser, mork.go)
//
// A Mork file is a "// <!-- <mdb:mork:z v="1.4"/> -->" header followed by:
//
//	< <(a=c)> (80=name) >            dictionary; (a=c) selects the column
//	<(80=text)>                      scope; a plain dictionary is values
//	{1:^80 {(k^BE:c)} [..] [..]}    table (id:scope); meta table skipped;
//	                                 table-level "-[id]" row removal is not
//	                                 interpreted (rows stay)
//	[1:^80 (^81=lit)(^81^82)]        row (id:scope); cell col=literal or
//	                                 col^valueAtom; "-(^81)" deletes a cell
//	[-1:^80 (^81=x)]                 cuts all cells of the row, then applies
//	                                 the cells that follow
//	@$${5{@ ... @$$}5}@              transaction group; "@$$}~~}@" aborts
//
// Text values decode "$XX" (hex byte), "\c" (literal c) and "\<newline>"
// (continuation); values that are not valid UTF-8 are read as Latin-1. A row
// is keyed by (scope, id) and later definitions merge into it; scope
// defaults to the enclosing table's. Atom ids resolve against the column
// dictionary for column positions and the value dictionary for values, each
// falling back to the other. Aborted groups and a trailing uncommitted group
// (an interrupted write) are rolled back.
//
// The parser never fails on garbage: unknown syntax is skipped and counted
// in File.Problems. Limits: 256 MiB input, 4M rows, 4M atoms, 1024 cells per
// row, 1 MiB per value, counted nesting 256 (recursion is never used).
//
// # Sidecar semantics (sidecar.go)
//
// Message rows are those outside the dbfolderinfo and thread scopes holding
// message-id, storeToken, msgOffset or flags. Lookup(msgID, offset) matches
// the From_ byte offset against a row's storeToken (decimal) or msgOffset
// (hex); in old folders whose rows have neither, against the row id. Failing
// that it matches a Message-ID that is unique in the folder. Every hit other
// than an explicit offset match must also match the Message-ID, and an
// explicit hit whose own Message-ID contradicts the one asked for is
// rejected, so a summary that is stale after compaction never attaches
// metadata to the wrong message. (X-Mozilla-Status is a flag word, not a key,
// so it plays no part.)
//
// Columns (numbers are hex in Mork except storeToken and junkscore):
//
//	flags      Thunderbird flags: not Read -> Unread, Replied -> Answered,
//	           Marked -> Starred, Expunged/ImapDeleted -> Deleted, Forwarded
//	priority   nsMsgPriority (0 unset, 1 none, 2 lowest, 3 low, 4 normal,
//	           5 high, 6 highest); 5 and 6 -> Important; Prop "priority"
//	           (lowest, low, high, highest)
//	junkscore  100 -> Junk; always kept as Prop "junkscore"
//	keywords   space separated tag keys -> display names; label 1..5 adds
//	           "$labelN". Bookkeeping keywords (junk, nonjunk, $forwarded,
//	           ...) are dropped; "$forwarded" also sets Forwarded
//
// HasAttachment is left to Entry.Resolve. Unknown tag keys are shown as is.
//
// Tag names and colours come from "prefs.js" in the nearest ancestor
// directory of the mbox (the profile root, at most 16 levels up):
// user_pref("mailnews.tags.<key>.tag"/".color", "...") over the defaults
// $label1..5 = Important, Work, Personal, To Do, Later. prefs.js is listed
// in Inputs. The nearest ancestor holding a Mail/ or ImapMail/ child wins over
// a nearer stray prefs.js.
//
// Provider results: (sidecar, nil) for a .msf with messages; (sidecar, err)
// when usable but the parser skipped garbage or rolled back an interrupted
// write (a warning). Without a usable .msf (none, one without messages, or
// an unreadable, non-regular, oversized (256 MiB) or non-Mork file, the
// last ones reported as err) the mbox keeps header-derived metadata, and
// when a prefs.js is found the result is still a tags-only sidecar that
// knows no message but names X-Mozilla-Keys keywords; with no prefs.js it
// is (nil, nil) or (nil, err). The .msf path is an input even when missing,
// so creating it later re-indexes the folder.
package msf
