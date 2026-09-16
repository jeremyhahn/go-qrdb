# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog 1.1.0](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning 2.0.0](https://semver.org/spec/v2.0.0.html).

Pre-1.0, MINOR is where breaking changes live; PATCH is reserved for changes
that do not move the API. Releases before `0.2.0-alpha` predate this file and
are not reconstructed here — their history is in the git log.

## [Unreleased]

## [0.18.0-alpha] - 2026-09-16

MINOR, not PATCH. The release adds a permission family to the registry:
new vocabulary is additive for existing callers, but pre-1.0 MINOR is where
surface changes live (release-versioning convention §1), and a consumer that
gates escrow routes on these permissions cannot resolve them from an earlier
tag at all.

This release exists to carry `escrow:create`, `escrow:recover`, `escrow:list`
and `escrow:revoke` to go-xkms, whose escrow subsystem is implemented and
wired but cannot be exposed on any network transport until the permissions it
gates those routes with exist here.

### Added

- **A key-escrow permission family** (`escrow:create`, `escrow:recover`,
  `escrow:list`, `escrow:revoke`). go-xkms's escrow subsystem sends wrapped
  key material to an external custodian (KMIP or a federation agent) and
  reconstructs it back, and every key-material route it exposes is gated by a
  permission pair drawn from this registry — there was no escrow family for
  it to use. Classified `DutyGovernance` (key entry/output, the same duty as
  `keys:import`/`keys:export`, not a cryptographic primitive), scoped
  `ScopeSelf`, and granted in full to the Security Officer and Admin roles,
  with `escrow:list` (metadata only — never key material) also granted to
  Auditor. qrdb owns the vocabulary; it ships no escrow implementation of its
  own, matching the existing PIV/SSH/Passwords split.

## [0.17.0-alpha] - 2026-09-13

