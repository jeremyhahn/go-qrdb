# Firstboot and System Bootstrap — Architecture

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

Firstboot is the **admin bootstrap**: system tenant, built-in roles, the
platform admin user and the admin client certificate. It runs *after* the
[ceremony](../ceremony/), which is barrier custody only.

## Problem

A system that has just initialized its barrier still has nobody who can log in,
no roles to authorize against, and no tenant to put anything in. Creating those
in the wrong order — or creating an admin whose credential the server has never
heard of — leaves an operator locked out of the system they just installed.

## Shape chosen

Two bootstraps, run in order, each idempotent:

| Concern | Package | Function |
|---|---|---|
| Barrier custody (M-of-N split, custodian enrolment, share lifecycle) | `pkg/ceremony` | `ceremony.Initialize` |
| System tenant + built-in roles + platform admin user + admin client cert | `pkg/firstboot` | `firstboot.Initialize` |

Orchestration entry points live in `pkg/bootstrap`:

Admin enrollment is cert-based (CSR-sign or generate-and-distribute -> SPKI-thumbprint binding); there are no invite tokens. The control-plane (system) and data-plane (tenant) scopes are orchestrated through the `pkg/bootstrap` entry points:

- **System bootstrap** — system barrier + system tenant + system admin + admin-mTLS.
- **Provision tenant** — minimal control-plane tenant record + tenant-admin cert enrollment (no tenant barrier key held by the system admin).
- **Initialize tenant barrier** — tenant-admin self-service (authenticated): initialize THIS tenant's barrier and scope.


**Admin enrollment is certificate-based** — CSR-sign or generate-and-distribute,
binding an SPKI thumbprint. **There are no invite tokens.**

## Admin credential paths

`firstboot.Initialize` binds the admin's SPKI thumbprint by one of three
mutually-exclusive paths, selected by which `Dependencies` inputs it receives:

| Path | Input | What happens | Key custody |
|------|-------|--------------|-------------|
| **A** generate-and-distribute | `CA` | `CertProvider.GenerateAdminCert` issues a key + cert bundle | CA emits the bundle |
| **B** CSR-sign | `AdminCSRPEM` + `Signer` | `CertSigner.SignAdminCSR` signs the admin's CSR | Key never leaves the admin |
| **C** adopt-presented | `AdminCertPEM` + `AdminCertPool` | The certificate the admin **already holds** (PIV card, security key) is validated and adopted; nothing is issued | Key never leaves the operator's token |

Path C is checked **first**, because it is the only path that issues nothing: a deployment that presents a certificate has already decided which identity the admin authenticates as, so falling through to a configured CA would mint a second, unrelated credential and bind that instead — leaving the operator holding a certificate the server has never heard of.

**Path C is checked first**, because it is the only path that issues nothing.
A deployment presenting a certificate has already decided which identity the
admin authenticates as; falling through to a configured CA would mint a second,
unrelated credential and bind *that* instead — leaving the operator holding a
certificate the server has never heard of.

Refusals:

- `AdminCSRPEM` **and** `AdminCertPEM` together → `ConflictingAdminCertInputsError`. Signing the CSR would leave the presented certificate unbound; adopting the certificate would silently discard the CSR. Neither is choosable on the operator's behalf.
- `AdminCertPEM` without `AdminCertPool` → `MissingAdminCertPoolError`. An absent pool is refused rather than treated as "trust anything".
- Parse failure, expiry, or no chain to the pool → `AdminCertRejectedError`.

`AdminCertPool` is deliberately **not** the platform CA: a certificate the operator already holds was issued by something else (corporate PKI, card vendor), so validating against the CA this deployment is about to create would reject every legitimate input.

`AdminCertPool` is deliberately **not** the platform CA: a certificate the
operator already holds was issued by something else (corporate PKI, a card
vendor), so validating it against the CA this deployment is about to create
would reject every legitimate input.

This makes admin enrolment the mirror of `EnrollExternalOfficer` — the admin and
every security officer enrol by the same rule, and an operator who can bring
their own credential to one role can bring it to the other.

### Alternatives rejected

- **Invite tokens.** Rejected: a bearer token that grants admin is a bearer
  token that can be replayed. Enrolment is certificate-based throughout.
- **Falling back to the CA when a certificate is presented.** Rejected — that
  is the reason path C is evaluated first.
