# Credentials — API

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-07

gRPC service `qrdb.v1.QRDBService`. The Unix socket transport serves the same
gRPC wire over a UDS, so its methods match the gRPC column.

## Capability matrix

| Capability | REST | gRPC / Unix | QUIC | MCP tool | Embedded (`Client.Credentials()`) | `scp` permission |
|---|---|---|---|---|---|---|
| Store | `POST /v1/credentials/store` | `CredentialStore` | `credential.Store` | `qrdb_credential_store` | `CredentialStore` | `credentials:store` |
| Submit (manual) | `POST /v1/credentials/submit` | `CredentialSubmit` | `credential.Submit` | `qrdb_credential_submit` | `CredentialSubmit` | `credentials:submit` |
| Retrieve current | `GET /v1/credentials/{name}/value` | `CredentialRetrieve` | `credential.Retrieve` | `qrdb_credential_retrieve` | `CredentialRetrieve` | `credentials:retrieve` |
| Retrieve version | `GET /v1/credentials/{name}/versions/{version}` | `CredentialRetrieveVersion` | `credential.RetrieveVersion` | `qrdb_credential_retrieve_version` | `CredentialRetrieveVersion` | `credentials:retrieve_version` |
| List versions | `GET /v1/credentials/{name}/versions` | `CredentialListVersions` | `credential.ListVersions` | `qrdb_credential_list_versions` | `CredentialListVersions` | `credentials:list_versions` |
| Rotate | `POST /v1/credentials/{name}/rotate` | `CredentialRotate` | `credential.Rotate` | `qrdb_credential_rotate` | `CredentialRotate` | `credentials:rotate` |
| GC expired versions | `POST /v1/credentials/{name}/gc` | `CredentialGCExpired` | `credential.GCExpired` | `qrdb_credential_gc_expired` | `CredentialGCExpired` | `credentials:gc_expired` |
| Delete | `DELETE /v1/credentials/{name}` | `CredentialDelete` | `credential.Delete` | `qrdb_credential_delete` | `CredentialDelete` | `credentials:delete` |
| List names | `GET /v1/credentials` | `CredentialList` | `credential.List` | `qrdb_credential_list` | `CredentialList` | `credentials:list` |
| Active strategy | `GET /v1/credentials/strategy` | `CredentialStrategy` | `credential.Strategy` | `qrdb_credential_strategy` | `CredentialStrategy` | `credentials:strategy` |

Ten capabilities on all six transports. *(Corrected 2026-10-06: the store and retrieve REST paths, and the retrieve-version, list-versions and GC permissions, were written to the wrong values.)* **Every transport handler routes
through the same `service.CredentialServicer`**, so behaviour cannot diverge
between them.

## Schemas

Store / submit:

```json
{ "name": "smtp-password", "value": "<bytes>" }
```

Rotate:

```json
{ "name": "smtp-password", "value": "<bytes>", "grace_period_seconds": 3600 }
```

`grace_period_seconds: 0` wipes the previous version immediately. The response
carries the new version number.

Retrieve version: `version` of **`0` means the current version**.

`CredentialVersion` entries carry the version number, creation time and the
expiry implied by the rotation grace period.

List returns **names only**, never values.

Strategy returns `manual` or `barrier`.

## Status and error mapping

| Condition | REST | gRPC / Unix |
|---|---|---|
| Success | 200 | `OK` |
| Credential not found | 404 | `NOT_FOUND` |
| Version expired or never existed | 404 | `NOT_FOUND` |
| Retrieve under `manual` with nothing submitted | **blocks** until submitted or the context ends | same |
| Barrier sealed under the `barrier` strategy | 503 | `UNAVAILABLE` |
| Enumeration on a strategy that cannot | 501 | `UNIMPLEMENTED` — `ErrEnumerationNotSupported` |
| Missing permission | 403 | `PERMISSION_DENIED` |

**The blocking read is deliberate.** Returning an empty value would let a boot
continue with no credential at all, which is exactly the failure the `manual`
strategy exists to prevent.

## Secret resolution SPI

Configuration fields may carry `credential://` or `literal://` references,
resolved at boot by `ConfigProcessor` into **enclave-backed `SecretValue`
targets rather than plain strings**. A protected field must reference a secret,
and resolution **fails closed**.

This is a **Tier 2, composition-plane** surface: it has an `sdk/go` alias file
and a documented functional option, and no wire endpoint — configuration is
resolved in process at startup, so there is nothing for a remote caller to
call. Scheme ownership across the stack is in
[credentials/secret-resolution.md](../credentials/secret-resolution.md).

## Pagination

`CredentialList` and `CredentialListVersions` return complete sets and are not
paginated. Both are bounded by operator action.

## Versioning

Credential REST paths are under `/v1/`. **No wire endpoint is deprecated and no
wire schema changed.**

The Go surface is Buffer-only after the OWNER's 2026-10-07 ruling:
`CredentialRetrieveBuffer` and `CredentialRetrieveVersionBuffer` return a
protected-memory `*secure.Buffer` the caller must `Destroy`, and the former
`[]byte` methods (`CredentialRetrieve`, `CredentialRetrieveVersion`,
`RetrieveCredential`, `RetrieveCredentialVersion`) are deleted from exported Go
interfaces and implementations. The Buffer forms ride the same wire operations
and the same `credentials:retrieve` / `credentials:retrieve_version`
permissions as the forms they replace. The retrieve responses are the one place
on the wire where a credential value travels as bytes; see
[Protected-memory boundary](architecture.md#protected-memory-boundary).
