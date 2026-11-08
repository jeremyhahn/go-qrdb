# Certificate Authority — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-26
> Conflicts with this document are defects. Fix them here first.

Command group: `qrdb ca` — post-init Certificate Authority operations. It is
the only CA command group; there is no `qrdb certs`.

All verbs accept the root persistent flags documented in
[../key-value/cli.md](../key-value/cli.md#global-flags).

## `qrdb ca issue`

Issue a client certificate for an **existing** account of your own tenant,
signed by your tenant's own CA, and bind it to the account. The CN is the
account's username and the OU is your tenant, so this call mints an identity.
The server only ever sees a certificate signing request: **the private key
never leaves the machine that made it.**

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--username` | string | — | required; the existing account the certificate is for |
| `--csr` | string | — | a PEM certificate signing request to issue for |
| `--ttl` | string | provider default (1 year) | validity duration, e.g. `8760h` |
| `--out` | string | — | output directory for `cert.pem` (and `key.pem` when the key is generated here) |

Two ways to run it:

- **With `--csr`**, the certificate is issued for that CSR's key, which stays
  wherever the CSR was made. With `--out` the certificate is written as
  `cert.pem`; without, it is printed.
- **Without `--csr`**, the CLI generates an ECDSA P-256 key **on this
  machine**, sends only a CSR for it, and writes `key.pem` and `cert.pem` to
  `--out`, which is then required.

```bash
qrdb ca issue --username alice --out ./alice
qrdb ca issue --username svc-import --csr svc-import.csr --ttl 720h --out ./svc
```

There is no `--tenant-id`: the tenant is always yours. Only the CSR's key is
used — the certificate names the account you pass, whatever the CSR's Subject
says. The certificate carries `ClientAuth` extended key usage only.

## `qrdb ca enroll`

Bind a certificate you already hold — issued by your own CA — to an
**existing** account of your own tenant. It must chain to one of your tenant's
trust anchors ([../trust-anchors/cli.md](../trust-anchors/cli.md)), its CN must
be the username, and it must name your tenant.

| Flag | Type | Meaning |
|---|---|---|
| `--username` | string | required; the existing account |
| `--cert` | string | required; the PEM certificate, leaf first |
| `--chain` | string | optional PEM intermediates |

```bash
qrdb ca enroll --username alice --cert alice.pem --chain issuing-ca.pem
```

## `qrdb ca delegate`

Issue a **delegation**: a short-lived client certificate, signed by your
tenant's own CA, that lets an embedding application's service principal act
inside your tenant with the permissions you name. A request made under it holds
the delegated scope **intersected** with the permissions the application grants
its principal, so a delegation can narrow that authority but never widen it.
Needs `certs:delegate`, which the built-in Admin and SO roles grant. The platform
operator cannot issue one for your tenant: only your own tenant delegates.

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--principal` | string | — | required; the service principal, `[a-z0-9][a-z0-9._-]*`, at most 64 bytes |
| `--scope` | strings | — | required; permissions delegated, repeated or comma-separated (`kv:get,kv:put`) |
| `--csr` | string | — | the principal's PEM certificate signing request |
| `--ttl` | string | `1h` | lifetime; at most `24h` |
| `--out` | string | — | output directory for `cert.pem` (and `key.pem` when the key is generated here) |

As with `ca issue`, **with `--csr`** the key stays wherever the CSR was made,
and **without** it the CLI generates an ECDSA P-256 key on this machine and
writes `key.pem` and `cert.pem` to `--out`, which is then required. Without
`--out` the certificate is printed with its principal, scope, serial and
expiry, or as JSON or YAML with `--output json|yaml`.

```bash
qrdb ca delegate --principal billing --scope kv:get,kv:put --csr billing.csr --out ./billing
qrdb ca delegate --principal billing --scope kv:get --ttl 15m --out ./billing
```

Hand `cert.pem` to the application; it presents it as its client certificate.
The application's node must be configured to accept delegations for that
principal, and one it does not recognise is refused. A delegation expires on
its own at the end of `--ttl`; to end it sooner, revoke it by the printed
serial with `qrdb ca revoke --serial`. It is listed by `qrdb ca list` like any
certificate your CA issued.

Refused with an invalid-argument error: a principal named for a built-in
role (`so`, `admin`, `operator`, `user`, `auditor`), a permission the server does not know, a repeated
permission, more than 64 permissions, or a `--ttl` over `24h`. Refused with a
permission error: a caller in the `system` tenant, and a caller that is itself
authenticated by a delegation, except a renewal within a standing policy
(below).

### `qrdb ca delegate policy`

Write a **standing delegation policy** once instead of issuing a delegation by
hand before each one lapses. While it stands, the application mints the first
certificate for the principal itself and renews it with the certificate it
holds, never with more scope or a longer lifetime than the policy names.

| Verb | Flags | Meaning |
|---|---|---|
| `policy put` | `--principal`, `--scope`, `--ttl` | write or replace the policy for a principal; `--ttl` is the longest lifetime a minted certificate may have, at most `24h` |
| `policy list` | — | list your tenant's policies, with principal, scope and lifetime |
| `policy delete <principal>` | — | delete the policy |

```bash
qrdb ca delegate policy put --principal billing --scope certs:delegate,kv:get,kv:put --ttl 1h
qrdb ca delegate policy list
qrdb ca delegate policy delete billing
```

All three need `certs:delegate` and act in your own tenant; a caller in the
`system` tenant and a delegated caller are refused with a permission error.
Deleting a policy stops every further mint and renewal at once (a not-found
error from then on); a certificate already minted works until it expires or
you revoke its serial. Each put, delete and mint is recorded in your tenant's
audit trail. A renewal needs `certs:delegate` both in the policy's scope and in
the grants of the principal's role. Deleting the tenant deletes its policies,
and a tenant that no longer exists mints nothing.

## `qrdb ca csr`

Generate an ECDSA P-256 key and a certificate signing request **locally**, with
no server involved. Writes `key.pem` and `csr.pem` to `--out`. This is how a
tenant's first admin produces the CSR the platform operator passes to
`qrdb tenant create --admin-csr`, and how anyone produces one for
`qrdb ca issue --csr`.

| Flag | Type | Meaning |
|---|---|---|
| `--username` | string | required; the CSR's Subject CN (the issuer names the account regardless) |
| `--out` | string | required; output directory |

```bash
qrdb ca csr --username alice --out ./alice
```

## `qrdb ca list`

List issued certificates: serial, username, tenant, issue and expiry times,
and revocation state.

| Flag | Type | Default |
|---|---|---|
| `--tenant-id` | string | your own tenant |

The only tenant you can list is your own, so the flag is never needed. Naming
another tenant is refused for everyone, `system` included. Leave it off:

```bash
qrdb ca list --output json
```

## `qrdb ca info` and `qrdb ca bundle`

**Both are aliases for `list`:**

- `info` — CA-only metadata such as DN, fingerprint and validity is **not yet
  exposed through the SDK**.
- `bundle` — a standalone CA+chain bundle export is **not yet exposed through
  the SDK**.

Both take `--tenant-id` (default: your own tenant). Do not read their names as
descriptions of what they return.

## `qrdb ca revoke`

Revoke a certificate by serial number.

| Flag | Type | Meaning |
|---|---|---|
| `--serial` | string | the certificate's serial number; required |

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
authority.** The marker is written and the serial is denied from then on, which
is what revoking a peer-issued certificate needs, but only an identity in the `system` tenant holding `certs:revoke:cross` may
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

`issue`, `enroll` and `delegate` act on your own tenant and name no other. `revoke` and
`list` are authorized against the tenant the COMMAND reaches, not the tenant
your credential is in:

| Command | Refused when |
|---|---|
| `ca issue --username <u>` / `ca enroll --username <u>` | `<u>` is not an account of your tenant (not found) |
| `ca list --tenant-id <t>` | `<t>` is not your tenant — whoever you are, `system` included (`PERMISSION_DENIED`) |
| `ca revoke --serial <s>` | `<s>` belongs to another tenant, or matches no record and you lack `certs:revoke:cross` (`PERMISSION_DENIED`) |

The OU of an issued certificate is where an mTLS login reads the tenant from,
which is why issuance takes no tenant: issuing into a tenant is issuing a
credential FOR that tenant.

An operator in the `system` tenant is confined to the `system` tenant like
everyone else: it cannot issue a certificate for, list, or revoke the
certificates of a customer tenant. Each tenant's own Admin does that. A new
tenant's **first** admin certificate is the one exception, and it is issued at
tenant creation — see [../multi-tenancy/cli.md](../multi-tenancy/cli.md).

## `qrdb ca export-bundle`

Build a PKCS#12 archive from a certificate and its private key, both already on
this machine. No server is contacted: the key leaves the machine only inside
the archive written to `--out`.

Unavailable when the CLI runs with `GODEBUG=fips140=only`: the format's
`localKeyId` attribute is a SHA-1 certificate fingerprint, which the Go
Cryptographic Module refuses to compute under strict enforcement. See
[../seal/fips140-mode.md](../seal/fips140-mode.md).

| Flag | Type | Meaning |
|---|---|---|
| `--cert-pem` | string | required; path to the certificate PEM |
| `--key-pem` | string | required; path to the private key PEM |
| `--ca-pem` | string | optional; path to an issuer certificate PEM to embed in the archive's chain |
| `--out` | string | required; output path for the `.p12` archive |
| `--passphrase` | string | required; passphrase for the archive |

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

`issue`, `delegate`, `revoke` and `list` all fail with a
barrier-sealed error, because the CA private key is destroyed in memory on seal.
Unseal first: [../barrier-seal/cli.md](../barrier-seal/cli.md).

## Before issuing, and after

`issue` and `enroll` need the account to exist, and they bind the certificate
to it themselves. A certificate still authorizes nothing until the account has
a role:

```bash
qrdb users create --username alice --email alice@example.com   # before issuing
qrdb ca issue --username alice --out ./alice
qrdb roles assign --user <user-id> --role <role-id>
```

See [../identity-rbac/cli.md](../identity-rbac/cli.md).