- **Treating a missing `AdminCertPool` as "trust anything".** Rejected:
  `MissingAdminCertPoolError`.
- **Choosing between a CSR and a presented certificate on the operator's
  behalf.** Rejected: `ConflictingAdminCertInputsError`. Signing the CSR would
  leave the presented certificate unbound; adopting the certificate would
  silently discard the CSR. Neither is choosable for them.
- **Non-deterministic role IDs.** Rejected: each built-in role gets a fixed
  UUID v5 derived from its name, so role identity is stable across clusters.

## Built-in roles

Created deterministically on first init, undeletable, persisted at
`__sys:role/<id>`, each with a fixed `uuid5(namespace, "go-qrdb/role/<Name>")`:

| Admin | `uuid5(namespace, "go-qrdb/role/Admin")` | `keys:*` (except delete infrastructure), `certificate:*`, `tenant:*`, `backup:*`, `audit:read` | Day-to-day key lifecycle |
| Operator | `uuid5(namespace, "go-qrdb/role/Operator")` | `keys:sign`, `keys:encrypt`, `keys:decrypt`, `keys:verify`, `barrier:seal`, `barrier:unseal`, `audit:read` | Routine operations |
| User | `uuid5(namespace, "go-qrdb/role/User")` | `keys:sign`, `keys:verify`, `keys:encrypt`, `keys:decrypt` | Crypto ops only |
| Auditor | `uuid5(namespace, "go-qrdb/role/Auditor")` | `audit:read`, `system:read`, `*:list`, `*:get-metadata` | Read-only everywhere |
| Custodian | `uuid5(namespace, "go-qrdb/role/Custodian")` | `barrier:unseal-share` | Shamir share holder only |

An admin user for the system tenant is also created:


The full permission matrix and the FIPS 140-3 AS10.03 separation-of-duties
constraints are in [../identity-rbac/](../identity-rbac/) and
[auth/roles.md](../auth/roles.md).

An `admin` user for the system tenant is created alongside, bound to the
`admin` role, once and idempotently.

## Startup sequence

1. Configuration loading
2. TLS certificate generation
3. Storage initialization
4. Raft initialization
5. Barrier initialization (if needed) — [../barrier-seal/](../barrier-seal/)
6. Role and user bootstrap — this feature
7. Multi-protocol servers start — [../api-server/](../api-server/)

## Component boundaries

| Piece | Package | Owner |
|---|---|---|
| Admin/tenant/role bootstrap | `pkg/firstboot` | go-qrdb |
| System / tenant orchestration entry points | `pkg/bootstrap` | go-qrdb |
| Barrier custody bootstrap | `pkg/ceremony` — see [../ceremony/](../ceremony/) | go-qrdb |
| Roles, users, permissions | `pkg/role`, `pkg/user`, `pkg/permission` — see [../identity-rbac/](../identity-rbac/) | go-qrdb |
| Certificate issuance | `pkg/cert` — see [../certificate-authority/](../certificate-authority/) | go-qrdb |

## Trust boundaries

- **The admin is bound by SPKI thumbprint**, not by a password or a token.
- **A presented certificate is validated against an operator-supplied pool**,
  never against the platform CA.
- **Only the leader may create.** In multi-node clusters followers detect
  `NotLeader` and wait for the leader's writes to replicate.
- **Sensitive keys live in the barrier.** After unseal they are available in
  memory through `seal.EncryptionBarrier.Decrypt()`.

## Idempotency

The bootstrap sequence is **idempotent** and safe to re-run:

- **Certificates** — If certs already exist, they are reused
- **Admin User** — If admin exists, creation is skipped
- **Barrier** — If sealed, unseal must be done manually; if unsealed, state remains
- **Raft** — If a node restarts, it re-joins the cluster using the stored log and snapshots

A fresh start vs. a restart of an existing node follows the same sequence; only the first run performs initialization, subsequent runs skip redundant operations.


A fresh start and a restart follow the same sequence; only the first run
performs initialization.

## What this feature deliberately does not do

- **No barrier custody.** That is the ceremony.
- **No invite tokens or password-based admin enrolment.**
- **No unsealing.** If the barrier is sealed, unsealing is a separate,
  deliberate operator action.
- **No role customization at bootstrap.** The six built-in roles are fixed;
  additional roles are created afterwards through
  [../identity-rbac/](../identity-rbac/).
