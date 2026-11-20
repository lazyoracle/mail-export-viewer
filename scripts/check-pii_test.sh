#!/usr/bin/env bash
# Tests for check-pii.sh: builds throwaway git repos and checks the verdict.
# Fixtures are assembled at run time so this file itself holds no trigger text.
set -u
here="$(cd "$(dirname "$0")" && pwd)"
script="$here/check-pii.sh"
pass=0; fail=0
tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT

newrepo() { # name [author-email]
  local d="$tmp/$1"; mkdir -p "$d"
  git -C "$d" init -q
  git -C "$d" config user.name "Test Author"
  git -C "$d" config user.email "${2:-1234+test@users.noreply.github.com}"
  echo "$d"
}
commit() { git -C "$1" add -A && git -C "$1" commit -q -m t; }
expect() { # want(0|1) name repo
  local out rc
  out="$("$script" "$3" 2>&1)"; rc=$?
  if { [ "$1" = 0 ] && [ $rc -eq 0 ]; } || { [ "$1" = 1 ] && [ $rc -ne 0 ]; }; then
    pass=$((pass+1)); echo "ok   $2"
  else
    fail=$((fail+1)); echo "FAIL $2 (rc=$rc)"; echo "$out" | sed 's/^/     /'
  fi
}

h="/ho""me/alice"; m="/Us""ers/bob"
at="@"
r=$(newrepo clean); echo 'hello, mail to a'"$at"'example.com and b'"$at"'users.noreply.github.com' >"$r/a.txt"; commit "$r"
expect 0 "clean repo passes" "$r"

r=$(newrepo linuxpath); echo "path $h/mail" >"$r/a.txt"; commit "$r"
expect 1 "linux home path fails" "$r"

r=$(newrepo macpath); echo "path $m/x" >"$r/a.txt"; commit "$r"
expect 1 "macos home path fails" "$r"

r=$(newrepo email); echo "carol${at}corp.test" >"$r/a.txt"; commit "$r"
expect 1 "real-looking email fails" "$r"

r=$(newrepo exsub); echo "carol${at}mail.example.org" >"$r/a.txt"; commit "$r"
expect 0 "example subdomain email passes" "$r"

r=$(newrepo key); printf -- '-----BEGIN %s PRIVATE KEY-----\n' RSA >"$r/a.txt"; commit "$r"
expect 1 "private key fails" "$r"

r=$(newrepo token); echo "t=gh""p_abcdefghijklmnopqrstuvwxyz0123456789" >"$r/a.txt"; commit "$r"
expect 1 "github token fails" "$r"

r=$(newrepo big); head -c $((5*1024*1024+1)) /dev/zero | tr '\0' 'a' >"$r/big.txt"; commit "$r"
expect 1 "file over 5MB fails" "$r"

r=$(newrepo mbox); printf 'From a Mon\n' >"$r/x.mbox"; commit "$r"
expect 1 "mbox outside testdata fails" "$r"

r=$(newrepo db); printf 'x' >"$r/x.db"; commit "$r"
expect 1 "db file fails" "$r"

r=$(newrepo bin); printf '\x00\x01\x02' >"$r/blob.dat"; commit "$r"
expect 1 "binary file outside testdata fails" "$r"

r=$(newrepo td); mkdir -p "$r/internal/mail/testdata"; printf 'From a Mon\n' >"$r/internal/mail/testdata/x.mbox"
printf '\x00\x01' >"$r/internal/mail/testdata/b.bin"; commit "$r"
expect 0 "mbox/binary under internal/**/testdata passes" "$r"

r=$(newrepo staged); : >"$r/a.txt"; commit "$r"; echo "$h" >"$r/b.txt"; git -C "$r" add b.txt
expect 1 "staged offending file fails" "$r"

r=$(newrepo author "dave${at}corp.test"); echo ok >"$r/a.txt"; commit "$r"
expect 1 "non-noreply commit author fails" "$r"

r=$(newrepo latin1); printf 'caf\xe9 %s/mail\n' "$h" >"$r/a.txt"; commit "$r"
expect 1 "latin-1 text with home path fails" "$r"

r=$(newrepo latin1mail); printf 'caf\xe9 erin%scorp.test\n' "$at" >"$r/a.txt"; commit "$r"
expect 1 "latin-1 text with email fails" "$r"

r=$(newrepo corpnoreply); echo "x${at}corp-noreply.com" >"$r/a.txt"; commit "$r"
expect 1 "lookalike noreply domain fails" "$r"

r=$(newrepo exio); echo "x${at}example.io" >"$r/a.txt"; commit "$r"
expect 1 "example.io fails" "$r"

r=$(newrepo noreplylocal); echo "noreply${at}vendor.test" >"$r/a.txt"; commit "$r"
expect 0 "noreply local part passes" "$r"

r=$(newrepo pkgver); echo "go get pkg${at}v1.2.3" >"$r/a.txt"; commit "$r"
expect 0 "pkg@version is not an email" "$r"

r=$(newrepo selfscan); cp "$script" "$r/check-pii.sh"; commit "$r"
expect 0 "script's own text is clean without exemption" "$r"
printf -- '-----BEGIN %s PRIVATE KEY-----\n' EC >>"$r/check-pii.sh"; commit "$r"
expect 1 "secret in a script file is flagged" "$r"

echo "passed=$pass failed=$fail"
[ $fail -eq 0 ]
