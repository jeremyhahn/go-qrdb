# Trust Bootstrap — Architecture

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-06

> **Shape only in this repository.** `qrdb bootstrap` exposes the canonical
> command surface; **every mode returns `PendingBootstrapBackendError`.** The
> TOFU implementation lives in go-xkms. See [cli.md](cli.md).

## Problem

A fresh client has no CA bundle. Fetching one over the connection you have not
yet authenticated is circular: whatever answers can hand you its own root and
you have no way to tell. Every solution is some way of getting one bit of
trusted information out of band, and the modes differ in *which* out-of-band
channel they use.

## Shape chosen

Five named modes, from strongest available downward:

| Mode | Out-of-band channel | What it pins |
|---|---|---|
| `dane` | DNSSEC-validated DNS | a TLSA record (RFC 6698) |
| `noise` | a Noise XK handshake with a pre-shared static key | the server's static key |
| `spki` | an operator-conveyed hash | the SHA-256 of the server's SPKI |
| `direct` | operator file copy | the CA bundle itself |
| `auto` | — | picks the strongest of the above that is available |

`auto` exists so a client does not have to encode the deployment's trust policy
at every call site; the ladder is evaluated at connect time.

### Alternatives rejected

- **Fetching the CA bundle over plain TLS and trusting it.** Rejected — that is
  trust-on-first-use with no verification at all, and it is what these modes
  exist to avoid.
- **A single mandatory mode.** Rejected: DANE needs DNSSEC, Noise needs a
  distributed static key, SPKI needs a conveyed hash, and `direct` needs a file.
  Different deployments can supply different things, and refusing all but one
  would make the feature unusable in most of them.
- **Implementing TOFU in qrdb.** Rejected — trust bootstrap belongs with the
  trust primitives, which live in go-xkms (built on go-truststrap). qrdb
  exposes only the command shape so **the canonical surface is identical across
  every binary in the stack**.

## Not to be confused with

- **`qrdb init`** — server-side first touch: barrier, CA, admin, roles. See
  [../firstboot/](../firstboot/). Trust bootstrap is the *client* side,
  establishing trust to a remote node.
- **Cluster discovery** — finding Raft peers. See
  [../clustering/](../clustering/architecture.md).
- **Service discovery** — a consumer concern built on lease + watch + KV.

## Component boundaries

| Piece | Package | Owner |
|---|---|---|
| `qrdb bootstrap` command shape | `cmd/qrdb/bootstrap.go` | go-qrdb — **shape only** |
| TOFU / DANE / Noise / SPKI implementations | — | **go-xkms**, over go-truststrap |
| DANE, Noise_NK, SPKI-pinned TLS primitives | — | **go-truststrap** |
| The CA bootstrap endpoint the client fetches from | `pkg/api/rest` (`GET /v1/ca/bootstrap`) | go-qrdb |

The one piece of this feature that is fully implemented **here** is the server
side of `direct`: `GET /v1/ca/bootstrap` is a public route that serves the CA
bootstrap bundle. It is registered only when exactly one source is
configured: qrdb's cluster CA, or a bundle the embedding host supplies through
`WithBootstrapCABundle`. Configuring both is a startup error.

## Trust boundaries

- **Every mode's security rests on its out-of-band channel.** DANE is only as
  good as the DNSSEC chain; SPKI only as good as how the hash reached the
  client; `direct` only as good as the file copy.
- **`GET /v1/ca/bootstrap` is unauthenticated by necessity** — a client in the
  bootstrap window has no credentials. It is registered only when the deployment
  configures a bootstrap certificate, so it does not exist by default.
- **Because it is unauthenticated, it serves certificates only.** No CA
  private key is ever served, by this route or by the Noise advertiser. Earlier
  releases served the cluster CA key here, and every multi-node cluster formed
  by them must treat that key as compromised and rotate it.
- **`auto` picking a weaker mode is a security-relevant outcome**, not a
  convenience. A deployment with a policy floor should name the mode.

## What this feature deliberately does not do

- **It does not implement any of the modes.** In this repository they are
  command shapes returning a typed pending error.
- **No server-side trust decisions.** Trust bootstrap is a client-side act.
