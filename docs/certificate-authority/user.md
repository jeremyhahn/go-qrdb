# Certificate Authority — User Guide

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

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
| SPKI thumbprint | what a user record binds to |

**The certificate is the identity.** There is no password, which is why issuing
one is a privileged act and why revoking one matters.

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

**Revoke one.**

```bash
qrdb ca revoke --serial 3f2a9c...
```

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
qrdb users create --username alice
qrdb roles assign --username alice --role Operator
```

See [../identity-rbac/user.md](../identity-rbac/user.md).

## Troubleshooting

**Everything CA-related fails with a barrier error.** The barrier is sealed and
the CA private key was destroyed in memory. Unseal, and it comes back. The CA
*certificate* keeps working throughout, on purpose — otherwise a sealed server
would be unreachable rather than just inert.

**You revoked a certificate and an external system still accepts it.** There is
no CRL and no OCSP responder here. Revocation is an internal blocklist: this
system stops accepting the certificate immediately, and nothing outside it
notices. If your certificates are checked by external relying parties, the
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

**A certificate expired.** Issue a new one and revoke the old. There is no
renewal workflow.

**The certificate does not work as a server certificate.** By design — client
certificates carry `ClientAuth` extended key usage only, so one cannot be used
to impersonate a server.

**You want ACME, or a CA backed by a TPM or HSM.** Both are go-xkms, injected
through `WithCertProvider`. The interface is the boundary; qrdb never depends on
go-xkms directly.
