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
//	< <(a=c)> (80=name)(81=text) >   dictionary; (a=c) sets atom scope
//	                                 (c: column names, other: values)
//	{1:^80 {(k:...)} [..] [..]}      table (id:scope); meta table skipped
//	[1:^80 (^81=lit)(^81^82)]        row (id:scope); cell col=literal or
//	                                 col^valueAtom; "-(^81)" deletes a cell
//	[-1:^80]                         deletes the row
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
// messageId, storeToken, msgOffset or flags. Lookup tries, in order: the
// byte offset (storeToken, decimal, or msgOffset, hex), the Message-ID (only
// when unique in the folder), then the statusKey read as the decimal message
// key (the row id). A hit whose own Message-ID contradicts the one asked for
// is rejected, so a summary that is stale after compaction never attaches
// metadata to the wrong message.
//
// Columns (numbers are hex in Mork except storeToken and junkscore):
//
//	flags      Thunderbird flags: not Read -> Unread, Replied -> Answered,
//	           Marked -> Starred, Expunged/ImapDeleted -> Deleted, Forwarded
//	priority   4,5 -> Important; shown as Prop "priority" (lowest..highest,
//	           normal and none are omitted)
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
// in Inputs. A missing, unreadable, oversized, non-Mork or message-less .msf
// makes the provider return nil: the mbox keeps its header-derived metadata.
// The Sidecar contract has no warning channel, so the provider is silent.
package msf
