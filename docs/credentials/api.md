# Credentials — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-11-01
> Conflicts with this document are defects. Fix them here first.

gRPC service `qrdb.v1.QRDBService`. The Unix socket transport serves the same
gRPC wire over a UDS, so its methods match the gRPC column.

## Capability matrix

| Capability | REST | gRPC / Unix | QUIC | MCP tool | Embedded (`Client.Credentials()`) | `scp` permission |
|---|---|---|---|---|---|---|
| Store | `POST /v1/credentials/store` | `CredentialStore` | `credential.Store` | `qrdb_credential_store` | `CredentialStore` | `credentials:store` |
| Submit (manual) | `POST /v1/credentials/submit` | `CredentialSubmit` | `credential.Submit` | `qrdb_credential_submit` | `CredentialSubmit` | `credentials:submit` |
| Retrieve current | `GET /v1/credentials/{name}/value` | `CredentialRetrieve` | `credential.Retrieve` | `qrdb_credential_retrieve` | `CredentialRetrieveBuffer` | `credentials:retrieve` |
| Retrieve version | `GET /v1/credentials/{name}/versions/{version}` | `CredentialRetrieveVersion` | `credential.RetrieveVersion` | `qrdb_credential_retrieve_version` | `CredentialRetrieveVersionBuffer` | `credentials:retrieve_version` |
| List versions | `GET /v1/credentials/{name}/versions` | `CredentialListVersions` | `credential.ListVersions` | `qrdb_credential_list_versions` | `CredentialListVersions` | `credentials:list_versions` |
| Rotate | `POST /v1/credentials/{name}/rotate` | `CredentialRotate` | `credential.Rotate` | `qrdb_credential_rotate` | `CredentialRotate` | `credentials:rotate` |
| Set rotation policy | `PUT /v1/credentials/{name}/rotation-policy` | `CredentialSetRotationPolicy` | `credential.SetRotationPolicy` | `qrdb_credential_set_rotation_policy` | `CredentialSetRotationPolicy` | `credentials:rotate` |
| Read rotation policy | `GET /v1/credentials/{name}/rotation-policy` | `CredentialRotationPolicy` | `credential.RotationPolicy` | `qrdb_credential_rotation_policy` | `CredentialRotationPolicy` | `credentials:rotate` |
| GC expired versions | `POST /v1/credentials/{name}/gc` | `CredentialGCExpired` | `credential.GCExpired` | `qrdb_credential_gc_expired` | `CredentialGCExpired` | `credentials:gc_expired` |
| Delete | `DELETE /v1/credentials/{name}` | `CredentialDelete` | `credential.Delete` | `qrdb_credential_delete` | `CredentialDelete` | `credentials:delete` |
| List names | `GET /v1/credentials` | `CredentialList` | `credential.List` | `qrdb_credential_list` | `CredentialList` | `credentials:list` |
| Active strategy | `GET /v1/credentials/strategy` | `CredentialStrategy` | `credential.Strategy` | `qrdb_credential_strategy` | `CredentialStrategy` | `credentials:strategy` |
| List migration conflicts | `GET /v1/credentials/migration-conflicts` | `CredentialMigrationConflicts` | `credential.MigrationConflicts` | `qrdb_credential_migration_conflicts` | `CredentialMigrationConflicts` | `credentials:list` |
| Resolve a migration conflict | `POST /v1/credentials/{name}/migration-resolve` | `CredentialResolveMigrationConflict` | `credential.ResolveMigrationConflict` | `qrdb_credential_resolve_migration_conflict` | `CredentialResolveMigrationConflict` | `credentials:rotate` |

**A `{name}` holding `/` or `%` is escaped twice in a REST path.** The server
routes and authorizes on the once-decoded path, so `{name}` must still be one
segment after that decode: escape `%` as `%25` and `/` as `%2F`, then
percent-encode the result as a path segment. `db/main` travels as
`db%252Fmain` and `a%b` as `a%2525b`; the server unescapes the segment once
more to get the name, and answers 400 when that fails. The Go SDK's REST
transport does this for you; the other transports carry the name unchanged.

**Every transport handler routes through the same
`service.CredentialServicer`**, so behaviour cannot diverge between them.

**Every operation acts on the caller's own store**, chosen from the tenant of
the authenticated principal: the platform's store for a system principal, the
tenant's own store, over its dedicated barrier, for a tenant principal. No
request field names a tenant, so no caller can reach another tenant's store. A
tenant's store reports the `barrier` strategy. See
[architecture.md](architecture.md).

## Schemas

Store / submit:

```json
{ "name": "smtp-password", "value": "<bytes>" }
```

Rotate:

```json
{ "name": "smtp-password", "new_value": "<bytes>", "grace_period_seconds": 3600 }
```

`grace_period_seconds: 0` wipes the previous version immediately. The response
carries `previous_version`, `new_version` and, with a grace period,
`grace_until_unix`.

Set rotation policy (the name travels in the path on REST and in the body
elsewhere):

```json
{ "name": "smtp-password", "enabled": true, "interval": "720h", "retain_versions": 3 }
```

