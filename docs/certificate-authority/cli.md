# Certificate Authority — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-09-10

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

**No CRL is generated and no OCSP responder is served** — deliberately, see
[architecture.md](architecture.md) — so external relying parties will not see
it. Within this system the revoked certificate stops authenticating: every
transport checks revocation on the authentication path, on every request, so a
client already holding an open connection is refused too.

There is a bounded lag. A node that has recently seen the certificate caches
the not-revoked answer for `auth.revocation_cache_ttl` (default `30s`), and
**that value is the window during which the revoked certificate still
authenticates**. Set it to `0` to close the window entirely.

**Revoking a serial this node holds no record for requires platform-operator
authority.** The behaviour is unchanged — the marker is written and the serial
is denied from then on, which is what revoking a peer-issued certificate needs
— but only an identity in the `system` tenant holding `certs:revoke:cross` may
ask for it. A serial that matches no record names no tenant, so there is no way
to tell "revoke a peer's certificate" from "revoke another customer's
certificate", and the marker applies cluster-wide to both. A tenant admin
revoking a serial its **own** tenant holds is unaffected and needs nothing
extra.

The command tells you which case occurred:

```
OK: certificate record found and marked revoked
OK: no local record for this serial (deny-by-serial revocation)
```

The second line is expected when revoking a peer-issued certificate. It is
also what a mistyped serial produces, so treat it as a prompt to double-check
the serial against `qrdb ca list` rather than as an error — the command
cannot tell the two apart on its own.

**There is no unrevoke.** No verb removes a revocation, on any transport.

### Which certificates you may act on

`issue`, `revoke` and `list` are authorized against the tenant the COMMAND
names, not the tenant your credential is in:

| Command | Refused with `PERMISSION_DENIED` when |
|---|---|
| `ca issue --tenant-id <t>` | `<t>` is not your tenant and you are not in `system` |
| `ca list --tenant-id <t>` | `<t>` is not your tenant and you are not in `system` |
| `ca revoke --serial <s>` | `<s>` belongs to another tenant, or matches no record and you lack `certs:revoke:cross` |

`ca issue` is the one to watch: `--tenant-id` becomes the certificate's OU, and
the OU is where an mTLS login reads the tenant from. Issuing into a tenant is
issuing a credential FOR that tenant.

An operator in the `system` tenant is unrestricted, which is how a platform
administrator provisions and audits customer tenants.

`ca export-bundle` names no tenant: it re-encodes the certificate and key you
supply, and the server holds neither.

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
