# Desktop GUI — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-21
> Conflicts with this document are defects. Fix them here first.

**No `qrdb` verb launches the GUI.** The same binary is both the CLI and the
GUI: invoked with **no arguments** on a machine with a display, it opens the
window; invoked with a verb, it runs the CLI.

## Launching

```bash
qrdb          # opens the GUI when a display is detected and the build has it
qrdb kv get x # runs the CLI, regardless
```

Display detection:

| Platform | GUI launches when |
|---|---|
| Linux | `DISPLAY` or `WAYLAND_DISPLAY` is set |
| Darwin | always, **unless `SSH_TTY` is set** |
| Windows | always |

The Darwin `SSH_TTY` check is the one you will notice: over SSH, a bare `qrdb`
gives you the CLI rather than trying to open a window.

## Building it

```bash
make install-frontend    # npm install for the TypeScript SDK + frontend
make build-frontend      # build the TypeScript SDK + Vite frontend
make build-client        # single binary, CLI + GUI, assets embedded
make dev-gui             # Wails dev mode with hot reload
```

`build-client` embeds every frontend asset with `//go:embed`, so the binary
needs nothing external at runtime.

The GUI requires the `desktop` build tag (and CGO). **Without it the binary is
CLI-only**: asking for the GUI yields `GUIUnavailableError` and the CLI runs
instead.

## The `embedded` transport

`--transport embedded` is an accepted value, but every CLI verb given it fails
with `EmbeddedTransportUnavailableError`: the CLI does not link the server
in-process. The embedded server is reached only from the GUI, through its
connection manager, in a build with the `qrdb_embedded` tag
(`WITH_QRDB_EMBEDDED=1`, the default).

## What the GUI does

- **First run** — **Add Server** opens: create an embedded server or connect
  to an external one. In a browser, a server never initialized gets the
  initialization wizard: barrier, share holders when Shamir, first admin
  credential.
- **Returning** — if the server is sealed it shows the unseal prompt; otherwise
  the full application.
- **Views are capability-gated**: what appears follows what the server reports
  through `admin capabilities`.

## Authentication

**There is no login screen.** Authentication is mTLS at the TLS handshake, and
the identity in the header comes from the client certificate's Subject CN. The
GUI is exactly as privileged as the certificate it presents — no more.

## Parity

The GUI has no `CommandDefinition`s; it is not a verb surface. The bindings
behind it are covered by Go unit tests; the TypeScript SDK's contract suites run
in CI's `test-typescript` job — see
[engineering.md](engineering.md#typescript-in-ci).
