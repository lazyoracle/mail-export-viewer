# mail-export-viewer

A read-only, offline viewer for exported mail. Point it at an export (a file
or a folder) and it opens a searchable mail client in your browser, served
only on `127.0.0.1`.

It reads:

- **mbox**: Gmail (Google Takeout), Thunderbird folders, Apple Mail
  exports, mutt and other mbox files;
- **Thunderbird `.msf` summaries** next to those mbox files, for flags and
  tags;
- **PST** files from Outlook for Windows;
- **OLM** archives from Outlook for Mac;
- **loose `.eml` files**.

A folder can mix all of these. Everything shows up in one folder tree and one
search index.

It is a single binary with the web UI built in. It makes no network
connections: no CDN, no web fonts, no remote images, no telemetry. It never
writes to your export. The only thing it writes is its index cache.

> **Status:** this is new software. It has been tested on large synthetic
> archives and on a few public sample PST files, but almost none of it has
> been checked against real exports yet. See
> [Status and verification](#status-and-verification) before you rely on
> it, and please report what breaks.

## Contents

- [Features](#features)
- [Install](#install)
- [Usage](#usage)
- [Finding and exporting your mail](#finding-and-exporting-your-mail)
- [What metadata is shown](#what-metadata-is-shown)
- [Search](#search)
- [Keyboard shortcuts](#keyboard-shortcuts)
- [Deleted messages](#deleted-messages)
- [Cache](#cache)
- [Security model](#security-model)
- [Status and verification](#status-and-verification)
- [Limitations](#limitations)
- [Troubleshooting](#troubleshooting)
- [Development](#development)
- [License](#license)

## Features

- **Formats are detected by content**, never by file extension. You can
  point it at a single file or at a folder; folders are searched
  recursively.
- **Metadata from every format**:
  - folders;
  - Gmail labels;
  - Outlook categories;
  - Thunderbird tags, with their names and colours;
  - flags (unread, starred, replied, forwarded, draft, deleted, junk,
    important, has attachment);
  - a Properties panel with format-specific fields that fit nowhere else.
- **Full-text search** over subject, people, body text and attachment
  names, with ranking, highlighted snippets and Gmail-style operators
  (`from:`, `label:`, `tag:`, `is:unread`, `before:` and more).
- **Conversation view**, across files and formats.
- **Reading messages**:
  - sanitised HTML, plain text, all headers, Properties and the raw
    message;
  - attachment downloads;
  - inline `cid:` images;
  - `.eml` download of any message, built on the fly for PST and OLM
    messages.
- **Statistics**: messages per year and month, top senders, largest
  folders.
- **Interface**: keyboard shortcuts, dark mode, deep links (in the URL hash),
  and a layout that adapts to narrow windows.
- **Large archives**:
  - files are read as streams;
  - message bodies are re-read from the export only when you open a
    message;
  - the SQLite/FTS5 index is cached on disk, so a restart takes
    milliseconds.
- **Copes with broken mail**: legacy charsets, broken headers and dates,
  damaged MIME, truncated files and corrupt PST nodes. Each problem is
  reported as a warning; one bad message does not stop the rest.

## Install

### Release binaries

Each GitHub release has four binaries and a `SHA256SUMS` file:

| file | platform |
|---|---|
| `mail-export-viewer-darwin-arm64` | macOS, Apple silicon |
| `mail-export-viewer-darwin-amd64` | macOS, Intel |
| `mail-export-viewer-linux-amd64` | Linux, x86-64 |
| `mail-export-viewer-linux-arm64` | Linux, ARM64 |

Download the binary for your machine and `SHA256SUMS` into the same
directory, then check and install it:

```sh
# macOS
shasum -a 256 -c SHA256SUMS --ignore-missing
# Linux
sha256sum -c SHA256SUMS --ignore-missing

chmod +x mail-export-viewer-darwin-arm64
sudo mv mail-export-viewer-darwin-arm64 /usr/local/bin/mail-export-viewer
```

Notes on the binaries:

- The Linux binaries are statically linked and need nothing else.
- The macOS binaries use only the system libraries.
- Windows is not supported. To read a PST from Windows, copy it to a Mac or
  a Linux machine.

**macOS quarantine.** The binaries are not signed with an Apple Developer ID
or notarised, so Gatekeeper blocks a downloaded copy. After checking the
checksum, remove the quarantine attribute:

```sh
xattr -d com.apple.quarantine /usr/local/bin/mail-export-viewer
```

You can also build it yourself (see below).

### Build from source

You need Go 1.24 or newer (`go.mod` says `go 1.24.0`). No C compiler, no
Node and no npm are needed: SQLite is pure Go (`modernc.org/sqlite`, built
with `CGO_ENABLED=0`), and the UI is plain JavaScript modules embedded with
`go:embed`.

```sh
git clone https://github.com/lazyoracle/mail-export-viewer
cd mail-export-viewer
make build                 # ./mail-export-viewer for this machine
./mail-export-viewer -version
make cross                 # dist/: the four release binaries and SHA256SUMS
```

## Usage

```sh
mail-export-viewer ~/Downloads/Takeout/Mail          # a folder (searched recursively)
mail-export-viewer "All mail Including Spam and Trash.mbox"
mail-export-viewer backup.pst
mail-export-viewer "Outlook for Mac Archive.olm"
mail-export-viewer ~/exports                         # any mix of the above
```

When it starts:

1. It prints the URL on stderr (default `http://127.0.0.1:8080/`, or the
   next free port).
2. It opens your browser.
3. It indexes in the background.

The UI works right away; a banner shows indexing progress. When indexing is
done, stderr gets a summary, for example
`indexed 1234 messages (mbox 1200, pst 34), 2 warnings, in 3.1s`.
Press Ctrl-C to stop. A second Ctrl-C forces it to quit.

### Flags

Flags may come before or after the path. Each can be written with one dash
or two.

| flag | default | meaning |
|---|---|---|
| `-addr host:port` | `127.0.0.1:8080` | Address to listen on. Without `-addr`, if the port is busy, the next 20 ports are tried. A non-loopback address is refused unless you also pass `-allow-remote`. |
| `-allow-remote` | off | Allow a non-loopback `-addr`. **Warning:** there is no authentication, so anyone who can reach the port can read all of your mail. |
| `-allowed-host name` | | An extra `Host` name or IP to accept. Requires `-allow-remote` (without it the command exits with a usage error). Give a bare name with no port. Repeatable. |
| `-cache-dir dir` | `<user cache dir>/mail-export-viewer` | Where to keep the index. |
| `-include-deleted` | off | Show messages flagged as deleted by default. The UI can still switch per view. |
| `-no-open` | off | Do not open a browser. |
| `-reindex` | off | Throw away this input's cached index and rebuild it. |
| `-version` | | Print the version and exit. |

Exit codes:

| code | meaning |
|---|---|
| 0 | normal exit |
| 1 | a runtime error, including a background indexing failure |
| 2 | a usage problem, or nothing usable in the input |

## Finding and exporting your mail

You can work on copies. The viewer only reads its input, but a mail client
that is running may rewrite its files while you read them. Close the client,
or copy the files somewhere else first.

### Thunderbird

Each Thunderbird folder is an mbox file with no extension (`Inbox`, `Sent`,
`Archives`), stored next to a `.msf` summary file. Subfolders live in a
`Name.sbd/` directory; a folder `Archives.sbd/2019` shows as
`Archives/2019`.

Point the viewer at one of these:

- the whole profile directory;
- its `Mail/` or `ImapMail/` directory;
- one account, such as `Mail/Local Folders`;
- a single folder file;
- a folder's `.msf` file (this opens the mbox next to it).

Profile locations (`<profile>` is a name such as `abcd1234.default-release`):

| system | path |
|---|---|
| macOS | `~/Library/Thunderbird/Profiles/<profile>/` |
| Linux | `~/.thunderbird/<profile>/` |
| Linux, Snap | `~/snap/thunderbird/common/.thunderbird/<profile>/` |
| Linux, Flatpak | `~/.var/app/org.mozilla.Thunderbird/.thunderbird/<profile>/` |
| Windows (copy it to a Mac or Linux machine) | `%APPDATA%\Thunderbird\Profiles\<profile>\` |

In Thunderbird, **Help → Troubleshooting Information → Profile Folder**
shows the exact path.

Inside the profile:

- **Local Folders**: `Mail/Local Folders/`.
- **POP accounts**: `Mail/<server>/`.
- **IMAP accounts**: `ImapMail/<server>/`. These hold only the messages
  Thunderbird downloaded for offline use. Messages it never downloaded are
  not in the files.

Inside a directory, names that start with a dot are skipped. So point the
viewer at the profile directory itself, not at the hidden directory above
it.

What comes from where:

- **The `.msf` summary** gives read, starred, replied, forwarded, deleted
  and junk flags, priority and tags. When the summary and the message
  headers disagree, the summary wins, because it is what Thunderbird shows.
  The summary is matched to messages by byte offset and Message-ID. A
  summary that is out of date (for example after the folder was compacted)
  is not applied to the wrong message.
- **Without a usable `.msf`**, flags come from the `X-Mozilla-Status`,
  `X-Mozilla-Status2` and `X-Mozilla-Keys` headers.
- **Tag names and colours** come from the profile's `prefs.js`
  (`mailnews.tags.<key>.tag` and `.color`). The viewer finds it by looking
  in the directories above the folder files, up to 16 levels.
  - If you copy only `Local Folders` somewhere else, `prefs.js` is not
    found. Tags then show Thunderbird's default names (Important, Work,
    Personal, To Do, Later) or their raw keys.
  - To get the names, copy `prefs.js` along, keeping the profile layout.

Thunderbird accounts that use the "one file per message" (maildir) storage
are not a supported layout. See [Limitations](#limitations).

### Gmail (Google Takeout)

1. At [takeout.google.com](https://takeout.google.com), deselect everything,
   select **Mail**, and create the export.
2. The archive contains `Takeout/Mail/All mail Including Spam and Trash.mbox`.
   Open that file, or the folder that contains it.

Gmail labels come from each message's `X-Gmail-Labels` header. They appear in
the **Labels** section of the sidebar and work with `label:`.

These system labels also set flags:

| label | flag |
|---|---|
| Unread | unread |
| Starred | starred |
| Important | important |
| Spam | junk |
| Drafts | draft |

The labels themselves are kept too. `Trash` is an ordinary label: messages
in Gmail's Trash are shown, like the Deleted Items folder of Outlook and the
Trash folder of Thunderbird. Use `-label:Trash` to leave them out.

### Outlook for Windows (PST)

In classic Outlook for Windows: **File → Open & Export → Import/Export →
Export to a file → Outlook Data File (.pst)**. Pick the top of the mailbox
and tick *Include subfolders*.

You may already have PST files:

- AutoArchive and **Archive** files are ordinary PSTs, often called
  `archive.pst`. They are usually in `Documents\Outlook Files\` or in
  `%LOCALAPPDATA%\Microsoft\Outlook\`. Outlook's
  **File → Account Settings → Data Files** lists the files it uses.
- To open an archive, copy the `.pst` file to the machine running the viewer
  and point the viewer at it.

What is supported:

- Unicode PSTs (Outlook 2003 and later) and the older ANSI format.
- Files with no encoding, "compressible" encoding (Outlook's default) or
  "high" encoding ([MS-PST] cyclic). "High" encoding has only been tested on
  synthetic files.
- Refused:
  - any other encryption method in the header, such as files protected by
    Windows Information Protection;
  - OST files with 4 KB pages, which current Outlook versions write.
- Password-protected PSTs have not been tested. The PST password is not a
  real encryption key, and the viewer does not ask for one.
- Deleted items that Outlook still keeps inside the file are not recovered.
  The *Deleted Items* folder is shown like any other folder.
- Only mail items are shown. Contacts, appointments, tasks and notes are
  counted and reported in one warning.

PST messages carry no raw message file. The viewer builds the message from
the PST's properties, and uses the stored transport headers when the PST has
them. **Download .eml** produces a synthesised `.eml`.

### Outlook for Mac (OLM)

In Outlook for Mac, use **File → Export** (or **Tools → Export**, depending
on the version), choose the mail items, and save the `.olm` archive. Open the
`.olm` file directly; nothing is extracted to disk.

The OLM layouts the viewer expects come from public descriptions of the
format and have **not** been checked against a real archive yet. The details
are under [Status and verification](#status-and-verification). Mail with
categories, flags and attachments should show. If it does not, please file
an issue as described there.

### Apple Mail

Apple Mail's own library (`~/Library/Mail/…`, stored as `.emlx` files) is
not supported. Export instead: select mailboxes, then **Mailbox → Export
Mailbox…**. This writes one `Name.mbox` directory per mailbox, each holding
an `mbox` file. Nested exports (`A.mbox/B.mbox/mbox`) show as `A/B`. The
nested layout is modelled from descriptions, not taken from a real export.

### Loose .eml files

A directory of `.eml` files is read as one folder, named after the
directory. A file counts as a message when it starts with mail headers, so
the `.eml` extension is not required. Files that are not mail are ignored.

### Mixing everything

Point the viewer at a directory holding any mix of the above. Each file is a
separate source in the sidebar, with a format badge. Folders inside a PST or
OLM appear under the file's name: `backup.pst` plus `Inbox` gives
`backup/Inbox`. Folders with the same path in different files also merge in
the tree, so you can browse `Inbox` across files.

The walk has some rules:

- it follows symlinks only when they point inside the folder you gave;
- it skips hidden files, special files and files it does not recognise;
- it stops at 32 directory levels or 1,000,000 entries, with a warning.

## What metadata is shown

| what | where it comes from | in the UI |
|---|---|---|
| Folders | directory and file names (mbox, eml), the Thunderbird `.sbd` tree, PST folders, OLM paths | sidebar tree per source, with total and unread counts |
| Labels | Gmail `X-Gmail-Labels` | sidebar **Labels**, pills on messages, `label:` |
| Categories | Outlook categories (PST *Keywords*, OLM category elements) | sidebar **Categories**, pills, `category:` (`label:` also matches them) |
| Tags | Thunderbird tags (`.msf` keywords, `X-Mozilla-Keys`), with names and colours from `prefs.js` | sidebar **Tags** with colour dots, pills, `tag:` |
| Flags | unread, starred, answered, forwarded, draft, deleted, junk, has attachment, important, from each format's flag fields | icons in the list, "Show only" filters with counts, `is:` |
| Properties | everything else of interest | the message's **Properties** tab |

Properties by format:

- **mbox**: the other `X-` headers.
- **Thunderbird**: priority and junk score.
- **PST**: importance, follow-up status, and the message class when it is
  not `IPM.Note`.
- **OLM**: the remaining simple elements, under their original names.

The **Headers** tab shows the transport headers when the message has them.
mbox and eml messages always do. PST messages do when Outlook stored them,
which is usual for mail received from the internet. OLM messages do not.

## Search

Type in the search box. Results update as you type, and a chip appears for
each operator, which you can remove with one click. Typing an operator
suggests its values (labels, tags, folders and so on). The help button next to the
box (**Search operators**) lists them.

How plain words match:

- Words must all match (AND).
- Matching ignores case and accents: `cafe` finds `Café`.
- Words are looked for in the subject, sender, recipients, body text and
  attachment names.
- While you type, the last word is matched as a prefix once it has 3 or more
  characters.

| syntax | meaning |
|---|---|
| `budget report` | both words, anywhere |
| `"quarterly budget"` | the exact phrase |
| `-draft` | without the word; `-` in front of any term, phrase or operator negates it |
| `from:alice` | sender name or address contains the word |
| `to:bob` | To or Cc |
| `cc:carol` | Cc only |
| `subject:invoice` | subject |
| `folder:Inbox` (alias `mailbox:`) | a folder path, or its last segment(s): `folder:inbox` matches `Inbox` and `backup/Inbox`, but not subfolders |
| `label:Work` | a Gmail label **or** an Outlook category (whole name) |
| `category:Blue` | an Outlook category only |
| `tag:Important` | a Thunderbird tag only |
| `filename:pdf` | part of an attachment's file name |
| `has:attachment` (also `has:attachments`, `has:att`) | has a real attachment; inline images don't count |
| `is:unread` / `is:read` (also `is:unseen`, `is:seen`) | read state |
| `is:starred` (also `is:flagged`) | starred or flagged |
| `is:answered` (also `is:replied`), `is:forwarded`, `is:draft`, `is:important` | the flag |
| `is:junk` (also `is:spam`) | junk |
| `is:deleted` (also `is:trash`) | messages flagged deleted (see [Deleted messages](#deleted-messages)) |
| `is:attachment` | same as `has:attachment` |
| `after:2019` `before:2020-07` `after:2019-03-15` | dates in UTC: `YYYY`, `YYYY-MM` or `YYYY-MM-DD` (`/` also allowed). `after:` includes the date, `before:` excludes it |

More rules:

- **Quoting values**: `label:"Project X"`, `from:"Alice Example"`. Inside
  quotes, `\"` is a literal quote and `\\` a literal backslash.
- **Exact names**: label, category and tag names match the whole name, and
  ignore case only for ASCII letters.
- **Negated operators** act on their own field only: `-from:bob` keeps
  messages that mention bob elsewhere.
- **Undated messages** never match `before:` or `after:`, but they do match
  `-before:` and `-after:`.
- **Mistakes are harmless**: unknown operators and malformed values (a bad
  date) are searched as plain text. Search syntax inside your text is never
  interpreted as SQLite FTS syntax.
- **Size limit**: a query is limited to 4 KB and 64 terms.
- **Chinese, Japanese and Korean**: text also matches as a substring of
  subject, sender and recipients. In the body, only whole runs of such text
  match.

Examples:

```
from:alice@example.com after:2019 before:2020 has:attachment
label:"Project X" -is:read
tag:Work subject:"status report"
folder:Archive/2019 invoice -filename:pdf
is:starred -label:Trash
```

Sorting (newest or oldest first, sender, subject, relevance), the deleted
mode and the conversation toggle are controls above the list. A click on a
folder, label, category or tag in the sidebar filters by it.

## Keyboard shortcuts

| key | action |
|---|---|
| `j` / `k` | next / previous message |
| `Enter` or `o` | open message |
| `u` | back to the list |
| `/` | search |
| `t` | conversation view on or off |
| `g` then `a` | go to All mail |
| `g` then `s` | go to Statistics |
| `[` / `]` | previous / next folder |
| `d` | switch theme |
| `?` | show shortcuts |
| `Esc` | close a dialog or the sidebar drawer |

Shortcuts do not fire while you type in a field, while Ctrl, Cmd or Alt is
held, or during IME composition.

## Deleted messages

Some exports still contain messages that were deleted but not yet purged.
The viewer marks these with the **deleted** flag:

- **Thunderbird**: messages marked deleted but not yet removed by compacting
  (`X-Mozilla-Status` 0x8, `X-Mozilla-Status2` IMAP-deleted, or the
  Expunged/IMAP-deleted bits in the `.msf`).
- **Gmail Takeout**: nothing gets this flag. `Trash` is an ordinary label
  (see [Gmail](#gmail-google-takeout)).
- **OLM**: messages whose deleted element is set.
- **PST**: nothing gets this flag. The Deleted Items folder is an ordinary
  folder, and data that Outlook deleted inside the file is never recovered.

These messages are **hidden by default**, as in the mail client. Hidden
messages are left out of lists, threads and sidebar counts, except the
Deleted filter's own count. Ways to see them:

- the menu above the list: **Hide deleted**, **Show deleted** or
  **Only deleted**;
- the **Deleted** filter under "Show only";
- an `is:deleted` search;
- `-include-deleted`, which makes **Show deleted** the default.

Nothing is removed from the index, so switching modes is instant.

## Cache

The index is one SQLite database per input, named after a hash of the
input's absolute path (with symlinks resolved):

- macOS: `~/Library/Caches/mail-export-viewer/index-<hash>.db`
- Linux: `$XDG_CACHE_HOME/mail-export-viewer/index-<hash>.db`, or
  `~/.cache/mail-export-viewer/index-<hash>.db`
- anywhere else with `-cache-dir`

Next to the database are its `-wal` and `-shm` files and two lock files,
`.lock` and `.run`. SQLite's temporary files go into the same directory,
not into `TMPDIR`.

- **Size.** The cache holds the indexed text, so it is roughly the size of
  the text in your mail, not counting attachments. In the validation runs it
  was 218 MiB for a 251 MiB mixed archive, and 1.17 GB for a 1.05 GiB mbox of
  long text messages.
- **Reuse.** On start, each file is checked by path, size and modification
  time, along with its `.msf` and `prefs.js` where they apply. Unchanged
  files are reused. Changed or new files are indexed again, and removed ones
  are dropped. The text of dropped or re-indexed messages is overwritten in
  the database (SQLite `secure_delete`, and FTS5 `secure-delete` for the
  search index), and the write-ahead log is written back and truncated
  after such a run, so it does not linger in the cache files.
- **Clearing.** Quit the viewer and delete the directory, or the one `.db`
  file with its `-wal`, `-shm`, `.lock` and `.run` files. Or run once with
  `-reindex`, which empties the index, vacuums it, and builds it again.
- **Corruption.** A corrupt cache, or one written by a different schema
  version, is detected and rebuilt.
- **Lock.** Only one viewer can use a given input's cache at a time. A
  second run on the same input with the same cache directory exits with
  "already running for this input", the first instance's process ID and its
  URL. The lock is released when the process exits, however it exits.
- **Permissions.** The cache directory is created with mode `0700`. The
  database, its `-wal`/`-shm` files and the lock files are created `0600`,
  because they contain your mail's text. All of them are opened without
  following symlinks; a symlink planted at one of those paths is refused.

## Security model

Your mail is untrusted input that is shown in a browser. The design keeps
both the mail and the viewer contained:

- **Loopback only.** The server listens on `127.0.0.1` and refuses any other
  address, both before binding and after. A `localhost` that resolves to
  something else is refused too. `-allow-remote` overrides this and prints a
  warning: there is no login.
- **Host check.** Requests must carry `Host: localhost`, `127.0.0.1` or
  `[::1]` (any port), or a name added with `-allowed-host`. Anything else
  gets 403. This blocks DNS-rebinding attacks from web pages you visit.
- **Locked-down HTTP.** Only GET and HEAD are accepted. The server sends no
  CORS headers, so other origins cannot read the API. Every response
  carries:
  - `X-Content-Type-Options: nosniff`
  - `Referrer-Policy: no-referrer`
  - `X-Frame-Options: DENY`
  - `Cross-Origin-Opener-Policy: same-origin`
  - `Cross-Origin-Resource-Policy: same-origin`
- **Content-Security-Policy.**
  - The UI page gets
    `default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; object-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'`.
  - API responses get `default-src 'none'; img-src 'self' data:; style-src 'unsafe-inline'`.
  - Raw messages, attachments and inline parts also get a `sandbox` policy,
    so even when opened directly in a tab they cannot run script.
- **Sanitised, sandboxed HTML.**
  - Message HTML goes through one allow-list sanitiser on the server. It
    removes scripts, event handlers, forms, frames, `<base>`, dangerous URLs
    and CSS that could fetch anything.
  - The result is shown in a `srcdoc` iframe sandboxed with only
    `allow-popups allow-popups-to-escape-sandbox`. With no scripts and no
    same-origin access, the frame has an opaque origin.
  - Links open in a new tab with `noopener noreferrer`.
  - The UI puts every other piece of text into the page as text, never as
    HTML.
- **Remote content is always blocked.** Remote images, stylesheets and
  tracking pixels are never fetched, and there is deliberately no "load
  remote content" button. A notice says when something was blocked.
  - Inline `cid:` images are shown. Small PNG, JPEG, GIF and WebP images are
    embedded as `data:` URIs. Larger ones load from per-message URLs that
    carry an unguessable token.
  - SVG is never shown inline.
- **Attachments are downloads.** They are always sent with
  `Content-Disposition: attachment` and a cleaned-up file name. Types
  outside an allow-list (HTML, SVG, scripts, unknown) are sent as
  `application/octet-stream`. Only raster images are previewed inline.
- **No network.** The binary makes no outbound connections. The UI is
  embedded and loads nothing from elsewhere. The only process it starts is
  the system's `open` or `xdg-open`, to show the URL (skipped with
  `-no-open`).
- **Read-only.** Input files are opened read-only. They are never modified,
  locked or extracted, and PST/OLM contents are never written to disk.
  - If a file changes while the viewer runs, its messages report that they
    are stale (HTTP 410) instead of showing the wrong bytes.
  - The end-to-end tests check that the input tree is byte-for-byte
    unchanged after a run, and that nothing is written outside the cache
    directory.
- **Private cache.** The cache is mode `0600` in a `0700` directory, as
  described under [Cache](#cache).
- **Hostile input.** Every reader caps sizes, nesting depth and counts. It
  recovers from a panic in one message and carries on, and it has been
  fuzzed. A message over 256 MiB is listed but not opened. File names,
  warnings and errors are cleaned of terminal control characters before
  they are printed.

See [SECURITY.md](SECURITY.md) for the threat model and how to report a
vulnerability.

## Status and verification

What has been done (details and numbers in
[docs/validation.md](docs/validation.md)):

- **Synthetic exports for every format.** A deterministic generator
  (`internal/testutil/corpus`, `scripts/gencorpus`) builds exports with a
  ground-truth record for every message: Gmail Takeout, a Thunderbird profile
  with `.msf` files and `prefs.js`, Apple Mail exports, `.eml` trees, OLM in
  both assumed layouts, and PST files written by a test-only PST writer.
- **Hostile input.** A "messy" set covers:
  - 15 legacy charsets and encoded-word edge cases;
  - broken headers and dates;
  - HTML bombs and a zip-bomb OLM;
  - path-traversal attachment names;
  - corrupt and truncated PSTs;
  - 0-byte files.
- **End-to-end tests** build the real binary and run it on each set and on
  a mixed directory. Over HTTP they check every count, flag, label, tag,
  operator, attachment, thread and security header against the ground
  truth, and check that the input is unchanged.
- **Public sample PSTs.** The PST reader was checked against three public
  third-party sample PST files (ANSI and Unicode, about 2,200 messages and
  170 attachments) with no failures. These files are not in the repository.
- **Fuzzing.** Native Go fuzzing covers the MIME parser, mbox framing and
  source, OLM archives and documents, PST files, Mork, the query parser,
  search and the HTML sanitiser, with no open findings.
- **Performance.** 105,000 messages (251 MiB) indexed in 47 s with a peak
  memory of 174 MiB, and a 1 GiB mbox in 57 s. Typical searches answer in
  2–100 ms. This was measured on an 8-vCPU Linux VM.
- **Checks.** The race detector, `go vet`, the JavaScript unit tests and a
  headless-Chromium UI check all pass.

**Not verified against real exports.** No real mailbox was used during
development. These parts are modelled from public documentation and may be
wrong:

- **Outlook for Mac OLM**: the archive layouts
  (`Local/Messages/…/message_*.xml` and
  `Accounts/<account>/com.microsoft.__Messages/…`), element names,
  categories, flag and priority values, date time zones, and how
  attachments are referenced.
- **PST features** that no sample file contained:
  - categories;
  - embedded-message and OLE attachments;
  - HTML recovered from RTF;
  - follow-up, importance, replied and forwarded flags;
  - cyclic encoding;
  - very large files using deeper B-tree and subnode structures;
  - Exchange-only addresses.
  See `internal/source/pst/doc.go`.
- **Thunderbird `.msf`** files written by real Thunderbird versions, and
  `prefs.js` tag settings.
- **Apple Mail** exports, especially nested mailboxes.
- **Gmail Takeout** labels that contain quotes or commas.
- **macOS**: it is built and tested in CI but has not been run by hand.
  Only headless Chromium has been used for the UI; no other browsers, and
  no screen readers.

**Please help.** If something is missing or wrong with a real export, open
an issue on GitHub. **Never attach real mail or real exports**, not even
"just one message". Useful, safe information:

- the viewer's version and the warnings it printed (they never contain
  message text, but check them for file names you'd rather not share);
- the export's structure with names replaced. For OLM, the output of
  `unzip -l archive.olm`, with folder and account names changed. For other
  formats, a directory listing;
- for an OLM or PST message that shows wrongly: the XML element names or
  property tags involved, with every value replaced by dummy text;
- best of all, a small synthetic file that shows the problem, made with
  `example.com` addresses and invented text.

## Limitations

- **Read-only by design.** No sending, replying, moving, flagging, deleting
  or IMAP. Changes in the UI are never written back.
- **No search inside attachment contents** (PDF, Office files and so on).
  Attachment file names are searchable.
- **Formats not supported**:
  - maildir, including Thunderbird's maildir storage;
  - Apple Mail's `.emlx` library;
  - Exchange or IMAP servers;
  - OST files with 4 KB pages, and PSTs protected by Windows Information
    Protection.
  Files that look like single messages may still be picked up as loose
  messages, but without their flags.
- **Only mail items.** Calendar items, contacts, tasks and notes in PST and
  OLM files are counted and skipped.
- **Encodings and attachments not decoded**: TNEF (`winmail.dat`) and
  uuencoded attachments are not unpacked, and S/MIME messages (`smime.p7m`)
  are not decrypted or unwrapped. ISO-2022-KR is not decoded.
- **Size limits**:
  - A single message over 256 MiB is listed but cannot be opened.
  - For mbox messages over 4 MiB, the index sees only the first 4 MiB, so
    attachment names after that are not searchable.
  - The detail view shows at most 8 MiB of text or HTML, and the plain-text
    tab the first 512 KB, with a link to download the rest.
  - The conversation view shows at most 5,000 messages.
- **Changed files are re-indexed whole.** A changed file is indexed again
  from scratch, with no incremental append. In the rare case that an mbox is
  rewritten so that the message boundaries land at exactly the same byte
  offsets, the change is not detected until restart.
- **Slower searches on very large archives.** `filename:` searches and
  Chinese/Japanese/Korean substring searches scan the whole index: about
  0.15–0.25 s at 100,000 messages, growing linearly. Indexing runs at about
  2,000 messages per second (a single SQLite writer), so a first run on
  1,000,000 messages takes several minutes. Later starts are instant.
- **UI limits**:
  - the list grows as you scroll instead of being virtualised;
  - thousands of labels are all drawn at once;
  - dates use two-digit years;
  - the "Conversations" toggle groups only the pages already loaded;
  - very large HTML bodies are not shortened like plain text.
- **ASCII-only case folding.** Case-insensitive matching of folder, label,
  category and tag names covers ASCII letters only.
- **Some Exchange senders** in PST files without SMTP addresses or transport
  headers show a name but no e-mail address.
- **Windows is not supported.**

## Troubleshooting

- **"no supported mail found in …"**: nothing in the path was recognised.
  Formats are detected by content:
  - mbox files start with a `From ` line;
  - PST files start with `!BDN`;
  - OLM files are zip archives with `Local/` or `Accounts/` entries;
  - `.eml` files start with mail headers.

  Check that you pointed at the export, not at a zip around it (unzip
  Takeout archives first), and that the files are not hidden (dot names).
- **"already running for this input"**: another viewer is serving this
  input from the same cache. Use the URL in the message, stop that process,
  or pass a different `-cache-dir`.
- **Port already in use**: without `-addr`, the next 20 ports are tried.
  Otherwise pick another port, for example `-addr 127.0.0.1:9000`.
- **The browser does not open**: use `-no-open` and open the printed URL
  yourself.
- **"Host not allowed" (403)**: you are reaching the server by a name other
  than `localhost` or `127.0.0.1`. With `-allow-remote`, add that name with
  `-allowed-host`.
- **A message says its file has changed (stale) or is gone**: the file was
  modified or removed after indexing. Restart the viewer, which re-indexes
  changed files, or use `-reindex`.
- **"message not found" after a restart**: the index was rebuilt and
  message ids changed, so old deep links no longer point anywhere. Search
  again.
- **Warnings in the banner or on stderr**: open **Details** in the indexing
  banner to see the warnings for each source. Corrupt messages, skipped
  calendar or contact items and unreadable folders are reported there, and
  the rest is still indexed. Files that look like a supported format but
  cannot be opened (for example a damaged PST header) are listed there
  under **Input problems**.
- **Thunderbird tags show as `$label1` or as raw keys**: `prefs.js` was not
  found above the folders. Point the viewer at the profile directory, or
  copy `prefs.js` along.
- **Garbled text in an old message**: the message declares no charset or a
  wrong one. The viewer falls back to UTF-8, then windows-1252. The
  **Headers** and **Raw** tabs show exactly what is in the file.
- **"cannot open the index"**: remove the cache file named in the message,
  or run with `-reindex`.
- **macOS says the binary "cannot be opened"**: see the quarantine note
  under [Install](#install).

## Development

The code is pure Go and uses no cgo. The UI is plain JavaScript modules with
no build step and no npm dependencies.

```
cmd/mail-export-viewer     CLI: flags, instance lock, browser, wiring
internal/mail              the one RFC 5322/MIME parser, charsets, html-to-text, .eml writer
internal/source            Source/Entry contract, content sniffing, format registry, Discover
internal/source/mbox       mbox and loose .eml, Gmail/Thunderbird header metadata
internal/source/msf        Thunderbird Mork (.msf) sidecar and prefs.js tags
internal/source/pst        PST reader (NDB/LTP/messaging, RTF), test-only writer in pstgen/
internal/source/olm        Outlook for Mac .olm reader
internal/index             SQLite/FTS5 index, query language, facets, threading
internal/server            JSON API (docs/api.md), sanitiser, security headers
internal/web               embedded UI, node unit tests (jstest/), browser checks (browsertest/)
internal/testutil          synthetic corpus generators (testutil/corpus)
internal/e2e               end-to-end tests against the built binary
scripts/                   gencorpus, check-pii
docs/                      design, plan, API, validation
```

Each package's `doc.go` records its format notes, decisions and limits.

### Make targets

| target | what it does |
|---|---|
| `make build` | `./mail-export-viewer` for this machine (`CGO_ENABLED=0`, `-trimpath`, version from `git describe`) |
| `make cross` | `dist/`: darwin/linux × amd64/arm64 static binaries and `SHA256SUMS` |
| `make test` | `go test ./...`, including the end-to-end tests (add `-short` by hand to skip them) |
| `make test-race` | `go test -race ./...` (needs cgo and a C compiler) |
| `make test-js` | UI unit tests with `node --test` (Node 22 is used in CI) |
| `make vet` | `go vet ./...` |
| `make check-pii` | fails if tracked files or commit identities contain home paths, real-looking e-mail addresses, secrets, mail/database/binary files or files over 5 MB |
| `make check-pii-history` | the same path, e-mail and secret patterns over every commit's diff and message on every ref |
| `make clean` | removes `dist/` and the binary |

`GO=/path/to/go make …` selects a Go toolchain.

### Tests

- **Unit and integration tests** sit next to the code. All fixtures are
  synthetic and generated in code; no real mail is ever committed.
- **End-to-end tests**: `go test ./internal/e2e` builds the binary (or uses
  `MEV_BIN`) and checks it against generated corpora.
- **Corpora by hand**:
  `go run ./scripts/gencorpus -out tests/tmp/x -sets gmail,pst,messy`.
  `tests/tmp/` is git-ignored.
- **Fuzzing**, one target at a time:
  `go test ./internal/source/pst -run XXX -fuzz FuzzSource -fuzztime 60s`.
  The other targets are listed in docs/validation.md.
- **Performance**:
  - `MEV_PERF=<corpus dir> go test ./internal/e2e -run TestPerf -v -timeout 1h`
    (generate the corpus with `gencorpus -sets bulk -bulk 100000`);
  - `MEV_PERF=1 go test ./internal/index -run Perf -v` for the index alone.
- **UI in a browser**:
  - `MEV_CHROME=<chrome-headless-shell> go test ./internal/web -run TestBrowser -v`
    runs the scripted checks;
  - `MEV_DEMO_ADDR=127.0.0.1:8099 go test ./internal/web -run TestServeDemo -timeout 0`
    serves a demo archive;
  - `internal/web/browsertest/scale.mjs` measures any running server.
- **Real PST samples**: `PST_SAMPLES=<dir of .pst files> go test ./internal/source/pst -run Real`.
  Keep such files outside the repository.

### Adding a format

1. Create `internal/source/<name>`.
2. Implement `source.Source`: `Kind`, `Path`, `Inputs`, a streaming `Scan`,
   and `Load`, `Raw` and `Attachment` that re-read one message by its
   `Locator`. Optionally add `FolderLister` and `TagStyler`.
3. Register it from `init` with
   `source.Register(source.Format{Name, Sniff, Open})`. Add a content
   sniffer `Is<Name>` in `internal/source/sniff.go` that cannot match any
   other format.
4. Blank-import the package in `cmd/mail-export-viewer/run.go`.
5. Run `sourcetest.Check` and `sourcetest.Exercise` against it, add a
   generator to `internal/testutil`, a set to the corpus, and a fuzz target.

The contract, including the rules for warnings, panics, stale locators and
size limits, is in `internal/source/doc.go`. The index, server and UI never
import format packages, so nothing else needs to change.

### CI and releases

`.github/workflows/ci.yml` runs on Linux and macOS:

- gofmt and vet;
- the tests with the race detector;
- the JavaScript tests;
- `check-pii`, and in a separate job `check-pii-history` over the full
  history;
- a cross-build of all four targets.

`.github/workflows/release.yml` runs on a `v*` tag:

1. It validates the tag and runs the tests.
2. It cross-builds with `CGO_ENABLED=0` and checks that the Linux binaries
   are static.
3. It writes `SHA256SUMS`.
4. A separate `publish` job creates the GitHub release. It is the only job
   with write permission.

All workflows use a read-only token by default, and checkouts do not persist
credentials. Third-party actions are referenced by major version tag, not
yet by commit SHA. See [SECURITY.md](SECURITY.md#supply-chain).

See [CONTRIBUTING.md](CONTRIBUTING.md) before sending changes.

## License

MIT, see [LICENSE](LICENSE). Third-party components compiled into the
binaries and their licenses are listed in
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
