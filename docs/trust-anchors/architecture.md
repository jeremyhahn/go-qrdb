# Trust Anchors — Architecture

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-06
> Conflicts with this document are defects. Fix them here first.

## Purpose

qrdb authenticates clients by certificate. A certificate is trusted at the
TLS layer if it chains to the cluster CA, a statically configured client CA,
or a tenant's own login CA (the per-tenant CA that signs CSRs). Trust anchors
add a fourth source: CA certificates a tenant registers so its own PKI can
issue its users' login certificates.

## The scoping rule

An anchor is trusted **for the tenant that registered it, and no other**.
Two layers enforce this together, and they are one object
(`pkg/clienttrust.Pool`):

1. **TLS layer.** The listener's client-auth pool is widened with every
   tenant's anchors, so a certificate chaining to any registered anchor
   completes the handshake.
2. **Issuer-tenant binding.** After the handshake, `VerifyIssuer` accepts an
   anchor-issued certificate only when one of the anchors in its verified
   chain belongs to the tenant the certificate claims. A certificate from
   tenant A's anchor that names tenant B is refused here, before any account
   lookup.

Login additionally requires the certificate's key to be bound to the account
(see [identity-rbac](../identity-rbac/architecture.md)); enrollment is how
that binding is made for an anchor-issued certificate.

## Components

```
            +---------------------------+
 API verbs  | service.CertEnrollment    |  AddTrustAnchors / List / Remove
 ---------> |   (tenant-self only)      |  EnrollCertificate (BYO cert)
            +-------------+-------------+
                          |
                          v
            +---------------------------+     change     +----------------------+
            | trustanchor.Registry      | -------------> | trustanchor.Changes  |
            |  (Observe wrapper)        |                +----------+-----------+
            +-------------+-------------+                           | Invalidate
                          | CABundle(ctx, tenantID)                 v
                          +---------------------------> +----------------------+
                                                         | clienttrust.Pool     |
                                                         |  TLS widening +      |
                                                         |  VerifyIssuer        |
                                                         +----------------------+
```

- **The verbs are confined to the caller's own tenant in the service.**
  `CertEnrollmentService.AddTrustAnchors`, `ListTrustAnchors` and
  `RemoveTrustAnchor` apply `tenant.Confine` themselves: an empty tenant is the
  caller's own, and any other -- the system tenant's caller included -- is
  refused with `*tenant.CrossTenantDeniedError` (403 / `PERMISSION_DENIED`).
  Every transport also checks `trust_anchors:*` (`ScopeSelfStrict`) against the
  named tenant in its handler, but confinement does not depend on a transport
  or a route remembering to: an anchor decides which certificates log in to
  its tenant, so one added to another tenant would mint logins there.
- **`trustanchor.Source`** — `CABundle(ctx, tenantID) ([]byte, error)`: one
  tenant's anchors as a PEM bundle. Every trust decision reads anchors through
  this one method. It is the shape of a CA bundle provider with the tenant
  made explicit, so an external trust store adapts to it directly.
- **`trustanchor.Registry`** — a `Source` a tenant admin can manage: `Add`,
  `List`, `Remove`. A store managed elsewhere answers `Add` and `Remove` with
  `ExternallyManagedError`.
- **Built-in default: `trustanchor.NewKVRegistry`.** Each tenant's anchors are
  records in the replicated system store, inside that tenant's identity
  namespace. They replicate with the cluster and are not node-local. They are
  deliberately outside the tenant's own barrier: an admin whose certificate
  chains to an anchor must be able to log in to unseal it.
- **Injection: `server.WithTrustAnchors(trustanchor.Registry)`** replaces the
  built-in store for an embedder with its own trust store. The SDK node
  builders forward it into the services before they are built; the embedded
  desktop binding has `EmbeddedServerBinding.SetTrustAnchors`.

## Snapshot and invalidation

The TLS handshake never reads the store: it loads a snapshot of the widened
client trust pool that a background loop on each node rebuilds every
2 seconds. Every successful `Add` or `Remove` is announced on the node's
client-trust change feed (a `trustanchor.Changes`), as is a tenant's login CA
coming into use for the first time on that node, and the announcement
schedules an immediate rebuild: a tenant onboarded a moment ago can log in as
soon as that rebuild lands on that node. Other nodes pick the change up at
their own next periodic rebuild. A removed anchor stops vouching at once
regardless, because the per-request issuer check (`VerifyIssuer`) reads the
tenant's anchors fresh. A failure to read anchors omits the anchors from the
snapshot and leaves the cluster CA, static CAs and tenant login CAs trusted.

## Enrollment of a bring-your-own certificate

`EnrollCertificate` (and tenant create with an admin certificate) verifies the
certificate before binding it:

1. it parses and permits client authentication;
2. its subject names the account (CN) and the tenant;
3. it chains, through any supplied intermediates, to one of **that tenant's**
   anchors at the current time;
4. its key's SPKI thumbprint is bound to the account, with the certificate's
   expiry.

The private key never reaches the node.

## Invariants

- An anchor never vouches across tenants.
- Anchor management is tenant-self only; an operator cannot add an anchor to a
  tenant it does not belong to.
- Removing an anchor schedules an immediate rebuild of the local trust
  snapshot, and the per-request issuer check refuses it at once.
- No anchor record is kept on a node's local disk.