MINOR, not PATCH. Of the 99 commits on `main` between `v0.16.3-alpha` and this
release, 20 carry a breaking marker in their subject, and three more (#402,
#429, #485) change an exported interface or contract without one. Pre-1.0,
MINOR is where breakage lives (release-versioning convention §1).

This release exists to carry `pkg/secure/analyzer/scope.go` to consumers. At
`0.16.3-alpha` the analyzer's only non-test source file is `analyzer.go`, so
without the four scoping clauses it reports generated protobuf, `_test.go`
files and wire DTOs as rule-3 findings — a gate wired against the pinned
release is red for reasons no change in the consuming repository can fix.
`scope.go` landed in `6d0c17b` (#429, #450) and has been unreachable to
consumers ever since.

### Added

- **The MCP transport carries the guarded write verbs** (#127).
  `sdk/go/transport/mcp` now implements `transport.GuardedKVClient`, so a
  consumer's `client.KV().(transport.GuardedKVClient)` succeeds over MCP as it
  already did over embedded, gRPC, unix, QUIC and REST (#119/#124/#125/#126).
  Three new tools carry the verbs — `qrdb_put_if_absent`,
  `qrdb_compare_and_delete`, `qrdb_compare_and_swap` — each requiring the
  permission of the write it performs (`kv:put`, `kv:delete`, `kv:put`), each
  taking its values base64-encoded, and each answering with a single-field JSON
  object (`{"stored": …}`, `{"deleted": …}`, `{"swapped": …}`) that mirrors the
  REST response bodies. A refused guard is a SUCCESSFUL tool result carrying
  false, never a tool error: losing the race is the normal outcome of a claim.

  Every handler makes exactly ONE call, to `service.KVServicer`'s matching
  atomic method — no `Get` before it, no read-then-write fallback after it —
  and that is asserted rather than asserted-in-prose. The MCP transport gets
  the guarded conformance matrix's four full-passage cells and the
  cross-transport contention suite's MCP row (`sdk/go/conformance`), plus a
  delegation test that counts which service methods the handler reached.
  Verified by mutation: replacing the single `PutIfAbsent` call with a `Get`
  followed by a `Put` leaves every sequential assertion green and turns both
  the delegation counters and the contention suite red.

  A client with the verbs talking to an MCP server without the tools gets
  `transport.CapabilityUnsupportedError`, matching the version-skew half of the
  contract the other transports carry. The classification is by JSON-RPC error
  CODE, not message text.

  **Known limit:** MCP does not walk the matrix's four typed-refusal cells,
  which require a transport that can present the multi-key transaction plane's
  capability refusal *by type*. MCP reports a service-layer failure as a tool
  result carrying a message string, so that refusal reaches the client as
  prose; admitting it would mean loosening `classifyRefusal` from a type
  assertion back to a substring match. Excluded and named
  (`transportKind.carriesTypedCapabilityRefusal`) rather than papered over;
  #169's error-typing sweep owns that surface.

- **Rate limiting is centralized in the auth middleware** (#201, #394), one
  limiter rather than a decision each handler made for itself.
- **Typed lease not-found and lease-expired across gRPC, unix, MCP and QUIC**
  (#385), so the condition is carried rather than reconstructed from prose.
- **`pkg/secure/analyzer` is armed as a build gate, with rule 3 scoped to
  where it can be satisfied** (#429, #450) — `scope.go`'s four clauses exempt
  generated protobuf, `_test.go` files, flag targets and wire DTOs, which are
  not holders of key material and cannot satisfy the rule. **This is the change
  this release exists to publish.**
- **A Trivy reporting floor below the blocking one** (#522). `make trivy`
  gated on `CRITICAL,HIGH` and that was the only severity list in the
  repository, so no leg could report a MEDIUM and a green Security signal
  could not distinguish "no MEDIUMs" from "nothing looked". Two real
  advisories, both fixed upstream, sat under that floor for the whole of the
  repository's history. The blocking floor does not move; `make trivy-report`
  scans `MEDIUM,LOW` as its own nightly step after the gate, exits nonzero on
  a finding so the signal can go red, gates nothing, and prints `UNKNOWN`
  without failing on it. `test/buildgates/trivy_severity_test.go` pins the
  split by parsing the Makefile and nightly workflow: every severity band is
  named by some leg, the gate names nothing below HIGH, the report leg is
  unreachable from `security`/`ci`/`_ci` transitively, and nightly runs the
  gate then the report. A local review of the first cut found the gate passed
  with the report leg commented out, made transitively blocking, or unplugged
  from nightly, and fixing those found a fourth: a column-0 make comment
  between recipe lines truncated the parse. All four are watched reds.
- **The FIPS document claims only what was measured** (#523). Nightly run
  17302 on `133fa56` is the first *scheduled* `fips140=only` run on `main`
  ever to pass, after twenty-two consecutive failures: root `make test` 126
  packages pass / 0 fail, `sdk/go` `make test-sdk` 18 / 0, with zero
  GCM-arbitrary-IV, X25519, `crypto/sha1` and short-key HMAC rejections.
  `docs/seal/fips140-mode.md` states step by step what that run executed,
  pins the figures to that commit with a statement that the tree has moved
  since, and **withdraws** the earlier claim that `go build ./...` was clean
  for `sdk/go`: the job does not run that step, so the SDK is compiled only
  as far as `go test` compiles it. Arming the job as a required check now
  waits on a stability record, fourteen consecutive green scheduled runs,
  rather than on preconditions that are met.

- **A red nightly opens a tracking issue, a still-red one rewrites it in place,
  and a green one closes it** (#478, #525).
- **`docs/coordination/`** (#173, #404) — the five-document feature folder the
  coordination surface never had.

### Changed — BREAKING

- **A refusal travels in the result payload, and no longer halts the shard**
  (#520, #527). Raft treats any non-nil error out of `Update` as a fatal apply
  failure: `lastApplied` stops advancing, and every subsequent linearizable
  read on that shard — on any engine sharing it, on keys written and read
  successfully microseconds earlier — waits out the caller's deadline and
  reports a consensus timeout. The MVCC engine had **no** apply classification
  at all, and the time-series stub could not say a refusal was a refusal, so
  one refused time-series write cost the shard its reads. Measured, not
  theorised, on both engines (#129 on KV, #520 on MVCC).

  `statemachine.ResolveApplyError` is now THE classification seam for the
  repository, and every `Update` in the tree routes through it. **The error
  declares which it is**: an error implementing `protocol.CapabilityError` —
  an `error` with a `Capability() string` method naming the absent verb or
  subsystem — is a refusal and is encoded into `Result.Data` with a capability
  marker, which the service layer turns into a typed
  `CapabilityUnsupportedError` for the caller. Everything else is a fault and
  still halts Apply. Silence means fatal; only a declaration means benign, so a
  forgotten marker costs availability on one shard loudly rather than
  swallowing a storage fault and serving diverged state silently.

  `test/buildgates` finds every method in `pkg/` taking `[]sm.Entry` and
  requires it to route through `ResolveApplyError` or to be listed, with a
  reason, as a delegator — default-deny, because the absence of a call is not
  something a reviewer reading one diff can see. A refused batch or transaction
  now stages nothing: opcodes are validated before any operation in the set is
  applied. **Reachable by any client able to propose an opcode this state
  machine does not implement.**

- **The MCP transport carries seventeen JSON-RPC error codes, and the
  substring classification is deleted** (#438). `pkg/api/mcp` had **no**
  error-code mapping at all — a tool failure reached a client as a sentence —
  so every typed condition the SDK promises degraded to matching words in
  prose. `sdk/go/transport/mcp` classified four conditions with
  `strings.Contains(msg, "not found")` and one with `"lease expired"`, which is
  fail-open in the direction that costs most: a transport failure whose text
  happened to carry the phrase read as a successful determination of absence.

  The new `pkg/api/mcp/wire` package declares one code per gRPC status code,
  by MEANING, exactly as the QUIC wire does since #437 — so a consumer keeps
  one mental model across transports instead of acquiring a second. The
  numbers are JSON-RPC's, from the −32000..−32099 band reserved for
  implementation-defined server errors, and three of them (−32002, −32005,
  −32010) are deliberately left unassigned to the names the portfolio's error
  convention already gives them.

  | condition | code |
  |---|---|
  | not found (key, user, role, lease, revision) | `NOT_FOUND` (−32004) |
  | barrier sealed, store closed, not leader, draining | `UNAVAILABLE` (−32006) |
  | capability this node was not built with | `UNIMPLEMENTED` (−32014) |
  | throttled caller | `RESOURCE_EXHAUSTED` (−32008) |
  | unclassified server fault | `INTERNAL` (−32603) |

  **What changes on the wire.** A service-layer failure used to leave as a
  JSON-RPC error pinned to `INTERNAL_ERROR`; it is now a tool RESULT carrying
  `isError`, which is what the MCP specification asks for and the only envelope
  a tool handler controls. The text content is byte-identical to what it was,
  so a client that only reads text sees no difference; the code rides in
  `structuredContent` as `{"error": {"code", "status", "message", "tool"}}`.

  **What changes in the SDK.** The nine single-subject user methods return
  `*transport.UserNotFoundError` naming the id or key **the caller supplied**,
  where they previously returned `*transport.KeyNotFoundError` (four of them)
  or a generic request error (five). `transport.RequestError.StatusCode` now
  carries the JSON-RPC code on MCP. An unrecognised code from a newer server
  degrades to the generic request error, never to a misclassification.

  The server owns no classification clauses of its own: it consults the gRPC
  transport's per-family classifiers through the new
  `grpc.ServiceErrorCode` and renders the result, so a condition cannot mean
  one thing over gRPC and another over MCP.

  One message test remains, behind a code gate: the lease expired/absent
  split, which no transport in this repository can separate by code because
  gRPC maps both to `NOT_FOUND`. It fires only once the code has already said
  not-found; it is not the ungated classification this change deleted.

- **Two barrier-passphrase config fields are `*secure.Buffer`, and an unusable
  one is now an error rather than a downgrade** (#429). Arming
  `pkg/secure/analyzer` as a build gate left exactly two findings that were
  neither a wire DTO, a generated struct, a flag target nor a test fixture —
  two Go config structs that a caller fills from its own key source and that
  live as long as the process they configure. Those are *holders* of key
  material, which is the population rule 3 exists for, so both changed type:

  | surface | before | after |
  |---|---|---|
  | `bootstrap.SystemConfig.Passphrase`, re-exported as `sdk.BootstrapSystemConfig` | `string` | `*secure.Buffer` |
  | `embedded.BarrierConfig.Passphrase` (`sdk/go/transport/embedded`) | `[]byte` | `*secure.Buffer` |

  Use `secure.NewBufferFromBytes`; `sdk/go/secure` re-exports the type and its
  constructors, so no consumer needs the internal `pkg/secure` path. The
  caller keeps ownership — neither `BootstrapSystem` nor `NewServer` destroys
  the buffer; each copies out of it once, inside a `WithBytes` borrow, at the
  point the material becomes the `seal.Credentials` the barrier demands.
  `string` cannot be zeroized at all and `[]byte` only by the caller's own
  discipline; a `Buffer` is mlocked, canary-guarded and wiped on `Destroy`.

  **A `*secure.Buffer` has a LIFECYCLE that neither previous type had, and
  that is a behaviour change, not only a type change.** `Buffer.WithBytes`
  returns the same `*NilBufferError` for a nil receiver, a destroyed buffer
  and a zero value, so the obvious single error check reads all three as "no
  passphrase configured". For `embedded.BarrierConfig` that is not a wrong
  error message, it is a **wrong security posture**: on a fresh data dir the
  resulting `seal.StrategyNone` barrier initializes successfully, `IsSealed()`
  is false, `encfs` wraps the Pebble data plane with a root key sealed by
  nothing, and an embedder that asked for a passphrase barrier gets plaintext
  at rest, permanently, with nothing said about it. The two cases are
  therefore separated:

  - `Passphrase == nil` — **not configured**. Unchanged behaviour: with
    `StrategyPassphrase` or the empty default the embedded transport comes up
    with no barrier, which go-quicraft announces at initialize (`barrier
    performs NO encryption`).
  - `Passphrase != nil` but unreadable — **a configuration error**.
    `NewServer` fails with the new `*BarrierPassphraseError` and does not
    fall back. Destroying the buffer after one server and constructing another
    is exactly what the ownership rule above invites, and it is the sequence
    that must fail loudly.

  Covered by four shapes in both directions — nil, destroyed, zero value,
  live — in `sdk/go/transport/embedded/barrier_passphrase_test.go`. Three of
  them are red without the guard; the live case additionally asserts the
  written value is not on disk in the clear. `pkg/bootstrap` keeps treating an
  unreadable buffer as `*MissingPassphraseError`, which is the error that
  branch already returned when the field was a `string`, and no barrier is
  weakened there.

  **Also corrected in the same change:** `BarrierConfig.Passphrase`'s doc
  claimed that with no passphrase `NewServer` "falls back to the deterministic
  bootstrap passphrase keyed by deployment ID 0". No such derivation exists
  anywhere in `sdk/go` — the only thing resembling it is a string a test
  builds and passes to `BarrierInitialize` explicitly — and the code ten lines
  into `setupBarrier` says so itself: "there is no deployment-derived
  passphrase". On a public SDK field whose doc is the only thing an embedder
  reads, the gap between "derives a passphrase" and "comes up with no barrier"
  is the gap between encrypted and not.

  **Consumer verification (pre-publication exception,
  `conventions/release-versioning.md`):** inside this repository the changed
  fields are referenced only by `cmd/qrdbd`, `sdk/go/types_bootstrap.go` and
  the two packages' own tests, all updated here. go-xkms and go-quicbaas were
  not checked out on the host that made this change and could not be grepped;
  both are filed rather than assumed — go-xkms#550 and go-quicbaas#830. Not a
  zero-consumer change and not claimed as one.

- **Barrier credentials are wipeable bytes at every layer** (#184). Every field
  that carries a caller-supplied barrier credential changed from `string` to
  `[]byte`: `BarrierInitializeRequest.Secret`, `BarrierUnsealRequest.Secret`,
  `BarrierInitializeShamirRequest.Secret`,
  `BarrierEnableEncryptionRequest.Passphrase` and
  `TenantBarrierInitRequest.Passphrase` in `sdk/go/transport`; the matching
  `secret` / `passphrase` fields in `qrdb.proto` (`string` → `bytes`); the
  `secret` / `passphrase` parameters on `service.BarrierServicer` and
  `tenant.TenantServicer`; and the REST and QUIC JSON request structs on both
  the client and the server.

  A Go `string` is immutable and therefore **cannot be zeroized**. A consumer
  holding a passphrase in protected memory had to convert it to a `string` to
  populate the request, and that copy then lived on the heap until the
  collector happened to reuse the page — recoverable from a core dump or a
  memory scrape for an unbounded period. The field type was the only thing
  forcing that, and `TenantBarrierUnsealRequest.Key` was already `[]byte`, so
  the fix is to make the outliers match rather than to add anything.

  The type change makes the credential *wipeable*; the rest of the change
  makes it *wiped*. The caller owns the slice it supplies and may zeroize it
  the moment the call returns, because no transport, service or sealing
  strategy retains it.

  **What "retains nothing" does and does not cover.** Wiping the slice destroys
  that slice and every alias of it. It does not reach the transient copy each
  encoder necessarily makes — base64 in a REST body, protobuf's out-of-place
  marshalling, the MCP JSON-RPC body. Those are unreferenced once the call
  completes and the collector reclaims them, but they are not zeroized, and
  nothing at this layer can zeroize them: serializing a value means copying it
  and the wire format owns that copy. The claim is that the copy the CALLER
  holds and the copy the DAEMON decodes are both destroyable on demand, which
  a string never was — not that no plaintext exists anywhere. The daemon owns the slice its decoder produced and
  `secure.Wipe`s it before the handler returns — on the success path and on
  every error path, including a rejected unseal, which is the case that
  matters most. `enable-encryption` is the one deliberate exception: its
  migration outlives the request, so ownership passes to that goroutine and
  the wipe is deferred there instead.

  **Wire format.** proto3 encodes `bytes` and `string` identically, so gRPC,
  Unix and QUIC protobuf frames are byte-for-byte unchanged. REST JSON and the
  QUIC tenant JSON frame now carry these fields as **base64**, the same
  convention every other `[]byte` field on those surfaces already uses. The
  TypeScript SDK encodes and decodes this for its callers; its own API is
  unchanged and still takes plaintext.

  **MCP is the documented exception.** An MCP tool argument is a JSON string
  by definition of the protocol, so `pkg/api/mcp.secretArg` and
  `sdk/go/transport/mcp.secretArg` are single, commented conversion points on
  each side. Callers who cannot accept that residue should not drive barrier
  operations over MCP.

  **Not covered:** Shamir share fields and SO/User PINs are still text strings
  on every transport. They are secrets too; the gap is stated in
  `docs/barrier-seal/api.md` rather than left implied.

  **Consumer verification (pre-publication exception,
  `conventions/release-versioning.md`):** inside this repository the changed
  types are referenced only by `sdk/go/transport/*`, `pkg/api/*`, `pkg/service`,
  `pkg/tenant` and `cmd/qrdb`, all updated here. go-xkms, go-quicbaas and
  go-trusted-platform were **not checked out on the host that made this
  change** and could not be grepped. go-quicbaas is a known consumer — it is
  the repository that raised this issue — and will need a mechanical update at
  its call sites, after which it can delete the `passphraseString` conversion
  it documents as residue today. This is not a zero-consumer change and is not
  claimed as one.

- **`RevokeCert` reports what it did, on every transport** (#287).
  `cert.CertProvider.RevokeCert`, `transport.CertClient.RevokeCert` (`sdk/go`)
  and the gRPC/Unix `RevokeCert` RPC now return a result alongside the error:
  `(*cert.RevokeResult, error)` in `pkg/cert`, `(*transport.RevokeResult,
  error)` in the SDK, `RevokeCertResponse{record_found}` on the wire (the RPC
  previously returned `google.protobuf.Empty`). `RevokeResult.RecordFound`
  (a projection of `RevokeOutcome`) distinguishes a certificate this node
  holds a `CertRecord` for from a deny-by-serial revocation of one it does
  not — the certificate may have been issued by a peer, or its record
  pruned. Both are successes: the revocation marker is written
  unconditionally, before the outcome is even determined, so a mistyped
  serial was previously indistinguishable from a deliberate revocation of a
  peer-issued certificate. Reporting that distinction as an error would have
  misreported the deliberate case — the one deny-by-serial exists to
  protect — as a failure. `qrdb ca revoke` / `qrdb certs revoke` now prints
  which case occurred.

  **Consumer verification (pre-publication exception,
  `conventions/release-versioning.md`):** `RevokeCert` and `ExportPKCS12` are
  referenced only inside go-qrdb's own `pkg/cert`, `pkg/api/*` and
  `sdk/go/transport/*`. go-xkms, go-quicbaas and go-trusted-platform were not
  checked out on the host that made this change; go-trusted-ca matches on
  the method name but has no go-qrdb dependency in its `go.mod`, so its
  `RevokeCert` is an unrelated API, not a consumer of this one. Zero verified
  external consumers at the time of this change.

- **`/certs/*` is gated on the tenant the REQUEST names, not the caller's**
  (#474, #506). `certs:generate` took its `tenantID` from the request body
  unchecked and wrote it into the certificate's Subject OU, which the mTLS
  authenticator reads as the identity's tenant — so a caller could mint a
  CA-signed certificate that authenticates as another tenant. Reachable on four
  transports.
- **The QUIC wire carries gRPC's seventeen conditions, not six** (#437, #461),
  by meaning rather than by message text; four lanes were blocked on the
  missing vocabulary.
- **`LockServicer` resolves per (tenant, database)** (#455, #487). It was a
  process singleton over pre-namespace engines, so two tenants sharing a shard
  under `prefix` isolation took the same lock, and `List`/`Status` enumerated
  each other's keys and holder ids.
- **Collection documents are (tenant, database)-namespaced** (#441, #454), so
  the collection service resolves like its twelve siblings.
- **Auth factors are per-tenant** (#393, #463), routed through
  `tenant.StoreResolver`.
- **The SO-PIN gate fails closed** on the same lever as the authorisation gate
  (#445, #471).
- **A deleted tenant id is retired, not released** (#413, #468).
- **`DROP DATABASE` reclaims the keyspace**, not just the directory (#412,
  #458).
- **`DropCollection` reclaims the collection's ID sequence** (#411, #449) —
  `SequenceServicer` gains a delete verb.
- **An explicit `_id` reserves the counter, and an insert never overwrites
  silently** (#447, #465).
- **A collection table honours the `_id` it declares as its primary key**
  (#467, #475). `REPLACE INTO` never replaced, `ON DUPLICATE KEY UPDATE` never
  updated, and `UPDATE ... SET _id` destroyed the document.
- **`Emit` reports whether the record landed, and an unrecordable
  authorisation decision is refused** (#335, #443).
- **An evicted entity watch says why** (#496, #501) — a terminal variant on
  `WatchEntityEvent`, and the one consumer that would have dropped it.
- **A revoked peer certificate no longer authorizes on the cluster key plane**
  (#285, #390).
- **`gosec`'s `-tags` is comma-separated** (#295, #397); the gate had been
  scanning the untagged tree.

- **A cert binding can be withdrawn** (#518). `AddCertBinding` shipped on all
  six transports and nothing removed a binding on any of them, so a binding
  written today could not be withdrawn through any API. Since #505 made the
  binding set what decides an account's mTLS admission, an enrolled key was an
  irrevocable *credential* rather than a stale record — and revocation is not
  the remedy, because a binding is over the `SubjectPublicKeyInfo` and outlives
  every certificate issued for that key, so a re-issue for the same key
  authenticates again. The exploitability is bounded (re-issuance needs
  `certs:generate`, gated on the target tenant by #506); what it cost was an
  operator who could not clean up after a key compromise.

  `RemoveCertBinding(userID, thumbprint)` is now on all six transports —
  `DELETE /users/{id}/cert-bindings/{thumbprint}`, the `RemoveCertBinding` RPC
  (gRPC and unix), `user.RemoveCertBinding` (QUIC),
  `qrdb_user_remove_cert_binding` (MCP) and `Client.Users()` (embedded) — and in
  the TypeScript SDK. It removes the record entry, the tenant-scoped SPKI index
  entry and the legacy un-scoped one.

  **A narrow verb, not a whole-set replacement.** `UpdateUserRequest.CertBindings`
  already replaced the set and maintained the index; exposing *that* over a
  transport would make a caller's snapshot of the rest of the set part of every
  withdrawal, and committing a snapshot is how a concurrent enrolment
  resurrects a binding that was just withdrawn.

  **`users:remove_cert_binding` is a separate permission**, not folded into
  `users:add_cert_binding`. Enrolment only narrows an account; withdrawing its
  LAST binding widens it back to admitting any certificate whose CN and OU name
  it. Both permissions now declare `ScopeSelf` explicitly rather than inheriting
  it, because `LookupScope` returns `ScopeSelf` for anything nobody tagged and
  these two decide which certificates authenticate as an account.

  **Revocation still does not drop a binding, on purpose.** Revoking during a
  routine rotation would otherwise unenrol the key being rotated. The two are
  separate operations answering separate questions, and an operator cleaning up
  after a key compromise performs both.

- **`ServiceModule` gains `RegisterQUIC`** (#402). A fourth registration hook
  beside `RegisterGRPC` (which serves gRPC and unix), `RegisterHTTP` and
  `RegisterMCP`, so a module registered once reaches every network transport
  instead of every one but QUIC. A type that implements the interface directly
  rather than embedding `BaseModule` must add the method.

- **A first node can advertise cluster enrollment under `GODEBUG=fips140=only`**
  (#485). Noise_NK_25519_ChaChaPoly_SHA256 performs an X25519 handshake, which
  is not a FIPS 140-3 approved key-agreement scheme, so `crypto/ecdh` refuses
  it. `clusterenroll.StartAdvertiser` returned that refusal as an error and the
  node failed to start: **cluster enrollment did not degrade in FIPS mode, it
  did not start.** The nightly `FIPS 140-3` job had reported this correctly for
  eleven consecutive runs.

  Noise is now excluded rather than attempted, on `crypto/fips140.Enabled()` —
  `Enabled()`, not `Enforced()`, matching the call `pkg/ceremony` makes for an
  Ed25519 external officer, because a deployment that asked for FIPS mode
  should not be handed non-approved cryptography just because `fips140=on`
  tolerates it. The ruling splits on the operator's stated intent:

  - **A first node** excludes Noise and comes up advertising the approved
    methods. It never chose a transport — there is no advertise-method option —
    and SPKI-pinned TLS, DANE and direct HTTPS all fetch the same CA bundle
    from `/v1/ca/bootstrap`. Refusing to boot would mean no multi-node cluster
    under FIPS at all, over a method a FIPS deployment could not use.
  - **A joining node that named Noise alongside an approved method** has Noise
    dropped from the truststrap method order; `AutoFetch` is a fallback chain by
    construction, so a preference list is what was given.
  - **A joining node that named Noise and nothing else** is refused at
    configuration time with `*clusterenroll.NoiseFIPSUnavailableError`, before
    the first dial and before anything reaches the data directory.

  **No downgrade is silent.** Both exclusions log at WARN naming FIPS, the
  method dropped and the approved methods to use instead, and the refusal's
  message leads with FIPS. That matters for the message as much as the
  behaviour: go-truststrap wraps a FIPS refusal in its `ErrInvalidKeySize`, so
  what an operator used to see was `noise: invalid key size: crypto/ecdh: use
  of X25519 ...` — a leading clause that sends them after a key-length bug that
  is not there. qrdb now refuses above that call, so the string is unreachable
  from a FIPS deployment; the mis-wrapping is filed upstream.

  **BREAKING, in the sense that matters here: a non-`nil` `Advertiser` now
  means "this node advertises enrollment", not "a Noise listener is running."**
  `cmd/qrdbd` registers `/v1/ca/bootstrap` on exactly that condition, and
  collapsing the two again would leave a FIPS first node unreachable by *every*
  method rather than by one — silently, which is the shape go-qrdb#241 exists to
  keep out of this package. Ask the new `(*Advertiser).NoiseEnabled()` when the
  Noise listener specifically is the question. `clusterenroll.NoiseAvailable()`
  and `*NoiseFIPSUnavailableError` are exported and re-exported from
  `sdk/go/clusterenroll`.

  No test was skipped to get there, and the ruling is asserted in BOTH runtimes:
  `noiseAvailable` is an overridable variable, as `readFileFn` already is, and
  method selection was split into a `buildAutoConfig` that performs no
  cryptography — which is what lets "a non-FIPS node still selects Noise" run
  under `fips140=only` as well as under `fips140=off`. Where a live Noise
  listener became genuinely unreachable, the test asserts the contract that
  replaces one rather than skipping, so a regression that resumed reading
  `noise-key.hex` in FIPS mode fails it.

  Measured, Go 1.26.6, `GOFIPS140=latest GODEBUG=fips140=only make test`:
  **124 pass / 2 fail** against 120/2 before, with X25519 rejections at **0**
  from 15. `pkg/clusterenroll` and `cmd/qrdbd` are green. The two survivors are
  separate pre-existing defects, both filed: `pkg/tenant` has a `cipher.NewGCM`
  test fixture, and `pkg/service`'s test binary does not compile at all (which
  is not a FIPS failure — it fails with FIPS off too). See
  `docs/seal/fips140-mode.md` and `docs/clustering/`. **Both survivors were
  closed later in this release**: the `pkg/tenant` fixture now uses an AEAD
  FIPS admits (#511), and `pkg/service`'s test double gained the `PutIfAbsent`
  its interface requires (#502).

### Changed

- `AGENTS.md` holds the rules and `CLAUDE.md` is the symlink (#376) — a
  symlink is a mode-120000 blob in git, so every plumbing reader of the old
  arrangement got a nine-byte path instead of the rules, silently and with exit
  status 0.
- `vet-tags` is armed in CI (#386). The recorded blocker was an untidy
  `go.mod`; the measured blocker was a missing tag on the
  `sdk/go/gorm/conformance` leg.
- `sdk/go` is under the linter it had never been under (#364).
- **A project's workflows run CI and nothing else.** The label-triggered
  review and auto-merge workflows this range added and iterated (`v7` through
  `v26`) are removed again: QuicBaaS is the sole orchestrator, and a review
  engine duplicated into every repository was the defect, since a fix reached
  one copy and silently missed the rest. Known gap, accepted by the owner:
  until QuicBaaS can post a verdict, no automated review runs here and merges
  need a human.
- **Exported interface methods with no non-test caller are enumerated and
  gated** (#271, #401): `test/tools/ifacesweep` records the findings per tag
  set, and CI refuses a new one.
- Build hygiene: module credentials point at the forge's new address and are
  written once, not three times (#363); `Dockerfile.gpu` names its final
  stage so the GPU integration lane can build (#365); the devcontainer stops
  rebuilding its 6.5GB image on every source edit (#294, #389).

### Fixed

- **A cert-binding write no longer commits a stale set** (#518).
  `AddCertBinding` read the record and then called `UpdateUser`, which read it
  again and guarded on ITS read — so the window between the two reads was
  unguarded, and an enrolment that read the set before a concurrent withdrawal
  committed would swap its stale set in cleanly afterwards, putting the
  withdrawn credential back. Both binding verbs now commit against the bytes
  they computed from (`updateUserAt`), which makes that a LOST swap, and retry
  the lost swap against whatever won — correct because "add T" and "remove T"
  say what to do to a set without depending on what the caller last saw it
  contain. Exhausting the bound reports `*ConcurrentUpdateError`, because an
  operator withdrawing a compromised key must never read a success that did not
  happen.

- **`secure.StdinProvider` no longer leaves the passphrase in an unwipeable
  heap buffer** (#184). It read through a `bufio.Reader`, which staged the
  passphrase in an ordinary heap allocation that was never wiped — in the one
  provider whose entire purpose is to keep passphrases out of unprotected
  memory. It now reads a bounded line one byte at a time straight into an
  mlocked `Buffer`, which is destroyed (and so zeroed) on every exit path
  including the error ones. Two behaviour changes follow: the provider no
  longer **over-reads** past the newline, so a subsequent reader of the same
  stream still sees the remainder; and input longer than
  `secure.MaxStdinPassphraseLen` (1024) is **refused** rather than truncated,
  because silently accepting a prefix would unseal against a credential the
  operator did not type.

- **`ExportPKCS12` refuses a revoked certificate** (#287). It parses the
  supplied `certPEM`, derives the serial with `FormatSerial` (the single
  definition of that format), and refuses with `*CertRevokedError` when
  `IsRevoked` says the serial is revoked. There is no override flag: a
  forensic or archival export of a revoked bundle needs that added
  deliberately, not defaulted. This is an OPERATOR SIGNAL, not a containment
  control — the caller already supplies `certPEM`/`keyPEM`, so refusal
  withholds nothing it did not already have; its value is that the system
  stops silently packaging a credential it will not honour anywhere in the
  cluster. Gives `cert.CertRevokedError` its first producer: both REST (409)
  and gRPC (`FAILED_PRECONDITION`) mappings were previously unreachable
  except through a mock.

- **Teardown is fenced** (#483, #488). `DeleteTenant` makes the retirement
  durable before the irreversible shred, so a failed tombstone write no longer
  leaves a live tenant whose data is permanently undecryptable. `DROP DATABASE`
  stops deleting other tenants' unattributable collection records (#481). The
  fixture uses an AEAD FIPS admits (#511).
- **`StandaloneBackend` is an `AtomicBackend`** (#466, #490), so the collection
  insert is a guarded write and `PutIfAbsent` works on the dedicated-store
  path.
- **The audit chain spans a rotation, and the verifier knows it** (#452, #476).
- **A cancelled watch releases its goroutine, and a watch that ended for a
  reason says so** (#405, #406, #494) — on the embedded transport and the
  namespaced wrapper. The last two sites of the same pattern closed in #515: a
  refused start revision keeps its type (#495), and a cancelled namespaced
  watch releases its goroutines (#499).
- **Secondary-index maintenance is scoped to the keys an index owns** (#410,
  #448).
- **A closed PebbleDB store returns `ErrStorageClosed` instead of panicking
  `pebble: closed`** (#371, #435).
- **`DropCollection` drops the indexes `CreateCollection` built** (#399, #439).
- **`qrdb_watch` stops crediting its cursor from a replay it never delivered**
  (#388).
- **`pkg/discovery` compiles on darwin** (#359) — the `socket(2)` flags move
  behind a platform seam.
- **`secure` holds the borrow across seven traced reads, and the deadlock is
  loud** (#74, #427).
- **`sdk/go/node` resolves the scoped encryption router's tenant leg lazily**
  (#384).

### Security

- **Cross-tenant certificate issuance is closed** (#474, #506) — see
  `/certs/*` above. A caller could mint a CA-signed certificate that
  authenticates as another tenant, on four transports.
- **An enrolled account's mTLS login is decided by its SPKI binding**, not by
  the certificate's CN and OU (#505, #516). Fail-closed per account: an account
  that has enrolled a binding is refused a certificate that does not match it;
  an account with none authenticates as before.
- **`FinishLogin` resolves the factor on the login session's tenant, not the
  caller's `ctx`** (#303, #392).
- **Every request body is bounded** on the REST plane (#378), and a comment
  claiming a bound six handlers lacked is gone.
- **Every workflow action is pinned by commit digest, and it is gated** (#367,
  #407). A tag is a ref its owner can move, and at the time the review job
  held a write-scoped token; that job is gone (see Changed), the pin gate
  stays.
- **Volume-pool paths are validated before they are created** (#292, #366),
  from one shared guard: absolute and canonical, no symlink, and no overlap
  with another candidate in the same request. The request type is
  wire-shaped, so the guard lands before the change that makes it reachable
  from a transport. `gosec` cannot see this sink: G304 covers opens, not
  directory creation.
- **The module-fetch token never touches disk** (#326, #368). The docker
  build's Gitea credentials reach `go mod download` as a BuildKit secret
  sourced from the environment, not from a temp file an EXIT trap was
  supposed to remove and did not on SIGKILL.
- **`gosec` scans the tag-gated files and the `sdk` module** (#377), and the
  91 test-file findings are triaged with the two measured reasons `-tests`
  still cannot go on recorded (#395, #423).
- **`software.sslmate.com/src/go-pkcs12` moves to v0.7.3 in all three
  modules** (#522) for GHSA-mpwr-8vm7-h73f, an authentication bypass in the
  package's decode functions. Measured rather than assumed: this repository
  calls exactly one symbol from it, the `Modern2023` encoder behind
  `ExportPKCS12`, and never decodes a `.p12`, so the bump is hygiene, not
  exposure; the grep is recorded in `docs/certificate-authority/architecture.md`
  beside the dependency.
- **`echarts` moves to 6.1.0 in the frontend** (#522) for CVE-2026-45249, an
  XSS. The major bump was blocked only by `svelte-echarts`, which nothing
  imported; it is removed. The production build, `svelte-check` and the 31
  time-series Playwright tests were run against the new major.
- **`google.golang.org/grpc` moves to v1.83.2 for CVE-2026-84445, and a stale
  trivy database now refuses to certify rather than certifying blindly**
  (#451, #477).

## [0.16.3-alpha] - 2026-09-06

### Added

- **`RoleServicer.ReconcileBuiltInRoles(ctx, tenantID)` — the ONE sanctioned
  writer to a built-in role's permissions** (#354). A built-in role is
  immutable by design, and the guard's own comment recorded the assumption it
  rested on: "there is no built-in permission reconcile path on boot, so this
  refusal costs no existing caller." That expired the first time a permission
  was renamed. Roles are copied out of `BuiltInRoles()` when a tenant is
  provisioned and never touched again, so a tenant created before the rename
  keeps the retired verbs forever and never gains the live ones — while update
  is refused, delete-and-reseed is refused, and create-over-the-top collides
  with the name index. Reconciliation is admitted because its target is not
  arbitrary: it is `BuiltInRoles()` itself, the definition the guard exists to
  protect. Every other caller is still refused. It is idempotent (a converged
  tenant costs ZERO store writes), touches only `IsSystemRoleName` names so
  custom roles survive untouched, and repairs Owner when present without ever
  creating it — Owner is posture-conditional. It must be called AFTER module
  permission registration, because `BuiltInRoles()` reads the registry at call
  time. Reported per role as `ReconcileReport` / `RoleReconcileResult`
  (permissions added, permissions removed, created).
- **`Role.RetiredPermissions` — drift visible on read, before the repair runs**
  (#354). Derived on every read from the live permission registry, listing the
  stored permissions this build no longer defines, and NEVER persisted:
  `marshalRole` is the single seam every store write passes through and clears
  it. Surfaced on `GetRole`, `GetRoleByName` and `ListRoles`, and on every
  transport's role payload. `Role.RetiredPermissionError()` returns the typed
  `*RetiredPermissionError`; no read path returns it as a failure, because a
  listing that failed on drift would hide exactly the roles an operator needs
  to see.
- **`permission.Unregistered(perms)`** (#354) — screens a whole permission list
  against the registry under ONE read lock. Every role read now screens its
  permissions and Owner holds `All()`, so a loop over `IsRegistered` would take
  a global RWMutex per permission on the authorization path.
- **The reconcile verb on every transport and in the CLI** (#354): REST
  `POST /roles/reconcile`, gRPC/Unix `ReconcileBuiltInRoles`, QUIC
  `role.ReconcileBuiltIn`, MCP `qrdb_role_reconcile_builtin`, embedded, and
  `qrdb roles reconcile [--tenant ID]`. It carries `roles:create` rather than a
  permission of its own: reconciliation IS seeding applied to an existing
  tenant, and a dedicated verb would be absent from every role seeded before it
  existed — so nobody would hold the grant needed to repair that exact absence.
  `qrdb roles get` prints a `Retired:` line. Re-exported by `sdk/go`:
  `ReconcileReport`, `RoleReconcileResult`, `RetiredPermissionError`,
  `RetiredPermissions`; and by `sdk/go/transport`:
  `RoleClient.ReconcileBuiltInRoles`, `ReconcileRolesRequest`,
  `ReconcileRolesResponse`, `RoleReconcileResult`, `Role.RetiredPermissions`.

### Fixed

- **`docs/identity-rbac/api.md`'s role capability matrix had drifted from the
  code** (#354). `GET /roles/by-name/{name}`, `GET /roles`,
  `POST /permissions/resolve`, `permission.List` and `permission.Resolve` named
  routes and methods that do not exist; corrected to `GET /roles/by-name?…`,
  `GET /roles/list`, `POST /roles/resolve-permissions`, `role.ListPermissions`
  and `role.ResolvePermissions`.

## [0.16.2-alpha] - 2026-09-05

### Added

- **`service.AttestationRecorder` — the trusted-server path for recording
  what an enrollment ceremony proved** (#342). `AddAttested` / `RotateAttested`
  take a typed `auth.VerifiedAttestation{Level, AAGUID}` and are the ONE
  writer of the server-owned `attest_*` factor-metadata namespace. The
  production user auth-factor service implements it alongside
  `UserAuthFactorServicer`; the in-process host that verified the attestation
  reaches it by type assertion, as it reaches `ActiveFactorChecker`. It is
  deliberately absent from the servicer interface and from every transport —
  no handler maps to it, so nothing on any wire can produce a verified
  attestation. Caller-supplied `attest_*` keys are refused on this path
  exactly as on `Add` (#234's protection is intact), and a level outside
  `none` / `software` / `hardware` is refused with `ErrInvalidAttestation`
  before anything is written. Re-exported by `sdk/go`: `AttestationRecorder`,
  `VerifiedAttestation`, `AttestationLevel` and its three values,
  `Unattested`, `MetadataAttestLevel`, `MetadataAttestAAGUID`,
  `ReservedMetadataPrefix`, `InvalidAttestationError` / `ErrInvalidAttestation`,
  and — missing since #234 — `ReservedMetadataKeyError` / `ErrReservedMetadata`.

### Changed

- **Every `Add` and `Rotate` on the user auth-factor service now records
  `attest_level=none`** (#342). A factor written without a ceremony proved
  nothing, and the server says so rather than leaving the key absent, so a
  record that proved nothing is distinguishable from one written before the
  field existed. The response carries the persisted metadata, stamp included.
  Callers that stamped `none` themselves to get this distinction must stop:
  the key is now refused as input on every path and written by the server.

### Fixed

- **`sdk.UserServicer` now returns `sdk.UserNotFoundError`** (#343).
  go-qrdb#289 (v0.16.1-alpha) moved `sdk.UserNotFoundError` from an alias of
  `pkg/user.UserNotFoundError` to an alias of `transport.UserNotFoundError`
  so every wire transport matched one type — but the re-exported servicer
  kept returning the service type, which the SDK no longer exported, so a
  servicer consumer honouring the `sdk/go` boundary could not classify a
  missing user at all (go-quicbaas's first-boot admin creation failed on
  its email-uniqueness check). `pkg/user.UserNotFoundError` is now an alias
  of the transport type: ONE type, returned by the servicer in-process and
  by every transport, matched by one `errors.As` against
  `sdk.UserNotFoundError`. The embedded transport's service-to-transport
  translation is deleted — there is nothing left to translate. The message
  is `user: not found: <id|lookup>` whichever layer returns it.

## [0.16.1-alpha] - 2026-09-05

### Added

- **`orchestrator:task_readmit` and `orchestrator:task_block`** (#339), beside
  `orchestrator:task_claim` in the broker data-plane `orchestrator` family.
  `task_readmit` returns a blocked orchestration task to the pending queue;
  `task_block` parks a task as blocked (`needs_info` / `blocked` / `escalated`)
  with a reason. Both are `ScopeTenantOnly` like every other verb in the family
  — denied to the system tenant, confined to the identity's own tenant — and are
  placed exactly where `task_claim` is: Operator holds them (and Admin through
  `orchestrator:*`); User and Auditor do not. The family stays qrdb-owned, which
  is what lets go-quicbaas register the two verbs it maps on its own transports
  without forking the resource.

## [0.16.0-alpha] - 2026-09-02

### Added

- **A real account lifecycle on `user.User`** (#196). `Enabled bool` could not
  express the states an identity actually moves through, so every consumer above
  qrdb carried its own — go-quicbaas kept a `BrokerProfile` side-record with a
  state enum plus separate `suspended` and `locked` booleans, an encoding that
  admits contradictions (locked *and* active) with no rule saying which wins.
  `Lifecycle` is now the single source of truth — `pending` · `active` ·
  `suspended` · `locked` · `disabled` · `deleting` — with a fixed transition
  table, and `Enabled` is a derived projection (`Lifecycle == active`) kept
  because it is the field existing clients read. `User.CanAuthenticate()` is the
  check, and it fails closed on a state the build does not recognize.
  `deleting` is **terminal**: a deletion writes it before its first mutation, so
  a credential ceremony that finishes late cannot leave a fresh orphan behind
  (the shape go-quicbaas#290 needs to close its registration-during-deletion
  TOCTOU). Re-asserting a state is a no-op, so a partially completed deletion is
  resumable.
- **`GroupIDs`, `AuthMethods` and `LastAuth` on `user.User`** (#196), settable
  through `CreateUserRequest` and `UpdateUserRequest`. `GroupIDs` stores the
  membership EDGE — qrdb owns no Group entity, exactly as `RoleIDs` holds role
  identifiers without owning the roles; a group's name and the roles it confers
  belong to the layer that performs the authorization consuming them.
  `AuthMethods` is a policy statement of what an account may present, distinct
  from `Factors`, which records what it has enrolled. `LastAuth` is a high-water
  mark that never moves backwards, so overlapping logins written out of order
  cannot roll an account's last-seen time back.
- **`NormalizeGroupIDs` / `NormalizeAuthMethods`** (#196) — the canonical
  identity-set normalizers, re-exported under `sdk/go` beside `NormalizeEmail`.
  Group ids preserve case (opaque minted tokens); auth methods fold case (a
  closed protocol vocabulary).
- **Every new identity field on every transport** (#196). REST, gRPC, QUIC, MCP,
  unix and all six `sdk/go/transport` clients carry `lifecycle`, `groupIDs`,
  `authMethods` and `lastAuth` on both the response and the create/update
  requests. `enabled` collapses five distinct states into one `false`, and a
  field the domain stores while the wire drops it is a field a remote consumer
  cannot see — which is how a consumer ends up keeping its own copy in the first
  place.
- **The clear-versus-leave-alone distinction survives every wire** (#196).
  `nil` leaves a set alone and an empty non-nil slice CLEARS it, and each
  transport carries that in its own idiom: JSON gets it free (absent / `null` /
  `[]`), gRPC needs explicit `has_group_ids` / `has_auth_methods` flags because
  proto3 cannot distinguish an absent repeated field from an empty one, and the
  MCP tools take `clear_group_ids` / `clear_auth_methods` because a flat tool
  argument cannot either. Without them a membership could be granted but never
  **revoked**: an empty list would read as "no change" and the revocation would
  report success while doing nothing.
- **The MCP identifier lists are JSON arrays, not comma-separated strings**
  (#196). A group id is an opaque minted token, so a comma is a character it may
  legitimately contain: CSV split such an id into two memberships and dropped
  blank elements the store refuses, writing a different authorization graph than
  the caller asked for while reporting success. A malformed list is now reported
  rather than read as "leave alone", because quietly not changing an
  authorization list the caller asked to change is the same defect wearing a
  different hat.

### Changed

- **`AuthMethods` is enforced at the authentication gate**, not merely stored
  (#196). `pkg/auth/middleware` now carries the name of the authenticator that
  succeeded and refuses with `AuthMethodNotPermittedError` (403 /
  `PermissionDenied`) when the account's policy is non-empty and does not list
  it. Storing the policy without checking it made method withdrawal fail
  **open**: a user restricted to `webauthn` could still walk in through the
  mTLS authenticator, because the credential was valid and nothing compared it
  against the policy. An empty policy states no restriction, so no account that
  predates the field is affected; and the check applies only where the method is
  observable to qrdb, because a forwarded assertion and a capability module's
  session token carry none it could evaluate. That second limit is carried by an
  explicit flag rather than by an empty name — an authenticator that names
  itself `""` is observed, not unobservable, and conflating the two would have
  let it bypass the policy through the ordinary chain.
- **`DeleteUser` claims the record by marking it `deleting` before the cascade**
  (#196), and the cascade commits against the marked version. Until the mark
  landed the delete had not won the record: another writer could still bind a
  credential or move a handle, and the cascade was sweeping a snapshot that no
  longer described the account. The account is now unauthenticable from the
  FIRST write of the delete rather than the last.
- **A terminal record accepts no mutation at all** (#196). `UpdateUser`, the
  `LastAuth` merge path and `AddCertBinding` all refuse it. Refusing only the
  lifecycle transition left every other field writable, so a certificate could
  still be bound to an account whose deletion had begun — the exact orphan the
  marker exists to prevent. Re-asserting the terminal state stays permitted, so
  a partially completed deletion is still resumable.
- **Lifecycle refusals map to client-actionable statuses** (#196).
  `InvalidLifecycleError` is HTTP 400 / gRPC `InvalidArgument`; a
  `LifecycleTransitionError` is HTTP 409 / gRPC `FailedPrecondition`. Neither
  had a mapping arm, so the commonest instance — an `EnableUser` racing a
  delete — surfaced as a 500 / `Internal`: a server fault reported for a refusal
  the server made deliberately, and one no client could learn to expect.
- **A `LastAuth`-only update is a commutative merge that retries** (#196)
  instead of returning `ConcurrentUpdateError`. `LastAuth` is written on the
  login path while ordinary updates are where an administrator edits roles;
  routing the login through the general path made its timestamp lost — and lost
  *below* the true high-water mark, because the loser computed its max against a
  base the winner had already replaced — whenever the two overlapped. The retry
  is bounded and sleepless, and the merge writes nothing at all when the stored
  value is already at or past the request.
- **`EnableUser` and `DisableUser` are lifecycle transitions**, not boolean sets
  (#196). Both are refused with a `LifecycleTransitionError` on an account
  already marked `deleting` — a plain `Enabled = true` would have revived an
  account whose deletion was in flight. Repeating either verb is still a no-op.
- **Records written before this release need no migration** (#196). A decode
  with no `lifecycle` derives one from `Enabled` (`true → active`,
  `false → disabled`) and re-derives `Enabled` from it, so no pre-upgrade
  account is locked out by an empty state and the explicit field is picked up
  the next time the record is written. A state that is present but unrecognized
  — written by a newer node mid-upgrade — is left exactly as written.

### Fixed

- **`docs/identity-rbac/engineering.md` had drifted** (#196): its `User` struct
  still showed a `string` ID and no `Email`. Corrected alongside the new fields.
- **`sdk/go/go.mod` required a stale root** (#196). It named
  `github.com/jeremyhahn/go-qrdb v0.10.0-alpha` while the tags had reached
  v0.15.0-alpha. Its `replace ... => ../..` keeps the repository building, but a
  `replace` in a dependency module is ignored by consumers, so an external
  module taking `sdk/go` alone resolved a root five releases behind it. Bumped
  in the release commit that cuts this version, and verified against a throwaway
  external consumer.
- **The release notes for 0.13.0-alpha, 0.14.0-alpha and 0.15.0-alpha were never
  written** (#196), so `make check-version-reconciled` — the check that exists
  precisely to catch this — had been failing on `main` since v0.13.0-alpha:
  VERSION read `0.15.0-alpha` while the newest CHANGELOG release heading still
  read `0.12.0-alpha`. The three sections are reconstructed below from the
  release commits and tag annotations, which restores the check to green.

## [0.15.0-alpha] - 2026-09-01

Notes reconstructed for a release that shipped without them (see the
[Unreleased] entry for #196, which found the gap). Content is taken from the
release commit and the tag annotation, not re-derived.

### Added

- **`Email` is the canonical tenant-unique login key** (#191). `User.Email` and
  `User.Name`, a tenant-scoped email secondary index, and `GetUserByEmail`
  across REST, gRPC, QUIC, MCP, the SDK and the CLI.

### Changed

- **`CreateUser` refuses a user with no email login key** unless the caller sets
  `CreateUserRequest.WithoutEmailLoginKey` (#191). A certificate-only or
  bootstrap identity is legitimate; an ordinary user silently created with no
  entry in the index every login resolves through is not. **Breaking.**

## [0.14.0-alpha] - 2026-08-31

Notes reconstructed from the tag annotation; see the note under 0.15.0-alpha.

### Added

- **Deferred tenant-admin enrollment seam** (#189, #193).
  `AdminEnrollmentMode` on `ProvisionTenant` — `cert` (default) or `deferred`
  (pending-invite) — with fail-closed pending-admin validation including a
  canonical-factor-store check (`service.ActiveFactorChecker` /
  `HasActiveFactor`), and `user.CreateUserRequest.Pending`.
- Registration-config ownership documented (it belongs to quicbaas
  `iam.self_service_registration`, not here).

## [0.13.0-alpha] - 2026-08-28

Notes reconstructed from the tag annotation; see the note under 0.15.0-alpha.

### Added

- **Coordination surface for control-plane consumers** (#166, #168) —
  lock / lease / CAS / watch. Hard acknowledged-deadline lease-loss model,
  `LeaseLostError` on late acks, a terminal-error contract on `WatchStream`, and
  bounded cleanup revoke.

### Fixed

- **`dek_accesses` counts reads, not attempts** (#164) — the counter is
  incremented inside the borrow.
- **`ChangePassword` leaves the barrier still checking passwords** (#161).
- **`withCurrentDEK`'s purge borrow is held across the callback**, with both
  escapes fenced (#82).

### Security

- Bumped echo to v4.15.3 for CVE-2026-55677 (#155).

## [0.12.0-alpha] - 2026-08-25

The guarded-verb plane: the conditional writes that arbitrate a cross-process
race are now reachable on every engine, in both deployment modes, and over the
remote transports — not only through the embedded server. A 12-cell conformance
matrix is the gate that says so, and it found two real defects on its way in.

### Added

- **`CompareAndSwap` on the transport boundary** (#121). `service.KVServicer`
  has carried all three guarded verbs since #56, but only `PutIfAbsent` and
  `CompareAndDelete` reached `transport.GuardedKVClient` in #119. A consumer
  holding the capability could arbitrate a create and a release and still had to
  read-then-write to *edit* — reopening the window the capability exists to
  close, over a network round trip on a remote transport. Implemented on the
  embedded and gRPC transports; QUIC, unix, REST and MCP are tracked as
  #124–#127.
- **The guarded verbs over gRPC** (#119, #123): `PutIfAbsent` and
  `CompareAndDelete` as RPCs, delegating to the service layer's atomic
  primitives with no client-side emulation.
- **`transport.GuardedKVClient`** (#119) — a capability sub-interface detected
  by type assertion, so a transport that cannot honour the verbs does not
  promise them.
- **`CompareAndSwap` and transactions on the file KV engine** (#117, #120), at
  proven parity with the MVCC engine.
- **A 12-cell conformance matrix** (#121): `{file, memory, pebble}` ×
  `{standalone, raft}` × `{embedded, remote-gRPC}`, asserting identical guarded
  semantics on every cell. It runs in-process in the ordinary test pass rather
  than forking the daemon, so a regression turns something red the same day.
- **`kvstore.GuardedBatchKVStore`** (#21) — a capability sub-interface for
  applying several writes atomically, conditional on one key still holding an
  expected value. Detected by type assertion, like `AtomicKVStore`; a store
  without a transaction plane does not advertise it.
- **A typed capability refusal** (#141). `transport.CapabilityUnsupportedError`,
  produced on every transport, so a consumer can detect "this node was not built
  to do that" with `errors.As` and degrade gracefully instead of parsing an
  error string.

### Changed

- **A capability refusal answers `Unimplemented` over gRPC, not `Internal`**
  (#141). `Internal` means the server broke; a client library alerts on it and
  retries an operation that can never succeed. REST answers 501 for the same
  condition, as it did before. **Consumers keying on `codes.Internal` for this
  condition must update.**
- **Registry capability descriptors now describe what the state machines can
  actually do** (#135). The `memory`, `standard`, `concurrent`, `fast`, `disk`
  and `highperf` state machines declared `mvcc`, `transaction`, `lease`, `index`
  and `timeseries` and could perform none of them — every registry factory
  constructs with zero options, so those subsystems are nil. Those flags are
  removed, and `regular` gains the `batch` capability it always had.
  **`sdk.SMDescriptor` consumers will see different `Capabilities` values.**
  Nothing in the database gates an operation on these flags; they are
  informational, and they were misinforming.

### Fixed

- **An unimplemented opcode no longer wedges the raft shard** (#129).
  `KVStateMachine.Update` returned `InvalidOpcodeError` to Raft, which treats a
  non-nil `Update` error as a fatal apply failure and trips the shard's circuit
  breaker. Measured: one ordinary authorized `CompareAndDelete` — an opcode no
  state machine in the package implements — permanently removed the shard from
  service. Every later proposal failed, including plain `Put`, and a
  linearizable read never returned at all. Reachable through the SDK's own
  guarded verbs with nothing more than `kv:delete`. Such an operation is now
  refused with a typed error and the shard keeps serving.
- **A batch naming an unimplemented operation is refused whole** (#129). The
  batch handlers applied operations in turn and only refused on *reaching* the
  unknown one, committing the earlier writes and their watch notifications while
  reporting the batch refused. Opcodes are now validated before anything is
  applied.
- **Transactions are served over the remote transports in standalone mode**
  (#121). `sdk/go/node` composed the transaction service only on the raft path,
  so `ExecuteTransaction` worked over the embedded transport and returned
  "transaction service is not configured" over gRPC on the same node — the
  engine/mode/transport non-uniformity #117 rules out.
- **A custodian enrolment is written as one guarded transaction** (#21). Its
  three keys — the primary record, the tenant index, and the username-index
  commit — are applied atomically, conditional on the registration still owning
  the username claim. Claims expire, so a registration that stalled past the TTL
  could previously lose its claim between an ownership check and its write, land
  that write on top of the winner's, and leave the winner's published index
  pointing at the loser's record: two callers returning success with different
  sealed envelopes, one record surviving, nothing reporting it.
- **A claim is released with a compare-and-delete** (#21), so seal-failure
  cleanup can no longer remove a claim another registration has legitimately
  taken over in the meantime.
- **Shamir share and secret custody moved into protected memory** (#76).
- **A data race in the QUIC e2e index-flags test harness** (#131), which could
  also make its polarity assertion pass for the wrong reason.

### Changed — BREAKING (sdk/go)

- **`IndexScan` expresses an open bound** (#115), so a one-sided range returns
  its matches. A consumer building index scans must update its bound
  construction.

### Scope of the guarded plane in this release

Stated explicitly, because "identical on every cell" has a boundary:

- **Standalone** — all three backends serve the full guarded plane on both the
  embedded and gRPC transports.
- **Raft, default `sm_type`** (`""`, `pebble`, `mvcc`) — full guarded plane.
- **Raft, non-default `sm_type`** (`memory`, `standard`, `concurrent`, `fast`,
  `disk`, `highperf`) — `PutIfAbsent` works and is genuinely atomic, being a
  native KV opcode. `CompareAndSwap`, `CompareAndDelete` and transactions
  **refuse with a typed error and the node keeps serving**. Those state machines
  never had a working MVCC store, and they are refused outright when the seal
  barrier is enabled. Giving them one is tracked as #136; when it lands, those
  matrix cells flip from refusal to full passage.
- **The file engine refuses `create_revision`/`mod_revision` transaction
  compares.** This is designed behaviour, not a limitation to be worked around:
  the engine keeps no MVCC revisions, so a revision compare would read 0, two
  concurrent writers would both compare equal and both commit, and one update
  would be lost with no signal. The native `PutIfAbsent` verb works there, so no
  capability is unreachable — only the etcd revision idiom.
- **Remote transports** — embedded and gRPC. QUIC, unix, REST and MCP do not
  carry the guarded verbs yet (#124–#127); their `GuardedKVClient` assertion
  correctly reports false until they do.

### Internal

- CI now runs the `sdk/go` module's tests and requires them in the gate (#132).
  They had never been executed by CI, so the entire guarded-verb transport plane
  this release is built on had merged on green runs that never exercised it.

## [0.11.0-alpha] - 2026-08-24

### Added

- **Standalone-mode transactions and compare-and-swap** (#62, #109). The
  standalone server now serves the transaction plane and CompareAndSwap, so a
  single-node deployment has the same conditional-write primitives a cluster
  does. This is the primitive `xkmsd` needs to fresh-boot on a qrdb backend
  (unblocks go-xkms #61).
- **`kvstore.CompareAndSwap`** (#81) — the guarded edit the atomic set was
  missing: replace a value only when the current value matches the expected one.
- **`sdk/go` `GuardedSwap`** (#78) — an optimistic-CAS loop over the transaction
  plane that retries a read-modify-write until it commits with no conflicting
  writer.
- **Write results now report whether a failed write was stored** (#103), stated
  at the layer that knows rather than inferred by the caller.
- **`sdk/go` re-exports `UsableAsMapKey`** (#68), so a consumer can guard a
  per-store cache without reaching past the SDK boundary.

### Changed — BREAKING (sdk/go)

- **Index creation carries case-sensitivity end to end** (#34, #108). The
  `sdk/go` index-create surface (`kvstore_adapter`, every transport's
  `transport_index`) gains a case-sensitivity flag and its request shape
  changes. A consumer pinning `sdk/go` must update its index-create calls.

### Fixed

- **A guarded write is durable before it acknowledges** (#94, #110). The state
  machine persists the write before returning success, so an acknowledged
  guarded write survives a crash.
- **Built-in roles are actually immutable** (#105): the `System` flag is derived
  from the role name rather than trusted from the stored record, so a built-in
  role cannot be made editable by clearing the flag.
- **Vector IVF clustering is seedable** (#99), and the recall test is fixed at
  its source — the single shared random init — instead of by averaging
  downstream of it.
- **The quota reservation is kept on every ambiguous write, not just the swap**
  (#98): a write whose stored/not-stored outcome is unknown no longer leaks
  quota.
- **The cert watcher is released on every `runStart` return, not only the clean
  one** (#97).
- **Snapshot framing is bounds-checked through the same predicate the encoders
  use** (#80).
- **`OpPutIfAbsent` is applied everywhere, and first boot reports its real
  error** (#87).
- **A version condition on an entity with no version is refused, not
  fabricated** (#75).
- **MVCC recognises the absence spelling every storage engine actually
  produces** (#77).
- **A failed compare read stalls the transaction instead of silently picking a
  branch** (#64).
- **An empty index tag is a registration error, not a silent no-op** (#67).
- **A not-configured subsystem is encoded so its reader can decode it** (#70).
- **A DAO miss is reported as NOT FOUND over both stores the SDK ships** (#65).
- **One bounds-checked length writer in the protocol**, so the omission cannot
  compile (#53).

### Fixed — SECURITY

- **Purge-class memguard calls are coordinated against live borrows** (#72), so
  a purge cannot free memory another goroutine is still reading.
- **The unseal ceremony fails closed on an unreadable share and holds the PIN
  borrow across the KDF** (#91).

## [0.10.0-alpha] - 2026-08-20

### Fixed — SECURITY

- **`StoreResolver` no longer hands every shared-prefix tenant the same
  unconfined store** (#41). It returned `r.shared` itself for the system
  tenant, every `IsolationPrefix` tenant and every unknown tenant, with no
  keyspace prefix applied. A `dao.GenericDAO` keys entities as
  `{entityType}/{id}` and has no tenant concept, so two prefix tenants' DAOs
  occupied one undifferentiated keyspace: measured on a two-tenant node, one
  tenant's `Count()` returned another's rows and one tenant's `QueryByIndex`
  returned another's rows.

  The data-level exposure pre-dates v0.8.0-alpha; the index-level exposure
  became reachable in v0.8.0-alpha, which made those index paths real.

  `Resolve` now returns a store confined to `{tenant}/` — the tenant alone.
  This store serves IDENTITY (users, roles, permission scopes, invitations),
  and identity belongs to a tenant rather than to one of its databases: a user
  must authenticate whichever database a request names. Namespacing it per
  database was measured to break login outright, because firstboot writes the
  admin user with no database bound to its context.

  `tenant.ComposeNamespace` remains the DATA plane's `{tenant}/{database}/`
  formula, applied by the transports and the SQL provider; that scoping is
  unaffected.

  Either way the isolation is a property of the store rather than of every
  caller remembering to prefix.

  Concrete consequence: `invite.TenantStore` documented that "a redemption
  attempted against the wrong tenant resolves to a different store and returns
  ErrNotFound". That was true for barrier tenants and **false for prefix
  tenants**, whose invitations shared one keyspace — and `Entity.TenantID` was
  written but never compared, so nothing else caught it. An invitation issued
  for prefix tenant A was redeemable through tenant B. The fix is what makes
  the documented guarantee true; the doc comment now records the correction.

### Changed — BREAKING (key layout)

- **Every record a shared-prefix tenant reaches through `StoreResolver` moves
  under `{tenant}/`.** This includes the reserved `__sys:` keyspace —
  users, roles, permission scopes — and DAO-keyed entities alike. There is one
  tenancy mechanism, the namespace, and no prefix exempt from it.

  **Pre-release policy** (`release-versioning.md` §1): backward compatibility,
  migrations and upgrade paths are non-goals before public release. The key
  layout changed clean. **Wipe and re-bootstrap dev deployments** — that is the
  upgrade path. Nothing is "orphaned"; the old layout simply ceases to exist.

### Fixed — regression introduced by the isolation above

- **Every IAM operation that names a record by its globally unique ID now
  reaches the tenant that HOLDS it** (#41). Confining each tenant's keyspace
  broke `users delete <id>`, `users get/enable/disable <id>`,
  `roles delete <id>` and `roles assign --role <id>` for any record created
  with `--tenant-id`: the by-id methods routed on the CALLER's tenant, and the
  admin's tenant is `system`, so the record was looked up in a store it does
  not live in. Measured on the integration stack: five CLI suites failing
  identically across all five transports with `user: not found` /
  `role: not found`.

  They worked before only by accident — every prefix tenant shared one
  keyspace, so "the caller's store" and "the record's store" were the same
  store. That accident is exactly what #41 removes, so the by-id route is now
  made explicit.

  `tenant.IdentityIndex` is a system-plane id → tenant map living in the
  system tenant's identity namespace — `tenant.IdentityNamespace`, the same
  single formula, at `__sys:identity_idx/tenant/{kind}/{id}`. An entry carries
  the id and the tenant and **nothing else**: it necessarily sits outside a
  barrier tenant's encryption, so it may leak existence and placement, never
  a username, role name or permission. It is written and removed by the same
  functions that write and remove the record — `CreateUser`/`DeleteUser`,
  `CreateRole`/`DeleteRole` — alongside their other index writes, because a
  mapping kept in step by a sibling caller is how every instance of this class
  began.

  The entry is a CLAIM, not a write. `CreateRoleRequest.ID` is
  caller-supplied — it travels from the REST and gRPC surfaces straight into
  the store — so a last-writer-wins map would let one tenant create a role
  carrying ANOTHER tenant's role id, take over that id's entry, and silently
  redirect every by-id operation an administrator performs on it. `Record`
  now refuses an id ANY tenant already holds — the claimant's own tenant
  included, because a create proceeding on a claim it did not win can have
  that claim released out from under it by a concurrent same-tenant delete,
  leaving a record with no entry and no way to reach it by id. A claim also
  requires a store that can settle it in one operation
  (`kvstore.AtomicKVStore`); there is no read-then-write mode, because two
  proposers would both observe "absent" and both win. `Record`
  refuses a held id with `*tenant.IdentityClaimConflictError` (
  surfaced to callers as `*role.RoleIDExistsError` → 409 / `AlreadyExists`,
  naming the CALLER's tenant so the refusal does not disclose where the id
  lives), `Forget` releases only a claim the named tenant holds, and the claim
  is settled BEFORE the record is written so a refused capture leaves nothing
  behind. `CreateRole` additionally refuses a duplicate id within the tenant,
  closing a pre-existing silent overwrite.

  `tenant.OwningTenant` is the one read-path rule both routers share: an
  unindexed id routes to the caller's own tenant (reach never widens); an
  indexed id routes to its owner when the caller may reach it; and a caller
  who may NOT reach it is routed to its own tenant, where the id is genuinely
  absent — a `CrossTenantDeniedError` there would confirm to a caller who
  supplied only an id both that the record exists and where it lives. A sealed
  owning barrier surfaces `tenant.ErrTenantSealed`; there is no fallback read.

  No CLI or API surface changes. `role.ResolvePermissions` is deliberately NOT
  index-routed: it resolves the caller's own bindings during authorization, and
  routing it globally would let a binding naming another tenant's role import
  that role's permissions.

- **`pkg/engine.KVEngine` applies `kv.OpPutIfAbsent`.** The opcode already
  existed in the command format, `kvstore.AtomicKVStore` advertised it, and
  `service.KVService.PutIfAbsent` proposed it — but of the three state
  machines only `PebbleStateMachine` (the `qrdbd --sm pebble` default) and
  `MVCCEngine` had a handler. On `--sm memory`, `--sm concurrent` and every
  `pkg/server/lifecycle` embedding, a conditional write failed the **entire
  proposal** with `engine: unsupported operation: 0x04`. The handler mirrors
  its two siblings exactly: the existence check runs inside the apply path so
  every replica decides identically, a declined write is reported through
  `Result.Value` (`kv.ResultNotStored`) rather than as an error that would
  stall consensus, secondary and compound indexes are maintained on the stored
  path, and the watch notification carries no `PrevKV` because the guard
  established the key was absent. All three implementations are now pinned by
  the same contract, including "of N conditional puts to the same key, exactly
  one stores, under every apply grouping".

- **`kvstore.AtomicKVStore` gains `CompareAndDelete`, and every state machine
  implements it** (#41). `PutIfAbsent` guarded the create; the release was a
  plain delete, and a guarded create paired with an unguarded delete is half a
  primitive. Two releasers both read holder A, the first deletes, tenant B
  legitimately claims the now-absent id, and the second delete removes **B's**
  claim — leaving B holding a record with no routing entry, the state the
  index exists to prevent, reached through the release path. No amount of
  re-reading closes it, because the window is between the read and the delete.

  New `protocol.OpCompareAndDelete`, handled by `KVEngine`,
  `PebbleStateMachine` and `MVCCEngine` alike, with the comparison evaluated
  inside the apply path so every replica decides identically. A refused delete
  is reported through `Result.Value`, never as an error that would stall
  consensus; a failed guard READ refuses rather than deciding, exactly as on
  the conditional-put path. `tenant.IdentityIndex.Forget` now releases through
  it.

  Encrypting wrappers (`seal.StorageBarrier`, `seal.atomicTenantSharedKVStore`,
  `database.BarrierBackend`) guard on the **ciphertext they read**, not on a
  re-encryption of the expected value: encryption is randomised, so a
  re-encrypted guard would refuse every time, while the observed ciphertext
  still refuses whenever anything wrote in between.

  One carrier genuinely cannot: `seal.StorageBackend` is a type alias for
  go-quicraft's interface, which has `PutIfAbsent` but not its release half.
  Those two call sites discover the capability and refuse with
  `seal.ErrGuardedDeleteUnsupported` rather than degrading — every backend in
  this repository implements it.

### Added

- `tenant.IdentityIndex`, `tenant.NewIdentityIndex`,
  `tenant.NewResolvedIdentityIndex`, `tenant.OwningTenant`,
  `user.WithIdentityIndex`, `role.WithIdentityIndex`,
  `tenant.IdentityClaimConflictError`, `role.RoleIDExistsError` — see above.
  The Option form keeps the index out of single-tenant embeddings, which have
  nothing to route.
- `kvstore.NewNamespacedKVStore` — a `KVStore` decorator confining every
  operation, including index registration and queries, to a key prefix. Only
  the variant over an atomic inner store implements `AtomicKVStore`, so the
  capability assertion answers honestly. An empty prefix is an error rather
  than a pass-through.
- `kvstore.EntityIndex.KeyPrefix` and `ResolvedPrefix` — the key prefix an
  index covers. Stores previously assumed `{entityType}/`, which holds only
  while the entity type is the LEADING key segment; a namespaced store breaks
  that and would silently index nothing.

### Known limitation

- **Shared-prefix tenants trade engine-backed indexes for isolation — the
  system tenant included** (#40 restores them). `KVEngine` decides which
  indexes a write maintains via `entityTypeFromKey`, which returns the FIRST
  path segment — so a namespaced key `acme/system/workitem/1` reads as entity
  type `acme`. The adapter refuses the namespace-qualified index name and the
  store refuses subsequent index queries with `kvstore.ErrIndexUnsupported`.
  Query RESULTS stay correct: `pkg/dao` detects the refusal and plans a full
  scan, so only the access path is slower. Teaching the engine about
  namespaced keys is #40's priority-1 mechanism.

## [0.9.0-alpha] - 2026-08-20

MINOR rather than PATCH: no qrdb API moves here, but `go-quicraft` advances TWO
minors and changes cryptographic behaviour on the AES-GCM data path, which this
module had to absorb at one call site.

### Changed

- **`go-quicraft` v0.6.2-alpha to v0.8.0-alpha**, `go-objstore` v0.2.2-alpha to
  v0.3.0-alpha. `go-truststrap` (v0.2.0-alpha) and `go-codec` (v1.4.0) were
  already current.

- **The wrapped-root-key envelope absorbs go-quicraft's AEAD nonce contract
  change.** In v0.7.0-alpha `pkg/seal` moved to `cipher.NewGCMWithRandomNonce`
  — the only GCM construction approved under `GODEBUG=fips140=only`, since SP
  800-38D requires the module itself to own nonce generation. Measured at the
  two pins:

  | | v0.6.2-alpha | v0.8.0-alpha |
  |---|---|---|
  | construction | `cipher.NewGCM` | `cipher.NewGCMWithRandomNonce` |
  | `aead.NonceSize()` | 12 | 0 |
  | `aead.Overhead()` | 16 | 28 |

  `pkg/seal/unsealkey.go` was the one place in this repository that took a raw
  `cipher.AEAD` from that package and framed its own nonce: `wrapRootKey` sized
  a nonce from `aead.NonceSize()` and persisted it in the `Nonce` field of the
  `wrappedRootKey` JSON envelope, beside `Ciphertext`, and `resolveDirectSecret`
  length-checked that field against `aead.NonceSize()` before `Open`.

  **A straight bump would have been silent and would have cost the store.**
  Nothing fails to compile — `cipher.AEAD` is unchanged — and a fresh barrier
  works, because new writes and new reads stay consistent with each other. But
  the write path starts emitting a ZERO-length nonce with a 60-byte ciphertext
  where the format has always carried 12 and 48, and the read path rejects every
  blob written before the bump with `ErrWrappedRootKeyCorrupt`. That blob is the
  barrier root key wrapped under the unseal key: an existing barrier stops
  unsealing. Measured on a real pre-bump blob, not inferred.

  The fix keeps the persisted bytes IDENTICAL in both directions rather than
  migrating them. `NewGCMWithRandomNonce` emits `nonce ‖ ciphertext ‖ tag`,
  which is exactly the pair the envelope already stored in two fields, so
  `wrapRootKey` splits the module-generated prefix back off and `unwrapRootKey`
  re-joins it before `Open`. Old blobs open unchanged, new blobs are
  indistinguishable from old ones, and **no migration is required** — which is
  the point: a wrapped root key is written once at initialization and read on
  every unseal for the life of the barrier, so a format change here would need a
  migration executed while the barrier is still sealed, i.e. while the key
  material needed to authenticate it is by definition unavailable.
  `pkg/ceremony`'s `sealEnvelope` splits the same prefix for the same reason;
  this is that pattern, not a new one.

  The envelope's nonce-length check now reads `SymmetricAlgorithm.NonceSize()`
  — the WIRE width, which stayed 12 — instead of `cipher.AEAD.NonceSize()`,
  which is now zero and would accept a nonce field of any length at all.

  Decoding and decryption of the envelope moved out of `resolveDirectSecret`
  into `unwrapRootKey`, so the format rules have one home and are reachable from
  a test without standing up a storage backend.

### Added

- **`pkg/seal/unsealkey_test.go`** gains tests that open a wrapped-root-key blob
  produced by this repository at go-quicraft v0.6.2-alpha, checked in as bytes
  rather than regenerated. Regenerating it would let the test drift onto the new
  format and start asserting only that the new code reads its own output — which
  is precisely the failure mode that makes this class of break invisible. It
  also asserts the write path still emits the 12/48 field widths, that the AAD
  still binds the blob to its `RootKeyPath` (sealing with one barrier and
  opening with another, so the assertion cannot pass merely because the AAD
  changed), and that a nonce field of the wrong width is rejected. All
  mutation-verified.

### FIPS 140-3 posture — the caller-supplied-IV class is GONE

Measured on both sides of this bump from the SAME base commit (414291c), one
script, one machine, Go 1.26.6, `GOFIPS140=latest GODEBUG=fips140=only`,
`-race`, and the build tags `make test` uses. Measuring both sides rather than
comparing against the figure recorded for `0.7.0-alpha` is deliberate: a count
taken by a different run on a differently loaded machine is not a baseline, and
masked findings make counts move for reasons that have nothing to do with the
change under test.

| | before (go-quicraft v0.6.2-alpha) | after (v0.8.0-alpha) |
|---|---|---|
| packages passing | 106 | **120** |
| packages failing | 16 | **2** |
| `use of GCM with arbitrary IVs is not allowed in FIPS 140-only mode` | **699** | **0** |
| `use of X25519 is not allowed in FIPS 140-only mode` | 15 | 15 |

**Fourteen packages move red to green, and there are zero regressions** — the
before-failing set is a strict superset of the after-failing set.

The movement is attributable, not merely coincident: every one of the 699
rejections was the AES-GCM-with-caller-IV construction that go-quicraft
v0.7.0-alpha replaced with `cipher.NewGCMWithRandomNonce`, and the count is
exactly zero afterwards. That whole class is closed for this module.

The X25519 count is *unchanged at 15*, which is the correct outcome rather than
a disappointing one: it is a different finding with a different owner. The two
packages still failing — `cmd/qrdbd` and `pkg/clusterenroll` — fail on
`clusterenroll: bootstrap server (noise) failed to start: noise: invalid key
size: crypto/ecdh: use of X25519 is not allowed in FIPS 140-only mode`. That is
go-truststrap's Noise handshake, closable only by compiling its `pkg/noiseproto`
`fips` variant, which this module's FIPS job does not pass `-tags fips` to. The
same tests fail identically before the bump and pass in the non-FIPS run.

Re-measured at the branch head after the last commit touching test code, not
carried forward from the earlier A/B run: **120 pass / 2 fail / 0 GCM / 15
X25519**, the same figures and the same failing pair. The re-run was not
ceremony — tests added after the A/B briefly introduced `cipher.NewGCM`
fixtures that broke `pkg/seal` under FIPS and invalidated these counts until the
hosted review caught it. A measurement describes the tree it was taken on.

**Two tests now skip under `fips140=only`, and neither is a masked failure.**
`TestWrappedRootKey_CallerNonceProvider` and
`TestWrappedRootKey_RejectsMismatchedProviderNonceWidth` construct a
caller-nonce AEAD to exercise the caller-nonce branch of
`sealWrapped`/`openWrapped`. A caller-nonce AEAD is one built with
`cipher.NewGCM` — exactly what this enforcement level refuses — so that branch
is provably unreachable in a FIPS deployment: no provider, software or hardware,
has an approved way to return such an AEAD. Both run in full outside FIPS. The
skip is keyed on `fips140.Enabled()` rather than a build tag because the FIPS
job passes no extra tags, and it is a skip rather than a stub AEAD because the
assertions that matter need real AES-GCM on both sides. Registered in
`docs/seal/fips140-mode.md` under "Skipped tests" with the same reasoning, so a
future count does not read them as hidden red. (Five tests skip on that run in
total; three predate this change.)

Nothing here claims this module operates under `fips140=only`; two packages
still fail and the job stays out of `ci-gate` while they do.

### Not affected

- Every other AEAD site in this repository — `pkg/crypto/aesgcm`,
  `pkg/dek/crypto.go`, `pkg/ceremony/external_officer.go`,
  `pkg/custodian/share_envelope.go` — already constructs
  `cipher.NewGCMWithRandomNonce` from the standard library directly and passes a
  nil nonce. None of them goes through go-quicraft's provider.
- go-quicraft v0.8.0-alpha's `MemoryEnclave` borrow discipline (`WithBytes`,
  `WithValue`, mutators draining borrows, no callback re-entry) reaches nothing
  here. This module's entire use of `pkg/enclave` is `enclave.WipeBytes`, a free
  function over ordinary slices, and its Shamir use is entirely the string
  `Share` API — no `ProtectedShare`, `SplitProtected` or `CombineProtected`.
  `pkg/secure`'s `enclave` field is this repository's own `memguard.Enclave`
  wrapper, unrelated to go-quicraft's type of the same name.

## [0.8.0-alpha] - 2026-08-19

MINOR, not PATCH: the `kvstore.KVStore` index contract changes (a store that
cannot index MUST now return `ErrIndexUnsupported`; `(nil, nil)` is
forbidden), `EntityIndex` gains fields, and `seal.NewTenantSharedKVStore`
returns a type that no longer always satisfies `AtomicKVStore`. Pre-1.0, MINOR
is where added API and breakage both live.

### Fixed

- **`QueryIndex` no longer lies on `StoreResolver`-returned stores** (#31, #32).
  Every `kvstore.KVStore` a consumer could reach implemented `QueryIndex` as
  `return nil, nil`, and `pkg/dao`'s query planner built its index catalog from
  the entity's struct tags by reflection rather than from what the store could
  serve. A field tagged `index:"true"` therefore routed to `QueryIndex` and came
  back with **zero results and a nil error**, while the same field WITHOUT the
  tag fell back to a full scan and was correct — so adding the index is what
  broke the query, and it failed toward "nothing found" with nothing logged.
  - `lifecycle.kvStoreAdapter` — the store the system tenant resolves to — now
    maintains **real** secondary indexes by routing registration and lookups to
    the node's `IndexServicer`, which reaches the same `KVEngine` that applies
    its writes. Index entries are maintained inside the Raft-applied batch,
    backfilled on registration, and replicated.
  - Stores that genuinely cannot index (`seal.tenantSharedKVStore`,
    `seal.tenantBarrierKVStore`, `embedded.serverKVAdapter`, the `cmd/qrdb`
    and `cmd/qrdbd` KV service adapters when built without a registerer or
    lookup, and any adapter built without an index subsystem) now return the
    new typed `kvstore.ErrIndexUnsupported` instead of an empty result. So do
    the definitions a store cannot serve even when it can serve others: a
    COMPOUND definition on any adapter whose compound query methods refuse, a
    case-insensitive one on the engine-backed paths (#34), and a
    database-qualified name the engine will not maintain (#38). Registration
    success is a capability statement, so it may not be given for something
    the store could never answer.
  - `pkg/dao` probes the capability at construction, **per index kind**. A
    refusing store yields an empty planner catalog for that kind, so tagged
    fields are answered by **full scan and are correct**; a direct
    `QueryByIndex`/`ScanByIndex`/compound call fails closed with the typed
    error. Single-field and compound definitions are registered in separate
    calls because a store can serve one and not the other — the lifecycle
    adapter has real single-field indexes through the engine and no compound
    verb at all — so a compound refusal no longer costs a working single-field
    index, and a compound query over a store without compound support falls
    back to a scan rather than erroring. `dao.New` no longer discards the
    result of `RegisterEntityIndexes`: any non-capability error now fails
    construction.
  - **`WithDatabase` DAOs registered an index name they never queried.**
    `pkg/dao` resolves a database-scoped index as
    `{database}.{entityType}.{field}`, but every store composed the
    unqualified `{entityType}.{field}` itself, so such a DAO advertised an
    index, planned a lookup, and queried a name nobody created. `EntityIndex`
    gains an `IndexName` field carrying the caller's resolved name, which
    every store now uses verbatim via `EntityIndex.ResolvedName` — except the
  engine-backed `lifecycle.kvStoreAdapter`, which REFUSES a qualified name
  (tracked as #38). `KVEngine.filterIndexes` decides which indexes a write
  maintains by matching the name against the key's entity type, and the
  adapter cannot see the engine's database setting — so a qualified name would
  be created, never updated, and answer every query empty with a nil error.
  Refusing makes a `WithDatabase` DAO over a lifecycle store full-scan, which
  is correct.
  - The SDK transport adapter composed `entityType + "." + ei.FieldName` for a
    COMPOUND definition, where `FieldName` is empty — creating a garbage index
    named `{entityType}.` while reporting success. It now refuses compound
    definitions.
- **`seal.tenantSharedKVStore` no longer advertises atomicity it may not
  have** (#30). `kvstore.AtomicKVStore` is optional and discovered by type
  assertion precisely so a caller can fail closed at CONSTRUCTION, but a
  single wrapper type that always defined `PutIfAbsent` made that assertion
  succeed unconditionally — a guard that always passes is not a guard. The
  wrapper is now two types, and only the one over an atomic shared store
  carries the method.
- **`seal.tenantSharedKVStore.PutIfAbsent` no longer degrades silently** (#30).
  When the backing shared store was not an `AtomicKVStore` it fell back to
  `Exists`-then-`Put` — the exact TOCTOU the capability exists to eliminate,
  delivered while reporting success, so two nodes could both win a
  create-if-absent race. It now returns `kvstore.ErrAtomicWriteUnsupported`.
- **The system tenant has an atomic path again** (#30).
  `lifecycle.kvStoreAdapter` had no `PutIfAbsent` at all, so
  `Resolve(ctx, SystemTenantID)` returned a store with no atomic write even
  though the engine beneath it has a native compare-and-set. It now implements
  `kvstore.AtomicKVStore` by delegating to `service.KVServicer.PutIfAbsent`.

### Added

- **`kvstore.MemoryKVStore` / `sdk.NewMemoryKVStore`** (#29) — a supported
  in-process `KVStore` with genuine single-process atomicity and **real** index
  maintenance on the write path. Consumers can unit-test DAO code without
  booting a node and without hand-writing a twelve-method index-aware fake per
  repository. Unique indexes are enforced on the write path
  (`*index.UniqueViolationError`), so a test store never accepts a duplicate
  the real engine would reject; `CaseSensitive` and `Sparse` are honoured
  rather than accepted and ignored (a case-insensitive index that never
  normalized would answer a differently cased probe with an empty result, and
  a unique one would admit both `A` and `a`); and a registration whose
  backfill fails publishes no index at all rather than leaving a half-filled
  one visible. A value it cannot DECODE under an indexed key prefix is now
  refused (`UnindexableValueError`) rather than stored unindexed: storing it
  reported successful index maintenance that did not happen, leaving the
  record invisible to every index query and outside every unique constraint,
  both with nil errors — reachable simply by configuring a DAO with the
  MsgPack or CBOR codec this change re-exports. Index READS hold the store's
  read lock for the whole lookup rather than just long enough to fetch the
  index pointer: a `Put` updates an index in several steps, and a reader that
  released the lock early could observe the key under neither the old term nor
  the new one and report "no matches" with a nil error. The document `null`
  is rejected too: it unmarshals into a nil map WITHOUT error, so it passed as
  a decodable object and then produced no index entries — the record existed
  under an indexed prefix and was invisible to every index query. An empty
  object `{}` remains acceptable, being a real object that simply carries no
  value for the field.
- **`sdk.AtomicKVStore`** (#28) — SDK consumers can now make the fail-closed
  capability assertion at construction without importing `pkg/kvstore`, which
  `docs/00` L3 forbids.
- **`sdk.ErrKVIndexUnsupported`, `sdk.ErrKVAtomicWriteUnsupported`** (#28) — the
  typed refusals, reachable with `errors.Is` from a consumer.
- **The missing DAO surface at `sdk/go`** (#28): `DAOWithShard`, `DAOWithCodec`,
  `DAOWithTimeout`, `DAOWithPartition`, `DAOWithDatabase`; `DAOCodecType` with
  `DAOCodecJSON`/`DAOCodecMsgPack`/`DAOCodecCBOR`; `DAOSortDirection` with
  `DAOSortAscending`/`DAOSortDescending`; and `DAOUnitOfWork`,
  `DAOTransactional`, `DAOVersioned`, `DAOTimestamped`. They carry the `DAO`
  prefix because the SDK root already has `ClientOption` namesakes —
  `sdk.WithTimeout` is a `ClientOption`, so an unprefixed re-export would fail
  at every call site with a type mismatch.

### Changed

- `kvstore.KVStore`'s index-method contract is explicit: a store that cannot
  index MUST return `ErrIndexUnsupported`; `(nil, nil)` is forbidden because it
  is indistinguishable from a maintained index with no matches. A store that
  cannot serve a definition MUST refuse it at registration rather than skipping
  it and returning nil — skipping tells the caller an index exists.
- `RegisterEntityIndexes` is documented as ACCUMULATING per entity type rather
  than replacing, which `pkg/dao`'s split single/compound registration relies
  on.
- `kvstore.EntityIndex` gains `IndexName` and the `ResolvedName` helper.
- The five `TestKVStoreAdapter_*_NoOp` and five `TestTenantBarrierKVStore_*_NoOp`
  tests asserted the `(nil, nil)` behaviour and passed, which is why the defect
  survived — a green test declared it the intended contract. They now assert the
  typed refusal, or are replaced by tests that assert engine-backed answers.

### Known limitations

- **`index:"case_insensitive"` is refused on the engine-backed and transport
  paths** (tracked as #34). `protocol.EncodeIndexCreate` carries no
  case-sensitivity flag, so `KVEngine.handleIndexCreate` builds every index
  case-sensitive; accepting the definition would create a case-SENSITIVE index
  while reporting success, and a lookup for `"Alpha"` over a stored `"alpha"`
  would return empty with a nil error. `lifecycle.kvStoreAdapter` and the SDK
  transport adapter therefore refuse it, so a DAO plans a correct full scan.
  Carrying the flag changes a Raft command format that existing logs replay
  through, which is its own change. Only an explicit `case_insensitive` tag is
  affected: `index.ParseIndexTags` defaults to case-sensitive, so ordinary
  `index:"true"` and `index:"unique"` fields keep their real engine index.
- **The SDK transport adapter refuses an OPEN range bound** (tracked as #36).
  `transport.IndexClient.IndexScan` takes plain strings, so there is no way to
  express "unbounded": a nil bound arrived as `""`, which every index reads as
  an explicit empty bound and REST rejects as a missing parameter. Since the
  planner leaves one side nil for every one-sided range, `Team >= "beta"`
  became `["beta", "")` and returned empty with a nil error. `ScanIndex` now
  refuses a nil bound so the caller sees the incapability rather than a wrong
  answer.
- **Numerically ordered fields are no longer advertised to the query
  planner** (#35 restores them properly). Index terms are decimal text
  compared lexicographically, so `"10"` sorts before `"2"` and `Age >= 2`
  returned an INCOMPLETE result — worse than empty, because a partial answer
  looks like a real one. This change is what makes those index paths live, so
  leaving the ordering enabled would have converted a uniformly-empty answer
  into a silently-partial one.
  `buildCatalog` therefore omits numeric single-field indexes and compound
  indexes with a numeric member, and the planner chooses a correct full scan.
  The cost is the sound numeric EQUALITY lookup losing its index — slower, not
  wrong — and the direct `QueryByIndex` path is unaffected. The surgical fix
  (the planner refusing only RANGE plans on such fields, using the `FieldType`
  it already carries) changes `pkg/planner`'s documented contract, which is an
  owner decision; #35 carries it with the analysis.
- **Barrier-encrypted tenant stores still cannot index.**
  `seal.tenantSharedKVStore` and `seal.tenantBarrierKVStore` hold only
  ciphertext, so nothing beneath them can read a field value. Indexing above the
  barrier would put the indexed value into an index KEY in the shared store, in
  the clear, defeating the isolation those types exist to provide; a
  keyed/deterministic index term preserves it for exact match but destroys the
  ordering range scans need. That is a cryptographic design decision and is not
  made here. Until it is ruled on these stores refuse, and a DAO over them
  answers correctly by full scan.

## [0.7.0-alpha] - 2026-08-19

MINOR rather than PATCH: no qrdb API moves here, but `go-truststrap` advances a
MINOR of its own and reaches consumers through this module's requirement graph,
so anything composing both takes the change transitively.

### Changed

- **`go-truststrap` v0.1.4-alpha to v0.2.0-alpha.** It splits `pkg/noiseproto`
  on a `fips` build tag — `dh.go` (`//go:build !fips`) performs X25519,
  `dh_fips.go` (`//go:build fips`) refuses with `ErrFIPSUnsupported`. That tag
  is the mechanism by which the Noise finding below gets closed. It is not
  closed here: this module's FIPS job does not pass `-tags fips`, so it still
  compiles the `!fips` variant.

### Fixed

- **`make test` sets an explicit `-timeout=30m`.** It previously set none and
  inherited `go test`'s 10m per-package default, which `pkg/seal` had been
  living inside by a margin of ten seconds: `589.797s` on the last passing run
  against a 600s budget — 98.3% — and then `600.442s` on the next one, which
  failed. Nothing about the package changed between those two runs; it is slow
  by design (Argon2id under `-race`) and the budget was always too small for it.
  The failure shape is the reason this mattered more than the delay: the timeout
  kills the whole test binary and names whichever test was starting when the
  alarm fired, so an exhausted budget reads as a hang in an unrelated package.
  Bounded rather than removed, so a genuine deadlock still fails; the CI job
  already allows 90 minutes and `test/integration` keeps its own `-timeout=25m`,
  which still applies because a later `-timeout` wins.

### FIPS 140-3 posture

Measured on this commit — Go 1.26.6, `GOFIPS140=latest GODEBUG=fips140=only`,
`-race`, and the same build tags `make test` uses:

    122 packages: 106 pass, 16 fail
    693 GCM rejections across 15 packages
     23 X25519 rejections across 3 packages

**This bump moves no package from red to green.** The pass/fail split and the
GCM count are identical to the pre-bump measurement recorded in `ci.yml`, and
the count is stated here so the next person does not have to re-derive it to
find that out. Both remaining findings originate outside this module:

- **GCM with caller-supplied IVs** — every rejection originates in
  `go-quicraft@v0.6.2-alpha/pkg/seal` (`algorithm.go`, `strategy_software.go`).
  qrdb's own five GCM construction sites already use
  `cipher.NewGCMWithRandomNonce`, so there is no qrdb-side residue. Fixed
  upstream in `go-quicraft@v0.7.0-alpha`; taking that bump is gated on the
  wrapped-root-key dual-read migration (go-qrdb#23), because the new
  construction reports `NonceSize() == 0` and an existing sealed store would
  stop unsealing.
- **X25519 in the Noise handshake** — `pkg/clusterenroll` (7), `pkg/ceremony`
  (8) and `cmd/qrdbd` (8). Noise_NK has no approved DH to move to, so this
  never closes by moving to a newer version; it closes by Noise being absent
  from a FIPS build. `pkg/ceremony` is newly counted here: it previously failed
  on GCM before reaching the Noise path.

Neither finding is closable by editing the FIPS job, and it stays out of
`ci-gate` while they stand. Nothing in this repository claims the module
operates under `fips140=only`.

## [0.6.0-alpha] - 2026-08-18

MINOR, not PATCH: pre-1.0 this is where breakage lives, and this release moves
the derivation of persisted role and custodian identifiers, tightens the
multicast shared-secret contract, and removes `statemachine.CachedDiskBackend`.

Two housekeeping defects are closed here rather than left to rot. `VERSION` had
read `0.5.7-alpha` through both the `v0.5.8-alpha` and `v0.5.9-alpha` tags — the
unreconciled-sources-of-truth failure `release-versioning.md` §2 warns about —
and neither of those tags got a CHANGELOG section, so their content was still
sitting under `[Unreleased]`. It is folded into this entry. `v0.5.9-alpha` in
particular carried the `CachedDiskBackend` removal, a breaking change published
as a PATCH; consumers that pinned back to `v0.5.8-alpha` over it should move
forward to this release.

**Consumers on `v0.5.9-alpha` or earlier should upgrade.** The UUIDv8 migration
below is what stops `pkg/role` panicking in package init under
`GODEBUG=fips140=only`; it landed after `v0.5.9-alpha` was cut, so every tagged
release before this one still takes down any downstream binary that authorizes
through `pkg/role` in a FIPS build.

### Added

- **Protected resolution targets for `ConfigProcessor` (`pkg/secret`).** A
  config field declared as `secret.SecretValue` (or `*secret.SecretValue`)
  instead of `string` is now resolved in place inside its own memguard
  enclave, so a resolved secret reaches the consumer without ever resting in
  an unwipeable Go string. `SecretValue` gained YAML/JSON codecs that decode
  from — and re-encode to — the plain reference string, so config documents
  are unchanged and a re-serialized config emits the reference rather than
  the plaintext (a non-scalar node — and JSON `null`, which is a configuration
  mistake rather than an empty secret — fails with the new
  `SecretValueDecodeError`). `Raw` is left intact by the walk.

  The protected target is deliberately stricter than a plain `string` field:
  every non-nil target the walk reaches is either resolved or reported in
  `Unresolved`, never skipped in silence. A target holding nothing (a null or
  absent key), an inline `literal://` secret, a bare value, or a scheme no
  resolver claims is refused with the new `UnresolvableTargetError` — an
  inline secret carries the plaintext in `Raw` itself, an ordinary Go string
  that outlives the walk, so sealing it would leave a second unprotected copy
  and break the guarantee the field type advertises. A target the walk cannot
  write to (boxed by value in an unsettable interface) is refused with the new
  `UnwritableTargetError`; boxed targets it can write to are resolved through
  an addressable copy and assigned back. An optional secret is spelled as a
  `*SecretValue`, where an absent or null key leaves the pointer nil — the one
  case the walk passes over. That is also what closes the YAML null gap:
  `yaml.v3` zeroes a null destination before the unmarshaler is consulted, so
  `password:` and `password: null` never reach `UnmarshalYAML` and are refused
  at the walk instead of at decode time, where JSON rejects its `null` token.

  A protected-target failure fails the call: `Process` returns
  `ErrUnresolvedSecretURI` whenever the new `ProcessResult.ProtectedUnresolved`
  is non-empty, with or without `WithFailClosed()`, because choosing the type
  is itself the request to fail closed on that field. `Process` also destroys a
  target's existing enclave before refilling it, so a failed refresh over an
  already-resolved config leaves the field empty rather than letting a revoked
  credential keep authenticating while its backend is down.

  Additive throughout: `Process`'s signature is unchanged, the deprecated
  string path keeps its exact behaviour — including that its failures stay
  opt-in diagnostics under `WithFailClosed()` — and no existing consumer
  breaks on upgrade.
- **Redacted display paths for `SecretValue` (`pkg/secret`).** `SecretValue`
  now implements `fmt.Formatter` and `slog.LogValuer`, which `fmt` and
  `log/slog` consult ahead of `fmt.Stringer`. `%v`, `%s`, `%+v`, `fmt.Sprint`
  and structured logging of a config struct holding a resolved secret therefore
  render the redacted description instead of the plaintext. New `Redacted()`
  exposes the same description directly: it shows the reference only when a
  resolver established that the reference points at a secret rather than
  carrying one, judging by provenance rather than syntax — the registry falls
  back to `StrategyLiteral` for anything no resolver claims, so a
  reference-shaped `https://user:pw@host` or `unknown://pw` is the secret in
  fact and is redacted, as is any value not yet resolved. Only an explicit
  `String()`/`Bytes()` call still materializes plaintext, and only
  serialization still emits `Raw` verbatim — reproducing the configuration
  document is what serializing it means.
- **`WithNoPlaintextStrings()` migration ratchet (`pkg/secret`).** With this
  processor option set, a plain `string` field holding a reference is left
  untouched — its secret is never fetched — and its path is reported in the
  new `ProcessResult.PlaintextSkipped` (always a subset of `Unresolved`). A
  consumer turns it on, converts the reported fields to `SecretValue` one at
  a time, and adds `WithFailClosed()` once the list is empty to refuse any
  boot that would produce a plaintext string. Both re-exported from
  `sdk/go/secret`.
- **Raft state machines over any storage backend (run-modes phase 5).**
  New `raft.sm_storage` (`--sm-storage`) selects the storage backend the
  storage-generic state machines (`sm_type: disk`/`fast`) compose:
  `pebble` (default), `file`, or `memory`, rooted per-(shard, replica)
  under the data dir. Objstore is refused as the hot apply path with a
  pointed error — it is a snapshot/backup tier. The pebble-native SMs
  (`pebble`, `mvcc`) reject a non-pebble selection.
- **File storage backend durability.** `pkg/storage/file` writes are now
  crash-safe: Set is temp-file + fsync + atomic rename + directory fsync;
  Delete fsyncs the parent; batch commits are all-or-nothing via a
  CRC-verified commit journal published before any per-key file is
  touched and replayed at open (a corrupt journal fails the open rather
  than serving a half-applied batch). New optional `storage.Syncer` seam.
  Acceptance: raft single-node survives a hard kill + restart over
  `--sm disk --sm-storage file`, and a 3-node cluster replicates over
  file-backed SM storage (test/integration/sm_storage_file_test.go).

- **Run-modes requirements ratified** ([docs/run-modes.md](docs/run-modes.md)):
  standalone = no Raft (standard database operations + always-on security
  core over any non-raft storage backend); raft (single- or multi-node) =
  MVCC/watch/lease/lock/transactions/etcd/cluster ops. Feature availability
  follows a three-gate model (build tag > run mode > config) with an
  explicit always-compiled CORE. The daemon's `mode: standalone` and the
  capabilities upgrade land in subsequent phases of the approved plan.

- **Feature profile system (run-modes phase 2).** `pkg/capabilities` now
  seals an effective per-deployment profile at boot
  (`capabilities.Seal(cfg.EffectiveCapabilities())`): the intersection of
  build tags, run mode, and config. `Capabilities` gains `mode` plus
  `lock`/`cluster`/`backup`/`rebalance`; the proto, REST/gRPC/QUIC/MCP/
  embedded transports, SDK DTO, and `qrdb admin capabilities` all carry
  the new fields (additive wire shapes). Before a composition root seals,
  `Get` keeps the historical compile-time fallback.
- **`mode` config key** (`"standalone"`/`"raft"`, empty = raft) with
  validation: under standalone, explicitly enabling `mvcc`/`watch`/
  `lease`, `raft.join`, or a multi-member `raft.peers` list fails with
  `requires mode: raft`; standalone skips the raft identity requirements
  (only `raft.data_dir` stays mandatory). New optional `enabled` toggles
  on `mvcc`/`watch`/`lease` narrow the raft profile.
- **Boot-time build gate** `Config.ValidateCapabilities()`: a wire
  protocol enabled in config but not compiled in is refused with an error
  naming the build tag. (Between phases 2 and 3 the daemon refused
  `mode: standalone` with a pointed error; phase 3 replaced the refusal
  with the real composition branch.)
- **Typed refusal hints.** REST `ServiceNotImplementedError` and the
  gRPC/QUIC/service `ServiceNotConfiguredError` gained an optional `Hint`
  appended to the message (e.g. `requires mode: raft`).

- **`mode: standalone` in the daemon (run-modes phase 3).** `qrdbd` now
  composes the raft-free data plane (`lifecycle.NewLocalStorage`/
  `NewLocalEngine` → `backend.LocalBackend`) when `mode: standalone` (or
  `--mode standalone`) is set: no raft host, no WAL, no election — boot
  drops from seconds to milliseconds. Standard database operations plus
  the always-on security core (mTLS/RBAC/IAM, seal barrier incl. encfs,
  audit) serve as in raft mode; raft-only endpoint families (MVCC
  revisions, watch, lease, lock, etcd-style Txn, cluster/backup/
  rebalance, encryption migration) are refused on every transport with
  501/Unimplemented naming the remedy `requires mode: raft`. The SQL
  catalog keeps its internal MVCC/transaction plumbing (standard ops).
- **One canonical local composition.** The three duplicate
  storage/engine composition sites (SDK node, embedded transport, and
  now the daemon) collapse into `lifecycle.NewLocalStorage`/
  `NewLocalEngine`; the embedded `database/sql` driver gains objstore
  support as a side effect.
- **SDK standalone gating (behavior change, as announced below).**
  `NewStandaloneNode` no longer exposes the MVCC/watch/lease/transaction/
  lock service families; standalone nodes report `mode: standalone` in
  capabilities with those features false. `NewClusteredNode` is
  unchanged. `service.AdminService` gains `WithCapabilityProfile` /
  `lifecycle.Infra.Profile` so each node in a multi-node process reports
  its own truth.
- REST's `capabilitiesResponse` now carries the full profile (a phase-2
  funnel gap), and REST maps `ServiceNotImplementedError` to 501 from
  registry-resolved paths (previously 500).

- **Per-mode exemplars and integration suites (run-modes phase 4).**
  `configs/qrdb-standalone-{pebble,memory,file}.yaml` exemplars;
  `configs/qrdb-standalone-mvcc.yaml` renamed to
  `qrdb-raft-single-mvcc.yaml` (it always was a single-node RAFT config —
  the old name contradicted the ratified terminology).
  `TestStandaloneMode_StandardOpsAndRefusals_PerProtocol` drives a
  `--mode standalone` daemon over all five transports: standard ops
  green, capability truthfulness, raft-only refusals; plus per-backend
  boot coverage. The unix SDK transport's capabilities mapping gained the
  new profile fields (the last funnel site). Downstream notified:
  go-quicbaas#4.

- **Release tag lockstep check.** `make check-tag-lockstep`
  (`scripts/check-tag-lockstep.sh`) checks release-versioning.md P2: every
  root `vX.Y.Z` tag must have an `sdk/go/vX.Y.Z` tag at the same commit, and
  no `sdk/go` tag may exist without its root. The new `Release Lockstep`
  workflow runs it on version-tag pushes in both namespaces (`v*` and
  `sdk/go/v*`) — within a minute of publication, rather than days later in a
  consumer's dependency bump — and re-audits the whole tag set on every merge
  to `main`. It detects a skewed release; it
  cannot prevent one, because a tag is visible the moment it is pushed
  (go-qrdb#13 tracks enforcement). The preventive step is running the check
  before publishing both tags in one atomic push
  (`git push --atomic origin vX.Y.Z sdk/go/vX.Y.Z`). `make test-tag-lockstep` covers
  the rejection branches against throwaway repositories, since this
  repository's own tag set is permanently valid and exercises only the
  accepting branch. `v0.5.9-alpha` was tagged on the root module alone, so
  `go get` at latest resolved the root to `v0.5.9-alpha` and `sdk/go` to
  `v0.5.8-alpha` — a pair that does not compile, because the older `sdk/go`
  still aliased the symbols removed below. The tag has since been cut.
  Reported by go-xkms (#6).

### Deprecated

- **Plaintext-into-`string` secret resolution (`pkg/secret`).** Resolving a
  secret into a plain `string` config field is the plain path the stack is
  moving off; use a `SecretValue` field target, and
  `WithNoPlaintextStrings()` to find the fields that still need converting.
  `SecretValue.String()` and `SecretValue.Bytes()` are likewise deprecated in
  favour of `WithBytes`/`Open` (and `Redacted()` for anything displayed).
  Nothing is removed and the deliberate accessors keep their behaviour: the
  exported signatures stay as they are until the stack's coordinated breaking
  release, so the whole dependency chain can move in one wave rather than repo
  by repo. `String()` still returns plaintext because it is the documented way
  to hand a secret to an API that takes a string, and silently redacting it
  would break those callers at run time with no compile error; the accidental
  paths (`%v`, `fmt.Sprint`, structured logging) are redacted now, as noted
  above, so what remains deferred is only the deliberate call.

### Removed

- **`statemachine.CachedDiskBackend` and `NewCachedDiskBackend`**, with
  their `sdk/go` aliases — a breaking change to an exported API, shipped
  in `v0.5.9-alpha`. Both were a reserved placeholder: a methodless struct
  no registry ever provided, so the `cached-disk` SM backend passed config
  validation and then failed at boot. There is no replacement and nothing
  to migrate — no build that compiled against these symbols was running a
  cached disk backend. The disk-backed path is `sm_type: disk`, now
  composable over any storage backend via `raft.sm_storage`.

### Fixed — SECURITY

- **`GOFIPS140=off` was read as a FIPS request.** SO PIN hashing in
  `pkg/auth` picked its KDF with `os.Getenv("GOFIPS140") != ""`, so the one
  value that explicitly means "not FIPS" selected PBKDF2-SHA256, and
  `crypto/fips140.Enabled()` was never consulted at all. Every
  FIPS-conditional path in the tree now routes through a single predicate,
  `seal.FIPSEnabled` (re-exported from `go-quicraft/pkg/seal`): the Go
  runtime's FIPS 140-3 module state first, `GOFIPS140` as an environmental
  fallback, and `off` honoured as disabled. `pkg/seal/kdf.DefaultHasher`
  loses the same env-only test, and `pkg/pin.AutoDetectHashConfig` — which
  was blind to the environment entirely — now honours a `GOFIPS140` FIPS
  request. Stored hashes are unaffected: each record carries its own
  algorithm/version tag and verification still routes by that tag, so a
  hash written in either mode continues to verify in the other.

- **A resolver reporting success with no value is now refused
  (`pkg/secret`).** `SecretResolver` requires a non-nil `SecretValue` on
  success, but `ResolverRegistry.Resolve` did not enforce it: a resolver
  returning `(nil, nil)` panicked the plain string path on the nil
  dereference, and would have let a protected target be counted as resolved
  while holding nothing. The registry now fails such a resolver with
  `ResolutionFailedError`, so the path is recorded in `Unresolved` and
  `WithFailClosed()` refuses the boot.
- **`ConfigProcessor` no longer destroys a `SecretValue` config field.** The
  walker descended into a `SecretValue`, saw its exported `Raw` reference as
  an ordinary resolvable string field, and overwrote it with the resolved
  plaintext while leaving the enclave empty — making the documented "hold the
  secret in a `SecretValue`" pattern strictly worse than a plain `string`
  field. A `SecretValue` is now a protected leaf the walk resolves into and
  never descends through.

### Changed — protected memory

- **The live cluster DEK is held in mlocked memory, and never copied out.**
  `dek.ClusterDEKManager` kept the plaintext cluster DEK in an ordinary
  `[]byte` for the manager's whole lifetime. It is now a `*secure.Buffer`
  (mlocked, guard-paged, wiped on destroy): the key is generated straight
  into protected memory rather than being copied there, and the superseded
  buffer is destroyed on `Rotate` and on `Close`.

  **BREAKING (service seam):** `ClusterDEKManager.GetCurrentDEK() ([]byte,
  error)` is gone, and no exported API replaces it: the key is now applied
  inside the manager by `WrapKey(plaintext []byte) ([]byte, error)` and
  `UnwrapKey(wrapped []byte) ([]byte, error)`, which perform the AES-256-GCM
  key wrapping under the manager's state lock. Neither a heap-backed copy nor
  a slice aliasing protected memory crosses the API boundary, so no caller
  can retain the key past a call or mutate the live key.
  `ClusterDEKManager.CurrentVersion()` is the checked
  counterpart of `GetDEKVersion` for callers that must distinguish
  "version 0" from "no DEK". Correspondingly,
  `service.DEKServicer.GetCurrentDEK(ctx) ([]byte, uint64, error)` becomes
  `CurrentDEKVersion(ctx) (uint64, error)`: the DEK bytes never crossed that
  boundary usefully — the embedded transport already discarded them, and no
  transport exposes DEK material.

### Changed — FIPS 140-3 identifiers

- **Deterministic role and custodian identifiers moved from UUIDv5 (SHA-1) to
  RFC 9562 §5.8 UUIDv8 over SHA-256.** `crypto/sha1` panics under
  `GODEBUG=fips140=only`, and both derivations run during package
  initialization and tenant seeding, so before this change a FIPS build died
  before a single test body ran — including in any downstream binary that
  authorizes through `pkg/role`. UUIDv8 is the version reserved for
  application-defined derivations, and it is the only one available: RFC 4122
  fixes v3 to MD5 and v5 to SHA-1, so no versioned name-based UUID can carry an
  approved hash. Inputs are length-prefixed before hashing so the encoding is
  injective — unframed, tenant `a/b` with role `c` and tenant `a` with role
  `b/c` would collide on one identifier. The 36-character UUID text shape is
  unchanged, so no index key or wire format reformats.

  **These identifiers are persisted data**, so the change is versioned rather
  than a flag day: writes emit only the v8 form and reads accept both. A
  cluster seeded by an older build keeps its v5 identifiers, and code holding a
  tenant plus a role NAME must not re-derive — on such a cluster the v8 value
  names no record at all. Use `role.ResolveSystemRoleID`, which answers from
  the store's name index and falls back to the canonical value, and
  `role.SystemRoleIDs`, which returns every identifier denoting the role so
  that the SO deny-list and the separation-of-duties Owner strip still match a
  grant made before the migration. Writing a canonical identifier straight into
  a legacy cluster's user records produces a grant that resolves to no role and
  silently strips the user's permissions.

### Fixed — release integrity

- **Every previously published tag recorded an unresolvable requirement for its
  own SDK submodule.** The root `go.mod` recorded
  `github.com/jeremyhahn/go-qrdb/sdk/go v0.0.0-00010101000000-000000000000`, a
  placeholder that resolves to nothing. A `replace` directive only applies in
  the main module, so the in-repo `=> ./sdk/go` masked it for every check run
  inside this repository — local builds, CI, `go list`, module-zip inspection —
  while a downstream consumer got:

  ```
  github.com/jeremyhahn/go-qrdb/sdk/go@v0.0.0-00010101000000-000000000000:
      invalid version: unknown revision 000000000000
  ```

  `pkg/ceremony`, `pkg/api/rest` and `cmd/qrdb` import `sdk/go/transport`, so
  the failure is reachable rather than theoretical: a scratch module that does
  `go get github.com/jeremyhahn/go-qrdb@v0.5.9-alpha` and imports
  `pkg/ceremony` cannot even `go mod tidy`. Reproduced with `GOWORK=off`.

  **Why nobody hit it.** Every current consumer — go-xkms, go-quicbaas,
  go-trusted-platform — carries its own explicit
  `require github.com/jeremyhahn/go-qrdb/sdk/go` line at a real version,
  because each also imports `sdk/go/transport` directly. Their own requirement
  satisfies the import, and the root module's nonsense version is never
  consulted. The defect therefore bites a NEW consumer that reaches a root
  package without independently depending on the SDK, which is precisely what
  the mandated throwaway-consumer check constructs. Latent, not dormant.

  The reciprocal pin was stale in the same way: `sdk/go` required the root at
  `v0.4.0-alpha`, five minor versions behind.

  Both cross-module requirements now name the release they ship in
  (`v0.6.0-alpha`), which the lockstep tagging rule guarantees exists — the
  root and `sdk/go` tags are pushed atomically. The `replace` directives stay,
  because in-repo development must still build against the working tree rather
  than a published copy of its own code.

  This is the defect `release-versioning.md` §2 mandates a throwaway-consumer
  check for, and the reason that check is mandatory: no test that runs inside
  the repository can see it. It is also the same failure that condemned
  `v0.2.0-alpha`, which means it regressed and went unnoticed for many
  releases.

### Fixed — tests

- **`TestDefaultHasher_NonFIPSMode` pinned the toolchain instead of the
  selector.** It cleared `GOFIPS140` and asserted the literal Argon2id version,
  but `seal.FIPSEnabled` consults `crypto/fips140.Enabled` before falling back
  to the variable, and `GOFIPS140` selects the module at BUILD time — `t.Setenv`
  cannot retract at run time what the linker already decided. The expectation
  now routes through the `wantVersion` helper the package's other FIPS-selection
  tests already use. No coverage is dropped and nothing is skipped: the test
  asserts Argon2id on an ordinary run and PBKDF2-SHA256 under the FIPS job,
  which is the correct answer for a FIPS binary because Argon2id is not an
  approved KDF. This was the last go-qrdb-side failure under
  `GODEBUG=fips140=only`; the remainder are upstream (see
  `docs/seal/fips140-mode.md`).

## [0.5.8-alpha] - 2026-08-17

### Added

- **Live ABAC policy reload.** `pkg/auth/abac.Decorator` now supports
  `Reload(policies []*abac.Policy) error`: the new set is compiled and
  validated before an atomic pointer swap, so a failed reload leaves the
  previous policy set fully in force and `Authorize` reads lock-free under
  concurrent reloads. `abac.WithLiveReload` keeps the decorator reloadable
  even when zero policies are configured at construction. `sdk/go/node.Node`
  exposes the seam via `WithABACLiveReload` / `Node.ABACPolicyReloader()`;
  `pkg/auth/wiring.AuthorizerConfig`/`AuthMiddlewareConfig` gained an
  `Authorizer` override field so the embedded in-process transport and every
  network transport's `AuthMiddleware` share one authorizer instance, and a
  reload reaches all of them identically. `sdk/go/types_abac.go` re-exports
  `ABACPolicyReloader` and `ABACWithLiveReload`.

## [0.5.7-alpha] - 2026-08-17

### Fixed — SECURITY-ADJACENT

- **Module-registered public routes serve anonymously.** The REST
  middleware short-circuited only its hardcoded built-in allowlist before
  authentication; a module's `HandlePublic` routes (WebAuthn
  registration/login ceremonies, OIDC discovery) still demanded a peer
  certificate once `client_auth` was enabled, breaking every anonymous
  first-contact ceremony. Public registry routes now short-circuit
  pre-authentication exactly like the built-ins.

## [0.5.6-alpha] - 2026-08-17

### Added

- **Pre-resolved scope permissions** (`auth.Identity.ScopePermissions`):
  a TRUSTED in-process issuance path (a module authentication fallback
  validating a platform token) may carry the token's `scp`-derived
  `resource:action` set on the identity. The RBAC authorizer unions them
  with store-resolved role permissions — additive only, SO deny-list
  still overrides — so client-credential/app tokens whose subject has no
  user record authorize for exactly what the token was issued for. Never
  wire-deserialized; the on-behalf path constructs fresh identities and
  cannot carry them.

## [0.5.5-alpha] - 2026-08-17

### Changed

- **Built-in routes yield to module registrations.** The documented
  "modules register first and take precedence" contract is now formal:
  a built-in route whose method+pattern a capability module already
  claimed is skipped (debug-logged) instead of colliding. Module-vs-
  module duplicates still fail construction loudly.
- qrdbd auth bootstrap no longer opens an unchained `audit.log` side
  file (the decision bridge made it obsolete); a nonexistent data dir no
  longer fails auth bootstrap.

## [0.5.4-alpha] - 2026-08-16

### Added

- SDK audit-record aliases (`qrdbsdk.NodeAuditEvent` / `NodeAuditFilter` /
  `NodeAuditPage`): composing hosts emit into the node's chained audit
  sink through the SDK boundary without importing `pkg/audit`.

## [0.5.3-alpha] - 2026-08-16

### Added

- **Module authentication fallback** (`apimodule.ModuleAuthFallback`, SDK
  `qrdbsdk.ModuleAuthFallback`): a capability module whose HTTP surface
  accepts credentials qrdb's own authenticators do not understand (a
  session-token plane) may authenticate the request when the host chain
  produces no identity. The fallback identity flows through the SAME
  store-resolved enrichment and the ONE authorizer as an mTLS identity.
- **REST route-collision guard**: registering the same method+pattern
  twice (two modules, or a module and the built-in surface) fails
  construction with a descriptive panic instead of silently shadowing.

### Changed — one audit plane

- qrdbd's per-decision auth audit now rides the tamper-evident chained
  event log via the decision bridge — identical wiring to embedded nodes;
  the unchained side file is gone.
- The legacy second audit system (`audit.New`, kv/file/multi backends,
  the `audit:` config block, `DaemonAuditConfig`) is DELETED. The chained
  `AuditLogger` (Emit/Query) is the only audit sink; the `Logger`/`Entry`
  middleware contract remains as the decision-bridge interface.
- Audit wire tags are snake_case (`tenant_id`, `client_ip`,
  `request_id`, `prev_hash`), closing the documented cross-project
  serialization mismatch.

## [0.5.2-alpha] - 2026-08-16

### Changed — SECURITY

- **Authentication is now the zero value.** `AuthConfig.Enabled` became a
  pointer (`*bool`) with `IsEnabled()`: an embedder that builds a
  `DaemonConfig` literal without touching the Auth block gets an
  AUTHENTICATED unified server; only an explicit `false` disables. This
  closes a live regression where both downstream daemons (xkmsd,
  quicbaasd) served every qrdb-native plane unauthenticated because the
  plain-bool zero value silently disabled the middleware. Use
  `config.BoolPtr` / `qrdbsdk.BoolPtr` for explicit values.

### Added

- **Per-decision audit on embedded nodes** (`audit.NewDecisionLogger`):
  the auth middleware's decision trail now bridges onto the node's
  chained compliance event log — authorization decisions land in the
  same tamper-evident chain as every other audited operation, matching
  qrdbd. Closes the documented SDK-node TODO.

## [0.5.1-alpha] - 2026-08-16

### Fixed

- **SDK error exports restored**: `sdk/go/types_errors.go` was accidentally
  truncated in 0.5.0-alpha to only the new `NotLeaderError` re-export,
  dropping `QRDBError`, `ErrorCode`, the error constructors and every
  other error alias. The full surface is restored with `NotLeaderError`
  appended. 0.5.0-alpha should not be depended upon.

## [0.5.0-alpha] - 2026-08-16

### Added

- **ABAC authorization decorator** (`pkg/auth/abac`): attribute-based
  access control layered over the RBAC authorizer, porting the core of
  go-quicbaas's IAM policy engine (YAML policy schema, compiler, resource
  trie, condition parser, evaluator) into its canonical qrdb home. Hybrid
  semantics: explicit deny overrides RBAC allow, explicit allow grants
  beyond RBAC, unmatched requests delegate to RBAC. Policies are
  tenant-scoped; the reserved system tenant's policies apply platform-wide
  as guardrails. Enabled in qrdbd via `auth.abac.{enabled,policy_dir}`
  (fail-closed validation and boot) and in embedded nodes via
  `node.WithABACPolicyDir` / `node.WithABACPolicies`. SDK re-exports in
  `sdk/go/types_abac.go` (`ABACPolicy`, `DecorateAuthorizer`, ...) plus
  `qrdbsdk.DaemonAuthABACConfig`. Policies load once at construction;
  hot-reload is a documented follow-up. See `docs/auth/abac.md`.

- **Embedded-node auth enforcement** (`pkg/auth/wiring`): the SDK node
  (`NewStandaloneNode`/`NewClusteredNode`) now constructs the SAME auth
  middleware qrdbd builds — RBAC authorizer (+ABAC decoration), mTLS/SPKI
  authenticator, adaptive/enforced mode — and passes it to the unified
  multi-protocol server. Previously an embedded node served every
  permission-gated route unauthenticated. `BuildAuthMiddleware` /
  `BuildAuthorizer` are shared by `cmd/qrdbd` and the SDK node (one
  construction path, no divergence).

- **Per-RPC public affordance for module gRPC services**
  (`apimodule.GRPCPublicMethodLister`, re-exported as
  `qrdbsdk.ModuleGRPCPublicMethodLister`): modules may declare exact gRPC
  methods that bypass authentication, symmetric with REST's
  `HandlePublic`. Registered method names are validated at startup
  (typed error on malformed names). Built-in health/reflection exemptions
  now genuinely bypass authentication rather than only the permission
  lookup.

- **Module MCP tool permissions** (`apimodule.MCPToolPermissioner`): the
  unified MCP server consumes per-tool permission declarations from
  registered modules.

### Fixed

- **Sealed store crash durability (data-loss grade)**: the seal store —
  the only persisted copy of the barrier root key and epoch state — was
  opened with `SyncWrites: false` (a rationale valid only for
  Raft-replicated stores). A crash could lose the root-key write; the
  next boot silently minted a fresh root key, permanently orphaning every
  file encrypted under the lost key (`quicraft/seal: decryption failed`
  on the data-plane and raft snapshot stores). All seal/DEK stores now
  force `SyncWrites: true`; SIGKILL-subprocess regression tests prove a
  killed node reboots and serves its data (`pkg/seal/encfs`,
  `sdk/go/node`).

## [0.4.0-alpha] - 2026-08-15

### Added

- **ServiceModule MCP tool authorization** (`apimodule.MCPToolPermissioner`):
  a module that registers MCP tools may now declare each tool's required
  permission; the unified server feeds the declarations into the same MCP
  tool registry the built-in tools use, so module tool calls are authorized
  by the one middleware path instead of being unconditionally denied. A
  module tool registered without a declaration remains fail-closed.
- **SDK re-exports for module hosts**: `qrdbsdk.WithServerTLS` (the
  TLS-only transports' certificate source, required by hosts that terminate
  TLS with their own material), `qrdbsdk.NewModuleHTTPRegistrar` (capture
  registrar for module tests), `qrdbsdk.ModuleMCPToolPermissioner`, and
  `qrdbsdk.PermissionIdentityGated` (the identity-gated sentinel).

### Changed

- `github.com/jeremyhahn/go-quicraft` pinned to `v0.4.2-alpha`
  (`batch.EncodeBatch` returns `([]byte, error)`; the error is propagated
  through `pkg/batch`).

### Fixed

- `pkg/api/mcp` default-tag test builds: the shared `CredentialServicer`
  mock lived in a file constrained to the full feature-tag set while an
  untagged test file used it; the mock now lives untagged so every tag
  combination compiles.

## [0.3.0-alpha] - 2026-08-14

### Added — the unified API server seam

Phases 0-2 and phase 3 wave 1 of
`software/plans/unified-api-server-2026-08-14.md`, the work that lets a host
process expose go-qrdb's own control plane instead of reimplementing it per
transport. New exported API, so MINOR.

- `pkg/apimodule`: `ServiceModule` and `BaseModule` with `Start`/`Stop`, a
  duplicate-name check, and a startup assertion that every registered route
  carries a permission. `WithModule` fans a module out across the transports.
  Re-exported from `sdk/go`. Additive — nothing existing changes shape.
- The control plane's own admin, rebalance and user-auth-factor surfaces are
  served through the seam.
- `pkg/sql/privilege`: GRANT, REVOKE and SHOW GRANTS parsing, rewriting and
  enforcement over `service.PrivilegeService`, plus `pkg/sql/pgcatalog`.

### Changed

- REST runs on chi/v5 v5.3.0. SSE `/watch`, on-behalf-of and the cluster-peer
  exempt routes are unchanged, gated by the pre-existing watch and
  barrier-authz tests passing unmodified.
- One authorization path: `authz.proto` carries `option (auth.permission)`, a
  reflection interceptor reads it, and `Perm_*` constants replace the five
  hand-maintained permission tables. Default-deny, the `IdentityGated`
  sentinel, the public-route allowlists and on-behalf-of handling are retained.
  QUIC and MCP gain method registries with mapping guard tests and REST a route
  guard, so a handler added without a permission now fails a test instead of
  shipping unauthenticated.

### Removed

- `pkg/auth/middleware/mapping.go` and the five permission tables it fed.
- The `pkg/sql/internal/privprobe` scaffolding, superseded by `pkg/sql/privilege`.

## [0.2.1-alpha] - 2026-08-14

### Fixed

- The two in-repo modules pinned each other at `v0.1.0-alpha`, so a consumer
  taking `sdk/go v0.2.0-alpha` resolved the parent module to the release that
  still had Avro, and the build failed with `undefined: codec.Avro` — the new
  SDK pulls go-codec v1.4.0, which the old parent's `pkg/dao` cannot compile
  against. Both modules carry a local-path `replace` at each other, which
  applies only in a main module, so every in-repo build resolved through the
  working tree and nothing surfaced the stale requirement. `sdk/go` now
  requires the parent at `v0.2.1-alpha` and the root requires `sdk/go` at the
  same version.

`v0.2.0-alpha` and `sdk/go/v0.2.0-alpha` are superseded and must not be
pinned. The tags remain, per the immutability rule.

## [0.2.0-alpha] - 2026-08-14

### Removed — BREAKING: the Avro codec is gone from both modules

`github.com/hamba/avro` is archived upstream and carries reachable advisories
GO-2026-5046, GO-2026-5047 and GO-2026-5048 with no fix forthcoming and no
production-grade replacement, so Avro leaves the product stack entirely (owner
decision, 2026-08-14; the same decision produced go-codec v1.4.0). These
exported identifiers no longer exist:

- `pkg/codec`: `TypeAvro`, `AvroCodec`
- `pkg/dao`: `CodecAvro`

The `codec_avro` build tag and the `WITH_CODEC_AVRO` Makefile switch are gone
with them, as are the `.avro` and `.avsc` entries in extension-based format
detection and Avro's membership in `probeableCodecs`. A caller that selected
Avro must choose another codec; the JSON, YAML, TOML, MsgPack, BSON, CBOR and
ProtoBuf codecs are unaffected.

### Added

- `cmd/qrdb` and `cmd/qrdbd` sources, and the Wails frontend, are actually in
  the repository. They had never been committed: the root `.gitignore` carried
  unanchored `qrdb` and `qrdbd` patterns intended for the compiled binaries,
  and a gitignore pattern without a slash matches at any depth and matches
  directories, so both command trees — 222 Go files — were excluded. `HEAD` did
  not build.
- `workflow_dispatch` trigger on the CI workflow (ci-template v1.3), so a run
  lost to a runner crash or host overload can be retried without a commit.

### Changed

- `github.com/jeremyhahn/go-codec` pinned to `v1.4.0`, the Avro-free release.
- `github.com/jeremyhahn/go-quicraft` pinned to `v0.3.1-alpha`.
- `golang.org/x/crypto` to `v0.55.0` and `golang.org/x/text` to `v0.41.0` in
  the `sdk/go/gorm/conformance` module, clearing ten HIGH advisories
  (CVE-2026-39828 through CVE-2026-46597, and CVE-2026-56852).

### Fixed

- HTTP/3 listener errors were compared against `http.ErrServerClosed` with
  `!=`, and the pgsql row collector compared against `io.EOF` with `==`; a
  wrapped sentinel from either was mishandled. Both now use `errors.Is`.
- The eight registry mocks in the transport and SQL provider tests implement
  `service.Registry.PrivilegeService`, and the MVCC error stubs are referenced
  by the names they actually have. Without these no test binary in the
  affected packages compiled, which also made `gosec` exit non-zero after
  reporting no findings.
- `TestRunStart_OpenAll_DatabasesDirPermissionError` provoked its failure with
  a 0o000 directory, which root ignores; under CI it started a real node and
  hung until the test binary's deadline. It now uses a regular file where the
  directory belongs, failing with `ENOTDIR` for root and non-root alike.
- `TestNewStartCmd_UnknownSM_NoConfig` read the package directory as its data
  directory, picking up a stray committed `data/ca.pem` and failing before it
  reached the assertion it was written for.

### Security

- `cmd/qrdbd/data/` is ignored. A locally run daemon issues itself a CA, node
  and admin key there, and the directory was previously covered only as
  collateral of the unanchored `qrdbd` pattern.
- Playwright's generated `playwright-report/` and `test-results/` output no
  longer ships in the repository.

[Unreleased]: https://github.com/jeremyhahn/go-qrdb/compare/v0.17.0-alpha...HEAD
[0.17.0-alpha]: https://github.com/jeremyhahn/go-qrdb/compare/v0.16.3-alpha...v0.17.0-alpha
[0.6.0-alpha]: https://github.com/jeremyhahn/go-qrdb/compare/v0.5.9-alpha...v0.6.0-alpha
[0.3.0-alpha]: https://github.com/jeremyhahn/go-qrdb/compare/v0.2.1-alpha...v0.3.0-alpha
[0.2.1-alpha]: https://github.com/jeremyhahn/go-qrdb/compare/v0.2.0-alpha...v0.2.1-alpha
[0.2.0-alpha]: https://github.com/jeremyhahn/go-qrdb/compare/v0.1.1-alpha...v0.2.0-alpha
