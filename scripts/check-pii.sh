#!/usr/bin/env bash
# check-pii.sh [repo-dir]: fail if the repository would publish private data.
# Scans tracked and staged files (index content) and commit authors for home
# paths, real-looking email addresses, secrets, oversized files, and
# mail/database/binary files outside internal/**/testdata.
set -u
repo="${1:-.}"
cd "$repo" || exit 2
fails=0
bad() { echo "check-pii: $*" >&2; fails=$((fails+1)); }

max_bytes=$((5*1024*1024))
self_re='^scripts/check-pii(_test)?\.sh$'
home_re='(/home/[A-Za-z0-9_][A-Za-z0-9._-]*|/Users/[A-Za-z0-9_][A-Za-z0-9._-]*)'
email_re='[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}'
secret_re='(-----BEGIN ([A-Z0-9]+ )*PRIVATE KEY-----|gh[pousr]_[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{30,}|AKIA[0-9A-Z]{16}|xox[baprs]-[A-Za-z0-9-]{10,}|sk-[A-Za-z0-9]{32,})'

# An address is fine if it is noreply or on an example domain.
email_ok() {
  local a="${1,,}" domain
  domain="${a#*@}"
  case "$a" in *noreply*) return 0;; esac
  [[ "$domain" =~ (^|\.)example\.[a-z]+$ ]]
}

while IFS= read -r -d '' f; do
  [[ "$f" =~ $self_re ]] && continue
  size=$(git cat-file -s ":$f" 2>/dev/null || echo 0)
  if [ "$size" -gt "$max_bytes" ]; then bad "$f: larger than 5 MB ($size bytes)"; fi

  case "$f" in internal/*/testdata/*|internal/testdata/*) allow=1;; *) allow=0;; esac
  if [ $allow -eq 0 ]; then
    case "$f" in
      *.db|*.sqlite|*.sqlite3|*.pst|*.ost|*.olm|*.mbox|*.msf|*.exe|*.so|*.dylib|*.zip)
        bad "$f: mail/database/binary file outside internal/**/testdata";;
    esac
  fi

  # Text scan; -I skips binary content. A NUL in the first 8 KB marks a binary file.
  if [ $allow -eq 0 ]; then
    if git show ":$f" 2>/dev/null | head -c 8000 | LC_ALL=C grep -qaP '\x00'; then
      bad "$f: binary file outside internal/**/testdata"
      continue
    fi
  fi
  content="$(git show ":$f" 2>/dev/null | grep -aI '' || true)"
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
