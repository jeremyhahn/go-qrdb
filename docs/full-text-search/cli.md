# Full-Text Search — CLI

> **Status: NORMATIVE** · Scope: go-qrdb · Last verified: 2026-08-19

**No CLI surface.** Full-text search has no `qrdb` verb group. It is reachable
only by importing `pkg/fts` in-process, in a build carrying the `qrdb_fts` tag.

The only CLI-visible trace of the feature is capability reporting:

```bash
qrdb admin capabilities
```

which lists `fts` when the daemon was built with `WITH_QRDB_FTS=1`.

This is a gap rather than a design decision. Under the repo's export obligation
a capability with no SDK surface is not shippable, and a CLI surface follows the
SDK surface. See [api.md](api.md) and
[architecture.md](architecture.md#sdk-exposure-gap).

Building with FTS compiled in:

```bash
make build WITH_QRDB_FTS=1
```
