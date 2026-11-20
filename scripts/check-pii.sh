#!/usr/bin/env bash
# check-pii.sh [--history] [repo-dir]: fail if the repository would publish
# private data.
# Default: scans tracked and staged files (index content) and commit authors
# for home paths, real-looking email addresses, secrets, oversized files, and
# mail/database/binary files outside internal/**/testdata.
# Only tracked or staged content is scanned: untracked and ignored files are
# invisible to this check.
# --history: scans instead every commit reachable from any ref (git log -p
# --all: added and removed lines of text files, commit messages, author and
# committer identities) with the same home-path, secret and email patterns,
# so data that was committed and later removed is still caught.
# Portable to macOS (bash 3.2, BSD grep/tr).
set -u
export LC_ALL=C
history=0
if [ "${1:-}" = "--history" ]; then history=1; shift; fi
repo="${1:-.}"
cd "$repo" || exit 2
fails=0
bad() { echo "check-pii: $*" >&2; fails=$((fails+1)); }

max_bytes=$((5*1024*1024))
home_re='(/home/[A-Za-z0-9_][A-Za-z0-9._-]*|/Users/[A-Za-z0-9_][A-Za-z0-9._-]*)'
email_re='[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}'
secret_re='(-----BEGIN ([A-Z0-9]+ )*PRIVATE KEY-----|gh[pousr]_[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{30,}|AKIA[0-9A-Z]{16}|xox[baprs]-[A-Za-z0-9-]{10,}|sk-[A-Za-z0-9]{32,})'

# Fine: noreply@<any>, <id>+<name>@users.noreply.github.com, example.com/org/net.
email_ok() {
  local a local_part domain
  a="$(printf '%s' "$1" | tr 'A-Z' 'a-z')"
  local_part="${a%@*}"
  domain="${a#*@}"
  [ "$local_part" = noreply ] && return 0
  [ "$domain" = users.noreply.github.com ] && return 0
  printf '%s\n' "$domain" | grep -Eq '(^|\.)example\.(com|org|net)$'
}

# scan_text name: reports home paths, secrets and emails in stdin.
scan_text() {
  local content m addr
  content="$(tr -d '\0' | grep -a '' || true)"
  [ -z "$content" ] && return
  while IFS= read -r m; do
    [ -n "$m" ] && bad "$1: home-directory path ($m)"
  done < <(printf '%s\n' "$content" | grep -aEo "$home_re" | sort -u)
  if m=$(printf '%s\n' "$content" | grep -aEo "$secret_re" | head -1) && [ -n "$m" ]; then
    bad "$1: private key or token pattern"
  fi
  while IFS= read -r addr; do
    [ -z "$addr" ] && continue
    email_ok "$addr" || bad "$1: email address not on example.* or noreply ($addr)"
  done < <(printf '%s\n' "$content" | grep -aEo "$email_re" | sort -u)
}

if [ $history -eq 1 ]; then
  if git rev-parse --verify -q HEAD >/dev/null; then
    # Binary files show as "Binary files ... differ" (their content is
    # covered by the default scan's binary and testdata rules).
    # The diff markers (+, -, space) are dropped so they do not join the
    # text that follows them.
    scan_text "history" < <(git log --all -p --no-color --no-ext-diff \
      --format='commit %H%nauthor %an <%ae>%ncommitter %cn <%ce>%n%B' | sed -E 's/^[-+ ]//')
  fi
  if [ $fails -gt 0 ]; then echo "check-pii: $fails problem(s) in the history" >&2; exit 1; fi
  echo "check-pii: history ok"
  exit 0
fi

while IFS= read -r -d '' f; do
  size=$(git cat-file -s ":$f" 2>/dev/null || echo 0)
  if [ "$size" -gt "$max_bytes" ]; then bad "$f: larger than 5 MB ($size bytes)"; fi

  case "$f" in internal/*/testdata/*|internal/testdata/*) allow=1;; *) allow=0;; esac
  if [ $allow -eq 0 ]; then
    case "$f" in
      *.db|*.sqlite|*.sqlite3|*.pst|*.ost|*.olm|*.mbox|*.msf|*.exe|*.so|*.dylib|*.zip)
        bad "$f: mail/database/binary file outside internal/**/testdata";;
    esac
  fi

  # A NUL byte in the first 8 KB marks a binary file. Text is scanned bytewise
  # (C locale, -a) so latin-1 and other non-UTF-8 text is not skipped.
  if [ $allow -eq 0 ]; then
    total=$(git show ":$f" 2>/dev/null | head -c 8000 | wc -c)
    nonul=$(git show ":$f" 2>/dev/null | head -c 8000 | tr -d '\0' | wc -c)
    if [ "$total" -ne "$nonul" ]; then
      bad "$f: binary file outside internal/**/testdata"
      continue
    fi
  fi
  scan_text "$f" < <(git show ":$f" 2>/dev/null)
done < <(git ls-files -z)

# Commit authors and committers.
if git rev-parse --verify -q HEAD >/dev/null; then
  while IFS= read -r addr; do
    [ -z "$addr" ] && continue
    email_ok "$addr" || bad "commit identity not noreply/example ($addr)"
  done < <(git log --format='%ae%n%ce' | sort -u)
fi

if [ $fails -gt 0 ]; then echo "check-pii: $fails problem(s)" >&2; exit 1; fi
echo "check-pii: ok"
