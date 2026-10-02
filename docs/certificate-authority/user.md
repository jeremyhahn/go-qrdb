# Certificate Authority — User Guide

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-09-10

Every caller authenticates with a client certificate. This is where they come
from.

## Prerequisites

- The barrier **unsealed** — the CA private key is destroyed in memory when it
  seals, so nothing can be issued while sealed.
- Client certificates of your own, and the `certs:*` permissions.

## First run

```bash
qrdb ca issue --username alice --out ./alice
ls ./alice        # cert.pem  key.pem
```

Then use them:

```bash
qrdb --server https://db.example.com --ca-file ca.pem \
     --cert-file alice/cert.pem --key-file alice/key.pem \
     kv list app/
```

## What the certificate means

| Field | Becomes |
|---|---|
| CN | your username |
| OU | your tenant |
| SPIFFE SAN `spiffe://<trust-domain>/tenant/<tenant>/user/<username>` | your tenant, under the deployment's trust domain — only when `auth.spiffe_trust_domain` is set |
| SPKI thumbprint | what a user record binds to |

**With a trust domain configured, the SAN is what your login reads the tenant
from**, and a node pinned to a different trust domain refuses the certificate.
Without one, certificates carry no SAN and the OU is read instead. A username or
tenant containing `/` cannot be expressed in the SAN and is refused at issue
time when a trust domain is set.

**The certificate is the identity.** There is no password, which is why issuing
one is a privileged act and why revoking one matters.

**And OU is why `--tenant-id` is not bookkeeping.** A login reads the tenant out
of the OU, so issuing a certificate into a tenant is issuing a credential for
that tenant. You may name your own tenant; naming another is refused, for
everyone — a platform operator in the `system` tenant included. The same rule
governs `ca list` and `ca revoke`.

## Common tasks

**Issue for a specific tenant, with a shorter life.**

```bash
qrdb ca issue --username svc-import --tenant-id acme --ttl 720h --out ./svc
```

Short-lived certificates are the cheap mitigation for the absence of a CRL —
see the troubleshooting note below.

**See what has been issued.**

```bash
qrdb ca list --tenant-id acme --output json
```

**Revoke one.** The certificate stops authenticating against this system within
`auth.revocation_cache_ttl` (default 30 seconds) — including for a client that
already has an open connection. Revocation cannot be undone.

```bash
qrdb ca revoke --serial 3f2a9c...
```

Revoking a serial this system never issued also takes effect — that is how you
revoke a certificate issued by a peer — but it is a **platform-operator**
action, allowed only from the `system` tenant. A serial that matches no record
here belongs to nobody this system can name, so it cannot be told apart from
another customer's serial, and the effect applies to every tenant. Revoking a
serial your own tenant holds needs nothing special.

The command tells you which case happened:

```
OK: certificate record found and marked revoked
OK: no local record for this serial (deny-by-serial revocation)
```

The second line is also what a **mistyped serial** produces, so treat it as a
prompt to double-check the serial against `qrdb ca list` rather than as
confirmation you revoked the certificate you meant to.

**Package a certificate and key for a system that wants PKCS#12** — a browser,
a Java keystore, a Windows certificate store.

```bash
qrdb ca export-bundle --cert-pem alice/cert.pem --key-pem alice/key.pem \
     --out alice.p12 --passphrase "$P12_PASS"
```

**Get the CA certificate.** It is public data and works even while the barrier
is sealed:

```bash
curl -k https://db.example.com/v1/ca/bootstrap -o ca-bootstrap.pem
```

Verify it out of band before you rely on it —
[../trust-bootstrap/user.md](../trust-bootstrap/user.md).

**Make the certificate actually useful.** A certificate authenticates; it does
not authorize. Bind it to a user and give that user a role:

```bash
qrdb users create --username alice --tenant-id system
qrdb roles assign --user <user-id> --role <role-id>
```

Both `assign` flags take **IDs**, not names; `qrdb roles get-by-name Operator`
resolves a role name to its ID.

See [../identity-rbac/user.md](../identity-rbac/user.md).

## Troubleshooting

**Everything CA-related fails with a barrier error.** The barrier is sealed and
the CA private key was destroyed in memory. Unseal, and it comes back. The CA
*certificate* keeps working throughout, on purpose — otherwise a sealed server
would be unreachable rather than just inert.

**You revoked a certificate and an external system still accepts it.** There is
no CRL and no OCSP responder here, on purpose: every consumer of a certificate
this CA issues is inside the cluster, where checking revocation is a local
replicated read rather than a network round trip to the issuer. This system
stops accepting the certificate within the revocation cache TTL, and nothing
outside it notices. If your certificates are checked by external relying parties, the
deployment needs the go-xkms certificate provider, which adds CRL and OCSP.
Until then, short TTLs are the mitigation.

**`qrdb ca info` and `qrdb ca bundle` returned a list of certificates.** They
are aliases for `list`. CA-only metadata and a CA+chain bundle export are not
exposed through the SDK yet — the verbs exist to keep the canonical tree
complete. The help text says this too.

**`qrdb certs` told you to run `qrdb ca`.** The group was renamed; `certs` is a
redirect stub. Note the REST paths were **not** renamed and are still
`/certs/...`.

**The certificate authenticates but you cannot do anything.** Authentication and
authorization are separate. Check the user binding and the role assignment.

**`ca issue`, `ca list` or `ca revoke` was refused with a permission error even
though you hold the `certs:*` permissions.** Those permissions apply to your own
tenant. Naming a different `--tenant-id`, or revoking a serial another tenant
owns, is refused for everyone, the `system` tenant included — holding the
permission is not the same as the certificate being yours to act on. Revoking a
serial nobody here issued needs `certs:revoke:cross`, which only the `system`
tenant can exercise. Ask the tenant that owns the certificate: its own Admin
issues, lists and revokes it.

**A certificate expired.** Issue a new one and revoke the old. There is no
renewal workflow.

**You revoked a certificate by mistake and want it back.** You cannot. There is
no unrevoke on any interface — revocation is a one-way door by construction, so
that nobody who can reach the store can quietly reinstate a withdrawn identity.
Issue a new certificate and bind it to the same user.

**The certificate does not work as a server certificate.** By design — client
certificates carry `ClientAuth` extended key usage only, so one cannot be used
to impersonate a server.

**You want ACME, or a CA backed by a TPM or HSM.** Both are go-xkms, injected
through `WithCertProvider`. The interface is the boundary; qrdb never depends on
go-xkms directly.
