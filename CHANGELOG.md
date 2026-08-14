# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog 1.1.0](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning 2.0.0](https://semver.org/spec/v2.0.0.html).

Pre-1.0, MINOR is where breaking changes live; PATCH is reserved for changes
that do not move the API. Releases before `0.2.0-alpha` predate this file and
are not reconstructed here — their history is in the git log.

## [Unreleased]

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

[Unreleased]: https://github.com/jeremyhahn/go-qrdb/compare/v0.3.0-alpha...HEAD
[0.3.0-alpha]: https://github.com/jeremyhahn/go-qrdb/compare/v0.2.1-alpha...v0.3.0-alpha
[0.2.1-alpha]: https://github.com/jeremyhahn/go-qrdb/compare/v0.2.0-alpha...v0.2.1-alpha
[0.2.0-alpha]: https://github.com/jeremyhahn/go-qrdb/compare/v0.1.1-alpha...v0.2.0-alpha
