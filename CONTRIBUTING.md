# Contributing

Thank you for helping. This project reads people's private mail, so two rules
come before everything else.

## Never include real mail

- **Never** put real mail in an issue, pull request, commit, test fixture,
  screenshot or log. That covers real messages, exports, `.msf` files,
  `prefs.js`, addresses, names, subjects and file paths. Mail you received
  from someone else is especially off limits.
- **Fixtures are synthetic and generated in code.** The generators live in
  `internal/testutil` and `internal/testutil/corpus`. Use `example.com`,
  `example.org` or `example.net` addresses and invented text.
- **When a real export breaks the viewer**, describe its structure instead:
  - the warnings the viewer printed;
  - an `unzip -l` listing or a directory listing, with names replaced;
  - the XML element names or PST property tags involved, with every value
    replaced.

  Better still, write a small synthetic file that reproduces the problem.
- **Screenshots** must show only synthetic data, for example the demo
  archive from `TestServeDemo`.

## Run `make check-pii` before every commit

`scripts/check-pii.sh` scans tracked and staged files and the commit
identities. It looks for:

- home-directory paths;
- e-mail addresses outside `example.com/.org/.net` and noreply addresses;
- private keys and tokens;
- mail, database and binary files outside `internal/**/testdata`;
- files over 5 MB.

CI runs the same check. Commit with your GitHub noreply address
(`<id>+<user>@users.noreply.github.com`).

## Development setup

- **Go 1.24 or newer** (`go.mod`: `go 1.24.0`). Dependencies must keep
  building with Go 1.24; check a dependency bump with `GOTOOLCHAIN=local`.
- **Node 22** for the UI unit tests (`make test-js`). No npm packages are
  used.
- **Optional**:
  - a C compiler, for `make test-race`;
  - a `chrome-headless-shell` binary, for the browser checks.

```sh
make build && ./mail-export-viewer -version
make vet test test-js check-pii
```

The README's [Development](README.md#development) section explains the layout,
the test environment variables (end-to-end, fuzzing, performance, browser,
PST samples) and how to add a format.

## Tests first

Every change in behaviour starts with a failing test:

1. Write the test and watch it fail for the right reason.
2. Make it pass with the smallest change.
3. Clean up.

For a bug, add the synthetic reproducer as a regression test. Parsers and
other code that handles hostile input should also get a fuzz seed, or a
fuzz target if none covers it.

Before opening a pull request, run:

- `gofmt -l .` (it must print nothing);
- `make vet`, `make test` and `make test-js`;
- `make test-race` where you can;
- `make check-pii`.

If you touched the UI, run `TestBrowser`. If you touched indexing or search
paths, compare with the numbers in `docs/validation.md`.

## Design rules

- **One implementation of each thing**: one MIME parser (`internal/mail`),
  one sanitiser, one index, one UI. Formats plug in through
  `internal/source`, and the index, server and UI never import format
  packages.
- **Nothing is written next to the input, and nothing goes to the network.**
  A new feature that needs either will not be accepted.
- **Bound everything that comes from input**: sizes, counts, nesting and
  time. One bad message must never stop the rest.
- **Write decisions and limits down** in the package's `doc.go`, and
  user-visible ones in the README.
- **No new dependencies without a strong reason.** New dependencies must be
  pure Go (no cgo), under a permissive licence, and added to
  `THIRD_PARTY_NOTICES.md`. Code from GPL, LGPL or AGPL projects cannot be
  used.

## Commits and pull requests

- Keep commits small and focused, one logical change each. Write the
  subject in the imperative ("Add OLM folder names from Folders.xml") and
  explain the why in the body.
- Do not commit generated corpora, caches, binaries or `dist/`; they are
  git-ignored. Files you need for testing go in `tests/tmp/` or a temporary
  directory.
- Pull requests must pass CI on Linux and macOS.
- Security problems go through private reporting, not pull requests. See
  [SECURITY.md](SECURITY.md).

By contributing you agree that your contribution is licensed under the MIT
License of this project.
