# Desktop GUI — User Guide

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-06
> Conflicts with this document are defects. Fix them here first.

The same binary that gives you the CLI also gives you a desktop application, and
the same frontend runs in a browser against a remote server.

## Prerequisites

- A binary built with the GUI: `make build-client`. Without the `desktop` build
  tag there is no GUI, and the binary runs as a CLI.
- A display. On Linux that means `DISPLAY` or `WAYLAND_DISPLAY`; on macOS
  anything that is not an SSH session; on Windows, always.
- **Client certificates.** There is no login screen — authentication is mTLS.

## Launching it

```bash
qrdb            # no arguments, with a display: the GUI opens
qrdb kv get x   # a verb: the CLI runs
```

Over SSH on macOS you get the CLI, because `SSH_TTY` is set. That is deliberate.

## First run

The setup wizard walks you through it:

1. **Server mode** — embedded (a server inside this application) or remote.
2. **Embedded**: storage backend (PebbleDB or memory), Raft configuration if you
   want it, the encryption barrier (Shamir shares and threshold), a summary, then
   initialization.
3. **Remote**: the server address and TLS settings, a summary, done.

After the wizard, if the server is sealed you get an unseal prompt; otherwise
the full application.

**Keeping an audit trail.** The embedded server can keep the same compliance
audit trail as a qrdbd node: set `audit` to `{"enabled": true}` in its saved
configuration, and it records to `audit-events.ndjson` in the data directory
(or `path`, relative to the data directory). It is off by default. Once it is
on, a trail that cannot be opened -- a full disk, a directory in its place --
stops the server from starting rather than running without it.

## Creating a database

**New Database** opens the database wizard: name and engine, storage, Raft
settings on a Raft server, wire protocols, the encryption barrier, a summary,
then creation.

The barrier is off unless you choose one. Choosing **Shamir** asks how many
holders there will be (N, at most 255) and how many of them unseal the
database together (M, at least 2), both whole numbers. After the database is created, sealed, the wizard asks you to
enrol its N holders: for each, a username, a role and the certificate request
the holder generated on their own machine. The wizard shows each holder's
issued certificate for you to pass on, and once all N are enrolled,
**Initialize** splits the key and seals one envelope to each holder. Holders
then claim their envelopes, and later unseal the database, on their own
machines with their own keys and the qrdb CLI; the wizard never handles a
share or a holder's key. If the server cannot build a Shamir barrier -- it has no
share-holder custody configured -- the create step shows the server's reason
and no database is created. Closing the wizard before **Initialize** leaves
the database sealed until you finish enrolling its holders: open the
database's view, where **Share Holders** shows who is enrolled, the forms for
the rest and **Initialize**, or use the CLI. A database takes exactly N
holders before it is initialized: the server refuses one more, wherever it is
enrolled from. Until then **Remove** takes a holder off -- they hold no share
yet -- and frees their place; after initialization a holder is removed by
revocation, which the remaining holders complete.
Choosing **Passphrase** asks for the
passphrase twice; it is sent once, when the database is created, and the
application does not keep it or show it again. You need it to unseal the
database, so store it safely. A per-database barrier needs the database's own
data directory, set in the storage step.

## Everyday use

The application shows what the server can actually do — views are gated on the
server's reported capabilities, so a server without vectors has no vector view.
Your identity appears in the header, read from your client certificate.

## In a browser

The same frontend runs against a remote `qrdbd` over its REST API. Nothing
special is needed server-side beyond CORS being enabled, and your browser
needs to present a client certificate as usual.

## Building

```bash
make install-frontend
make build-frontend
make build-client        # single binary, all assets embedded
make dev-gui             # hot reload while developing
```

`build-client` embeds the frontend, so the binary is self-contained — there is
nothing to install alongside it.

## Troubleshooting

**`qrdb` with no arguments printed CLI help instead of opening a window.**
Either the binary was built without the `desktop` tag, or no display was
detected. On macOS over SSH the latter is intentional.

**"GUI unavailable".** The build lacks the `desktop` tag. It requires CGO;
rebuild with `make build-client`.

**`--transport embedded` fails.** The embedded transport is part of the
desktop/GUI build. It is listed in the flag's values on every build but only
works in that one.

**The GUI shows fewer views than you expect.** Views are gated on the server's
capabilities. Check `qrdb admin capabilities`: the feature is either not
compiled in, gated by the run mode, or disabled in config.

**It is stuck on an unseal prompt.** The server is sealed. Unseal it — see
[../barrier-seal/user.md](../barrier-seal/user.md).

**There is no username or password box.** Correct. Authentication is mTLS at the
TLS handshake. If you cannot connect, the problem is your certificate, not a
missing credential prompt.

**You are worried the GUI can do more than the CLI.** It cannot. There is no
GUI-only API; both paths use the same surfaces, and you have exactly the
permissions your certificate's user holds.

**The desktop app is holding your data.** In embedded mode it is: the data
directory and the barrier are on your machine. Treat a desktop deployment as the
single-node server it is, including for backups.
