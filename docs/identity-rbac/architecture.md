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

### Reconciliation: the one sanctioned writer

A built-in role is immutable — `UpdateRole` refuses to replace its permission
set and `DeleteRole` refuses to remove it — so that an operator cannot widen
`auditor` or re-permission `custodian` by hand.

That invariant assumed nothing ever needed to rewrite a built-in's grants. The
assumption expired the first time a permission was **renamed**. Roles are copied
out of `BuiltInRoles()` when a tenant is provisioned and never touched again, so
a tenant created before the rename keeps the retired verbs forever, never gains
the live ones, and every write path that could repair it is closed: update is
refused, delete-and-reseed is refused, and create-over-the-top collides with the
name index. The symptom is a 403 on a live endpoint with nothing explaining it.

`RoleServicer.ReconcileBuiltInRoles(ctx, tenantID)` is the seam. It is **not** a
relaxation of the invariant, because its target is not arbitrary: it is
`BuiltInRoles()` itself, by construction the definition the guard exists to
protect. The caller chooses only WHICH tenant to converge, never what to
converge it to, and every other caller is still refused. The guard was never
about the write; it was about who chooses the value.

Three properties make it safe to run unattended:

- **Idempotent, and measurably so.** A built-in already in agreement is skipped
  entirely — no write, no `UpdatedAt` bump, no audit record. A no-op `Put` would
  still replicate a Raft entry on every boot of every node.
- **Bounded blast radius.** Only names `IsSystemRoleName` recognises are read or
  written. A custom role — including one an operator created to work around the
  drift this repairs — survives untouched.
- **Owner is repaired but never created.** Owner drifts for the same reason (its
  grant is `permission.All()`) and is equally immutable, so it is converged when
  the tenant already holds it. It is never seeded, because Owner is
  posture-conditional: creating it would hand a strict separation-of-duties
  deployment a superuser reconciliation was never asked to create.

**Timing is part of the contract.** `BuiltInRoles()` resolves its permission
sets through the global registry at call time, and a composing layer contributes
permissions when its modules register. A composer must therefore call this
AFTER module registration, or it will converge every tenant onto a definition
missing the modules' own verbs and then report success.

Drift is also visible **before** the repair runs: `Role.RetiredPermissions` is
derived on every read from the live registry and lists the stored permissions
the build no longer defines. It is never persisted — one marshal seam clears it
— so it can only ever report the current disagreement, and it is the field an
operator reads on a listing to see which tenants need converging.

## The canonical login key

Identity in qrdb has two handles, and they answer different questions.

`Username` is the tenant-scoped human name, and for a system identity it is the
only one — `admin` is not an address. `Email` is the **canonical login key**:
the handle every layer above this one resolves a principal by, from the WebAuthn
credential name through the session subject to the CLI's user identifier.

Before this existed, each layer above kept its own answer — go-xkms named
credentials from one field, go-quicbaas held an email in a side table beside the
qrdb record — and a login could verify a factor and then fail to find anybody it
belonged to. Owning the key here, and exporting it through the SDK, is what
makes those layers agree by construction rather than by convention.

The key is tenant-scoped, not global: the same person may legitimately hold an
account in two tenants, and every login path supplies the tenant alongside the
handle, so a global index would forbid that while buying nothing.

## Component boundaries

| Piece | Package | Owner |
|---|---|---|
| User model, lifecycle state, cert bindings, role bindings, group membership, attributes | `pkg/user` | go-qrdb |
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
- **`Lifecycle` is the single source of truth for whether an account may
  authenticate** — `pending` · `active` · `suspended` · `locked` · `disabled` ·
  `deleting`, with `Enabled` a derived projection (`Lifecycle == active`)
  maintained by the store. `User.CanAuthenticate()` is the check, and it fails
  closed on a state this build does not recognize.
- **`deleting` is terminal.** A deletion writes it before its first mutation,
  and from that instant no concurrent enable, and no credential ceremony
  finishing late, can make the account authenticable again. `DeleteUser` marks
  the record before its own cascade, and a terminal record accepts no mutation
  at all — not an update, not a login timestamp, and above all not a new
  certificate binding.
- **`AuthMethods` is a policy qrdb enforces for the authenticators it owns.**
  An empty policy states no restriction. Where the method is not observable to
  qrdb — a forwarded assertion, a capability module's own session token — the
  layer that ran the ceremony enforces it.
- **Group membership is stored, the Group entity is not owned.** `GroupIDs`
  holds opaque identifiers; a group's name and the roles it confers belong to
  the layer performing the authorization that consumes them — the same division
  qrdb already applies to `Attributes`.
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
- **No Group entity.** qrdb stores a user's `GroupIDs` — the membership edge —
  but owns no group catalog, no group CRUD and no group→role resolution. Roles
  live here because qrdb's own PDP evaluates them; a group is an indirection for
  assigning roles to many users, and the layer that owns the mapping is the one
  that resolves it. Adding a Group entity later would not change this field.

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
