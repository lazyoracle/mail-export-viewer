# Security policy

## Supported versions

Only the latest release gets security fixes. There is no long-term support
branch. Before reporting, please check whether the problem still exists on
the latest release or on `main`.

## Reporting a vulnerability

Please report vulnerabilities **privately** through GitHub's private
vulnerability reporting:

1. Go to the repository's **Security** tab.
2. Choose **Report a vulnerability**.

Please do not open a public issue for a security problem.

A good report includes:

- the version (`mail-export-viewer -version`) and your platform;
- what an attacker controls (a crafted message, an export file, a web page
  you visit, a local user) and what they gain;
- a reproducer made only of **synthetic** data: invented text and
  `example.com` addresses. Never send real mail or real exports, not even
  privately.

This is a volunteer project, so there is no guaranteed response time. You
will get an acknowledgement in the advisory thread. The fix will be released
and the advisory published once a fixed release is available, crediting you
if you want.

## Threat model

mail-export-viewer reads mail exports on your own machine and shows them in
your browser through a server bound to the loopback interface.

**What we protect:** your mail and the machine running the viewer.

**Attackers we consider:**

1. **Hostile mail content.** Any message in an archive may be crafted, for
   example spam, phishing or a message sent to you specifically. It must not
   be able to:
   - run script in the viewer's origin;
   - read other messages;
   - make your browser or the viewer contact the network (tracking pixels,
     remote CSS);
   - trick you into opening a dangerous attachment inline;
   - make the viewer crash, hang or use unbounded memory or disk.
2. **Hostile export files.** A crafted PST, OLM (zip), mbox, `.msf` or
   `.eml` file, such as a zip bomb, a cyclic structure, huge declared sizes
   or path-traversal names, must not:
   - write outside the cache directory;
   - read files outside the input;
   - exhaust memory or CPU without bound;
   - cause another message's bytes to be served.
3. **Web pages you visit while the viewer runs.** They must not be able to
   read the API: there is no CORS, and the `Host` allow-list blocks DNS
   rebinding. They must not be able to frame the UI or change anything.
   There is nothing to change, because the server is read-only and accepts
   only GET and HEAD.
4. **Other local users on a shared machine.** They must not be able to read
   the index cache, which holds your mail's text: it is `0600` in a `0700`
   directory. They must not be able to redirect the viewer's writes through
   symlinks.

**Out of scope:**

- An attacker who already runs code as your user, or who has root.
- Anyone who can reach the port when you run with `-allow-remote`. There is
  no authentication; the flag exists for trusted setups only and prints a
  warning.
- The browser itself, its extensions, and what you do with a downloaded
  attachment after saving it.
- Recovering deleted or encrypted content. The viewer does not do this.

## Defences

The [README](README.md#security-model) describes the defences in detail.
In short:

- **Network**:
  - the server listens on loopback only and checks the `Host` header;
  - only GET and HEAD are accepted, with no CORS;
  - strict Content-Security-Policy, `nosniff`, `X-Frame-Options: DENY`,
    COOP and CORP headers on every response.
- **HTML mail**:
  - one allow-list HTML sanitiser on the server;
  - a `srcdoc` iframe sandboxed with only
    `allow-popups allow-popups-to-escape-sandbox`, so no scripts run and the
    frame has no same-origin access;
  - remote content is always blocked.
- **Attachments**: always downloads with cleaned file names. Unknown or
  active types are sent as `application/octet-stream`, and SVG is never
  shown inline.
- **Hostile files**: every format reader caps sizes, nesting and counts. Each
  message is read separately and a panic in one is recovered. The readers
  are fuzzed (see [docs/validation.md](docs/validation.md)).
- **No network and read-only**: the binary makes no outbound connections
  and never writes to its input.

## Supply chain

- **Dependencies are minimal and pure Go**: `golang.org/x/text`,
  `golang.org/x/net` and `modernc.org/sqlite` (with its own dependencies).
  [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md) lists everything compiled
  into the binaries. `go.sum` pins every module hash.
- **Release binaries**:
  - built by `.github/workflows/release.yml` with `CGO_ENABLED=0` and
    `-trimpath` from a validated `v*` tag;
  - published with a `SHA256SUMS` file;
  - the build job has a read-only token, and only the separate `publish`
    job, which runs no project code, can write;
  - checkouts do not persist credentials.
- **Unpinned actions (TODO).** The workflows reference third-party actions
  (`actions/checkout`, `actions/setup-go`, `actions/setup-node`,
  `actions/upload-artifact`, `actions/download-artifact`) by **major version
  tag**, not by commit SHA. Before publishing releases from this repository,
  pin each one to a full commit SHA checked against the action's repository
  (with the tag as a comment). Dependabot (`.github/dependabot.yml`) then
  keeps the pins current.
- **No signatures yet.** Release binaries are not signed or notarised. Verify
  them with `SHA256SUMS`, or build from source.
