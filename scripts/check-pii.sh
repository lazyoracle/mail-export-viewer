#!/usr/bin/env bash
# check-pii.sh [repo-dir]: fail if the repository would publish private data.
# Scans tracked and staged files (index content) and commit authors for home
# paths, real-looking email addresses, secrets, oversized files, and
# mail/database/binary files outside internal/**/testdata.
# Only tracked or staged content is scanned: untracked and ignored files are
# invisible to this check. Portable to macOS (bash 3.2, BSD grep/tr).
set -u
export LC_ALL=C
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
  content="$(git show ":$f" 2>/dev/null | tr -d '\0' | grep -a '' || true)"
  [ -z "$content" ] && continue
  if m=$(printf '%s\n' "$content" | grep -aEo "$home_re" | head -1) && [ -n "$m" ]; then
    bad "$f: home-directory path ($m)"
  fi
  if m=$(printf '%s\n' "$content" | grep -aEo "$secret_re" | head -1) && [ -n "$m" ]; then
    bad "$f: private key or token pattern"
  fi
  while IFS= read -r addr; do
    [ -z "$addr" ] && continue
    email_ok "$addr" || bad "$f: email address not on example.* or noreply ($addr)"
  done < <(printf '%s\n' "$content" | grep -aEo "$email_re" | sort -u)
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
