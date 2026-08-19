# Certificate Authority — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

Command group: `qrdb ca` — post-init Certificate Authority operations. **This
is the canonical CA surface; it replaces the legacy `certs` group.**

`qrdb certs` still exists as a redirect stub and prints:
*"subcommand 'qrdb certs' has been replaced by 'qrdb ca'. Run: qrdb ca"*.

All verbs accept the root persistent flags documented in
[../key-value/cli.md](../key-value/cli.md#global-flags).

## `qrdb ca issue`

Issue a new client certificate. The CN becomes the username and the OU becomes
the tenant, so this call mints an identity.

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--username` | string | — | username for the certificate subject (CN) |
| `--tenant-id` | string | `system` | tenant (OU) |
| `--ttl` | string | provider default (1 year) | validity duration, e.g. `8760h` |
| `--out` | string | — | output directory for `cert.pem` and `key.pem` |

```bash
qrdb ca issue --username alice --out ./alice
qrdb ca issue --username svc-import --tenant-id acme --ttl 720h --out ./svc
```

The certificate carries `ClientAuth` extended key usage only.

## `qrdb ca list`

List issued certificates: serial, username, tenant, issue and expiry times,
and revocation state.

| Flag | Type | Default |
|---|---|---|
| `--tenant-id` | string | `system` |

```bash
qrdb ca list --tenant-id acme --output json
```

## `qrdb ca info` and `qrdb ca bundle`

**Both are aliases for `list` today.** The help text says so directly:

- `info` — CA-only metadata such as DN, fingerprint and validity is **not yet
  exposed through the SDK**.
- `bundle` — a standalone CA+chain bundle export is **not yet exposed through
  the SDK**.

Both take `--tenant-id` (default `system`). They exist so the canonical verb
tree is complete; do not read their names as descriptions of what they return.

## `qrdb ca revoke`

Revoke a certificate by serial number.

| Flag | Type |
|---|---|
| `--serial` | string |

```bash
qrdb ca revoke --serial 3f2a9c...
```

Revocation is recorded in an internal blocklist. **No CRL is generated and no
OCSP responder is served**, so external relying parties will not see it — see
[architecture.md](architecture.md). Within this system, the revoked certificate
stops authenticating.

## `qrdb ca export-bundle`

Export a certificate and key as a PKCS#12 archive.

Unavailable when the daemon runs with `GODEBUG=fips140=only`: the format's
`localKeyId` attribute is a SHA-1 certificate fingerprint, which the Go
Cryptographic Module refuses to compute under strict enforcement. See
[../seal/fips140-mode.md](../seal/fips140-mode.md).

| Flag | Type | Meaning |
|---|---|---|
| `--cert-pem` | string | path to the certificate PEM |
| `--key-pem` | string | path to the private key PEM |
| `--out` | string | output path for the `.p12` archive |
| `--passphrase` | string | passphrase for the archive |

```bash
qrdb ca export-bundle --cert-pem alice/cert.pem --key-pem alice/key.pem \
     --out alice.p12 --passphrase "$P12_PASS"
```

## Getting the CA certificate

The CA certificate is public data and is available even while the barrier is
sealed:

```bash
curl -k https://db.example.com/v1/ca/bootstrap -o ca-bootstrap.pem
```

That route exists only when a bootstrap certificate is configured — see
[../trust-bootstrap/cli.md](../trust-bootstrap/cli.md).

## When the barrier is sealed

`issue`, `revoke`, `export-bundle` and `list` all fail with a
barrier-sealed error, because the CA private key is destroyed in memory on seal.
Unseal first: [../barrier-seal/cli.md](../barrier-seal/cli.md).

## After issuing

A certificate alone does not authorize anything. Bind it to a user and give the
user a role:

```bash
qrdb users create --username alice           # if the user does not exist
qrdb roles assign --username alice --role Operator
```

See [../identity-rbac/cli.md](../identity-rbac/cli.md).

## Parity

Every verb here has a `CommandDefinition` in the integration parity suite.
