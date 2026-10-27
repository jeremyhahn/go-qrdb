# Trust Bootstrap — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-26
> Conflicts with this document are defects. Fix them here first.

Command group: `qrdb bootstrap` — trust bootstrap for a **fresh client**
establishing CA trust to a remote node.

> **Every subcommand in this group fails.** They return a typed
> `PendingBootstrapBackendError`. The underlying TOFU implementation lives in
> go-xkms; qrdb exposes the command shape so the canonical surface is identical
> across `qrdb`, `xkmsctl`, `quicbaasctl` and `platformctl`.

**Distinct from `qrdb init`**, which is server-side first touch — see
[../firstboot/cli.md](../firstboot/cli.md).

## The modes

| Verb | Mechanism |
|---|---|
| `qrdb bootstrap auto` | pick the strongest available trust mode automatically |
| `qrdb bootstrap dane` | DANE TLSA pinning (RFC 6698) |
| `qrdb bootstrap noise` | Noise NK out-of-band trust handshake |
| `qrdb bootstrap spki` | SPKI SHA-256 pin |
| `qrdb bootstrap direct` | direct CA bundle copy (operator-supplied trust root) |

**None of them takes a flag.** Each accepts arbitrary positional arguments; the
real argument set will be defined by the backend that implements it.

```bash
qrdb bootstrap auto
qrdb bootstrap spki
```

Every verb prints nothing on stdout, writes this line to stderr and exits `1`:

```text
bootstrap <mode> is owned by xkms; invoke via xkmsctl bootstrap <mode> or platformctl bootstrap <mode>
```

## What works

The **server side of `direct`** is implemented: a public endpoint serves the CA
bootstrap certificate to a client with no credentials.

```bash
curl -k https://db.example.com/v1/ca/bootstrap -o ca-bootstrap.pem
```

That route exists **only when the deployment configures a bootstrap
source**; otherwise it is a 404 rather than an empty answer.

Having obtained a CA bundle by whatever out-of-band means your deployment
supports, use it as `--ca-file` on ordinary commands:

```bash
qrdb --server https://db.example.com --ca-file ca-bootstrap.pem \
     --cert-file client.pem --key-file client-key.pem barrier status
```

You can also confirm a server has been initialized before you have any
credentials:

```bash
curl -k https://db.example.com/v1/config/initialized
```

## Parity

The five mode verbs exist to hold the canonical surface across the stack's
binaries. `test/integration/bootstrap_cli_test.go` pins that shape on every
transport: each verb fails with the pending-backend error, positional
arguments are accepted, and any flag is rejected as unknown.
