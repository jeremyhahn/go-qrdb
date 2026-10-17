# Capabilities — User Guide

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-06
> Conflicts with this document are defects. Fix them here first.

Use capabilities to answer "what can this node do right now?"

```bash
qrdb --server https://db.example.com:63002 admin capabilities
qrdb --server https://db.example.com:63002 admin capabilities --output json
```

If a feature is `false`, check build tags, run mode and configuration. For
example, etcd requires the etcd build tag and raft mode; vector and time-series
require their build tags; watch and lease can be narrowed by config.

The feature target is `make integration-test-capabilities`. Its matrix entry is
the `[capabilities](../../docs/capabilities/)` row in
`test/integration/COVERAGE.md`.