`interval` is a Go duration string. An enabled policy needs a positive
`interval` and `retain_versions` of at least 1; `retain_versions` counts the
current version, so `3` keeps the current value and two predecessors. A
disabled policy (`"enabled": false`) clears the schedule. The `manual`
strategy refuses every policy: it cannot mint a value. Set returns 204 on REST
and `google.protobuf.Empty` on gRPC.

Read rotation policy returns the same fields plus `next_rotation`, the RFC 3339
UTC instant of the next scheduled rotation, omitted when nothing is scheduled:

```json
{ "name": "smtp-password", "enabled": true, "interval": "720h0m0s",
  "retain_versions": 3, "next_rotation": "2026-11-29T12:00:00Z" }
```

A credential with no policy reads back `"enabled": false`.

Retrieve version: `version` of **`0` means the current version**.

`CredentialVersionInfo` entries carry `version`, `is_current`,
`created_at_unix` and `grace_until_unix`, the expiry implied by the rotation
grace period.

List returns **names only**, never values.

Strategy returns `manual` or `barrier`.

List migration conflicts returns the credentials whose copy on the serving
node differs from the replicated record (architecture.md, "Prefix
migration"), sorted by name, each with the parts that differ. It never
carries a value:

```json
{ "conflicts": [ { "name": "smtp-password", "value": true, "versions": false, "policy": false } ] }
```

The list is the serving node's own: each node reports only the copies it
kept, so an operator asks each node in turn.

Resolve a migration conflict (the name travels in the path on REST and in the
body elsewhere):

```json
{ "name": "smtp-password", "resolution": "import" }
```

`resolution` is `import`, which writes the node's kept current value as a new
version of the replicated record, retaining the version it replaces, or
`discard`, which keeps the replicated record. Either way the node's kept
records for the name are deleted. Resolve returns 204 on REST and
`google.protobuf.Empty` on gRPC.

## Status and error mapping

| Condition | REST | gRPC / Unix |
|---|---|---|
| Success | 200; 204 for store, submit, delete, set rotation policy and resolve | `OK` |
| Resolve of a name with no migration conflict on the serving node | 404 | `NOT_FOUND` — `ErrMigrationConflictNotFound` |
| Resolution other than `import` or `discard` | 400 | `INVALID_ARGUMENT` — `ErrInvalidMigrationResolution` |
| Credential not found | 404 | `NOT_FOUND` |
| Version expired or never existed | 404 | `NOT_FOUND` |
| Submit of a name already submitted under `manual` | 409 | `ALREADY_EXISTS` — `ErrCredentialAlreadySubmitted` |
| Empty name, empty value or negative grace period | 400 | `INVALID_ARGUMENT` |
| Refused rotation policy (non-positive or unparsable interval, `retain_versions` below 1, any policy under `manual`) | 400 | `INVALID_ARGUMENT` — `InvalidRotationPolicyError` |
| Retrieve under `manual` with nothing submitted | **blocks** until submitted or the context ends | same |
| Barrier sealed under the `barrier` strategy, or the caller's tenant barrier sealed | 503 | `UNAVAILABLE` |
| Caller's tenant has no dedicated barrier (`prefix` isolation) | 501 | `UNIMPLEMENTED` — `CapabilityUnsupportedError` |
| Enumeration on a strategy that cannot | 501 | `UNIMPLEMENTED` — `ErrEnumerationNotSupported` |
| Missing permission | 403 | `PERMISSION_DENIED` |

**The blocking read is deliberate.** Returning an empty value would let a boot
continue with no credential at all, which is exactly the failure the `manual`
strategy exists to prevent.

## Secret resolution SPI

An application embedding qrdb runs `ConfigProcessor` over its own configuration
at boot, resolving `credential://` references into **enclave-backed
`SecretValue` targets rather than plain strings**; `qrdbd`'s own configuration
carries no reference. A protected field whose reference does not resolve, or
that holds an inline `literal://` value, **fails closed**.

This is a **Tier 2, composition-plane** surface: it has an `sdk/go` alias file
and a documented functional option, and no wire endpoint — configuration is
resolved in process at startup, so there is nothing for a remote caller to
call. Scheme ownership across the stack is in
[credentials/secret-resolution.md](../credentials/secret-resolution.md).

## Pagination

`CredentialList` and `CredentialListVersions` return complete sets and are not
paginated. Both are bounded by operator action.

## Versioning

Credential REST paths are under `/v1/`. **No wire endpoint is deprecated.**

The Go surface is Buffer-only: `CredentialRetrieveBuffer` and
`CredentialRetrieveVersionBuffer` return a protected-memory `*secure.Buffer`
the caller must `Destroy`, and no exported Go interface has a `[]byte`
retrieve method. The Buffer forms ride the `CredentialRetrieve` and
`CredentialRetrieveVersion` wire operations under the `credentials:retrieve` /
`credentials:retrieve_version` permissions. The retrieve responses are the one place
on the wire where a credential value travels as bytes; see
[Protected-memory boundary](architecture.md#protected-memory-boundary).
