# Identity and RBAC — Architecture

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

Users, roles, the permission vocabulary, and the single policy decision point
that authorizes every request on every transport.

## Problem

Every capability in the store needs an answer to "may this caller do this?", on
six transports, for two dozen subsystems. Answering it in each subsystem
produces two dozen slightly different answers. Answering it against a
per-transport model produces six.

## Shape chosen

**One identity model, one permission vocabulary, one PDP.**

```
mTLS certificate  →  identity (CN=username, OU=tenant, SPKI thumbprint)
                  →  user record (RoleIDs + RoleBindings + Attributes)
                  →  RBAC PDP  →  allow / deny on a resource:action permission
```

- **Authentication is passwordless mTLS.** The certificate *is* the credential.
- **Permissions are `resource:action` strings**, registered in one global
  registry with categories for UI rendering.
- **The PDP is single.** Every transport handler resolves the same permission
  constant against the same decision point.

### Identity mapping

| Certificate field | User field | Role |
|---|---|---|
| Subject CN | `Username` | unique identity within a tenant |
| Subject OU | `TenantID` | tenant isolation boundary |
| SHA-256(SubjectPublicKeyInfo), base64url | `CertBindings[].Thumbprint` | **the authoritative mTLS identity** — `User.MatchesCert(cert)` |

**The thumbprint is computed over the public key, not the certificate.** A
renewed certificate for the same key produces the same thumbprint, so bindings
survive renewal — which is what makes certificate rotation an operation rather
than a re-enrolment.

### Three authorization inputs on one user

| Field | Model | Meaning |
|---|---|---|
| `RoleIDs` | RBAC, system-scoped | roles held globally |
| `RoleBindings` | RBAC, tenant-scoped | `{RoleID, TenantID}` — additive to `RoleIDs` |
| `Attributes` | ABAC | key/value pairs evaluated by policy |

Tenant-scoped bindings are the reason a control-plane user can be tenant-admin
for tenant X without being an admin of the system tenant.

### Alternatives rejected

- **Passwords.** Rejected outright — the user model is passwordless. There is no
  password field to leak, rotate or brute-force.
- **Binding to the full certificate by default.** Rejected as the default:
  `CertBindingSPKIThumbprint` is canonical precisely so renewal does not break
  the binding. `CertBindingFullCertFingerprint` exists for exact-certificate
  pinning where that is genuinely wanted, and is **not** the default.
- **Per-subsystem authorization.** Rejected — one PDP, one vocabulary.
- **Interpreting factor enrollment blobs.** Rejected: qrdb stores
  `FactorEnrollment.Data` and **never interprets it**. Verification belongs to
  the registered `authfactors.Factor`, which is go-xkms's.

## Built-in roles

Six roles are bootstrapped by `firstboot.Initialize` with deterministic UUIDv8
(SHA-256) identifiers, undeletable — see [../firstboot/](../firstboot/architecture.md) for
the permission matrix. The load-bearing one is the **Security Officer's explicit
denial of crypto operations**: governance and key use are different people, and
that is the FIPS 140-3 AS10.03 separation-of-duties control.

## Component boundaries

| Piece | Package | Owner |
|---|---|---|
| User model, cert bindings, role bindings, attributes | `pkg/user` | go-qrdb |
| Roles | `pkg/role` | go-qrdb |
| Permission vocabulary and registry | `pkg/permission` | go-qrdb |
| The RBAC PDP, ABAC, middleware, authenticators | `pkg/auth` | go-qrdb |
| Certificate issuance | `pkg/cert` — see [../certificate-authority/](../certificate-authority/) | go-qrdb |
| Auth factors framework and the mTLS factor | `pkg/authfactors` — see [../auth-factors/](../auth-factors/) | go-qrdb |
| FIDO2 / PIV / PKCS#11 factor implementations | — | **go-xkms**, injected |
| External auth backends (LDAP, PAM, TACACS+, RADIUS) | — | **go-auth**, if composed |

## Trust boundaries

- **The tenant comes from the certificate, never from the request.** This is the
  root of tenant isolation across every feature.
- **Enrolment bindings are SPKI thumbprints**, so possession of the private key
  is what authenticates.
- **`Enabled: false` blocks authentication** without deleting the record.
- **`System: true` users and the built-in roles cannot be deleted.**
- **Factor data is opaque.** qrdb never parses it, so a factor implementation
  cannot smuggle a decision past the PDP through its blob.

## What this feature deliberately does not do

- **No passwords.**
- **No hardware factor verification.** The `Factor` framework and the software
  mTLS factor are here; FIDO2, PIV and PKCS#11 come from go-xkms.
- **No external identity providers.** LDAP, PAM, TACACS+ and RADIUS are
  go-auth's, composed above.
- **No permission invention at runtime.** The vocabulary is a registry populated
  at `init()`; `Parse` validates against it.

## Companion documents

- [auth/README.md](../auth/README.md) — pluggable authentication interfaces and
  the default mTLS authenticator.
- [auth/roles.md](../auth/roles.md) — the built-in roles with their full
  permission matrix and the FIPS 140-3 AS10.03 separation-of-duties
  constraints.
- [auth/abac.md](../auth/abac.md) — attribute-based access control.
- [auth/layering.md](../auth/layering.md) — how the authorization layers compose.
- [auth/custom-authorizers.md](../auth/custom-authorizers.md) — replacing or
  extending the authorizer.
