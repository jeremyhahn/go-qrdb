# Trust Bootstrap — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-06

## Capability matrix

| Capability | REST | gRPC / Unix | QUIC | MCP | Embedded | `scp` permission |
|---|---|---|---|---|---|---|
| Fetch the CA bootstrap certificate | `GET /v1/ca/bootstrap` | MISSING | MISSING | MISSING | — | **public**, conditionally registered |
| Is the system initialized? | `GET /v1/config/initialized` | MISSING | MISSING | MISSING | — | **public** |
| `auto` / `dane` / `noise` / `spki` / `direct` client modes | **not implemented here** | — | — | — | — | n/a |

The client-side modes have no API because they have no implementation in this
repository — `qrdb bootstrap <mode>` returns `PendingBootstrapBackendError` and
the TOFU implementation lives in go-xkms. See
[engineering.md](engineering.md#implementation-status).

The two REST rows are **the only unauthenticated routes in this feature**, and
two of the very few in the whole server. Both exist because a client in the
bootstrap window has no credentials to present. Their absence from the other
transports is marked `MISSING` per the convention, though it is largely moot:
gRPC, QUIC, Unix and MCP all expect an mTLS handshake a credential-less client
cannot complete.

## `GET /v1/ca/bootstrap`

Serves the CA bootstrap certificate: `CERTIFICATE` PEM blocks and **nothing
else**. The route is public, so it never serves key material. The configured
bytes are reduced to their `CERTIFICATE` blocks when the server is built, so
a private key concatenated onto the certificate is dropped rather than
published. Releases before this one served the cluster CA private key here;
see the CHANGELOG entry for the rotation that requires.

**Registered only when `len(s.caBootstrapCert) > 0`.** A deployment that has not
configured a bootstrap certificate returns 404 — the route does not exist,
rather than existing and returning nothing. That distinction matters: an empty
200 would be indistinguishable from a stripped response.

```bash
curl -k https://db.example.com/v1/ca/bootstrap -o ca-bootstrap.pem
```

**Fetching this over an unverified connection is trust-on-first-use.** The
whole point of the DANE, Noise and SPKI modes is to give the client an
independent way to verify what it received. `direct` mode substitutes an
operator's out-of-band file copy for that verification.

## `GET /v1/config/initialized`

Reports whether the system has completed bootstrap. Public, unconditional.

## Status and error mapping

| Condition | REST |
|---|---|
| Certificate served | 200 |
| No bootstrap certificate configured | 404 — route unregistered |
| System not initialized | 200 with the negative answer, not an error |

## Versioning

Both routes are under `/v1/`. Neither is deprecated.
