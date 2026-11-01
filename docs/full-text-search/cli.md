# Full-Text Search — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-10-23
> Conflicts with this document are defects. Fix them here first.

**No CLI surface.** Full-text search has no `qrdb` verb group. It is reachable
only by importing `pkg/fts` in-process, in a build carrying the `qrdb_fts` tag.

The only CLI-visible trace of the feature is capability reporting:

```bash
qrdb admin capabilities
```

Its `fts` row reads `true` when the daemon was built with `WITH_QRDB_FTS=1`,
and `false` otherwise. With `--output json` the same value is the `fts` field.

A CLI surface follows the SDK surface, which does not exist yet. See
[api.md](api.md) and [architecture.md](architecture.md#sdk-exposure-gap).

Building with FTS compiled in:

```bash
make build WITH_QRDB_FTS=1
```
