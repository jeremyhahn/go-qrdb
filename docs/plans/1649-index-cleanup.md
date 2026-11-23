# #1649 / PR #1664: index cleanup on overwrite

## fx1664s: Codex round 2 at 6804c789e0 P1 1

**Cause.** `UpdateIndexes`, `UpdateIndexesBatch` and
`UpdateCompoundIndexesBatch` (`pkg/engine/indexing.go`) returned at the top
when the new value did not parse as a JSON object, before the previous
value's index entries were removed. `PutWithIndexes` passes the previous
value as `prevKv`, so overwriting an indexed JSON record with arbitrary bytes
left its old entries in place: a lookup of the old value still returned the
key, and a unique value it held stayed taken, so another key writing that
value was refused.

**Fix.** The new value is parsed by `parseIndexFields`, which yields no
fields for a value that is not a JSON object instead of ending the call. The
removal of the previous value's entries runs unconditionally (still
best-effort parse of `prevKv.Value`; the key-scope and `BatchableIndex`
skips are unchanged), and nothing is added for the new value. The compound
variant removes the old compound entry with `DeleteCompoundBatch` and adds
none. `CheckUniqueViolation` and `CheckUniqueViolationsBatch` already skip a
non-JSON put (it has no field to check), so they are unchanged.

**Tests.** `pkg/engine/indexing_test.go`:
`TestUpdateIndexes_NonJSONReplacementRemovesOldEntry`,
`TestUpdateIndexesBatch_NonJSONReplacementRemovesOldEntry`,
`TestUpdateCompoundIndexesBatch_NonJSONReplacementRemovesOldEntry`.
`pkg/engine/kv_engine_test.go`:
`TestKVEngine_PutWithIndexes_NonJSONOverwriteReleasesUniqueValue` and
`TestKVEngine_PutWithIndexes_NonJSONOverwriteReleasesCompoundUniqueValue`
(unique index; after the non-JSON overwrite the old value looks up nothing
and a second key takes it). Each failed before the fix. The existing
`TestUpdateIndexesBatch_NonJSON` (non-JSON value, no previous value: nothing
indexed) still holds.

**Open.** `CheckUniqueViolationsBatch` treats a key that a multi-op batch
overwrites (with non-JSON, or JSON carrying a different value) as still
holding its old unique value, so a batch that releases a value from key A
and gives it to key B in the same batch is refused. Only deleted keys are
excluded today. Not changed in this lane.

## rw1649h: the cleanup is versioned by `CmdFlagOrderedUnique`

Codex round 3 P1 1 on PR #1706: the unconditional removal above changed
how an existing, unflagged log replays (a non-JSON
overwrite of A, then an unflagged write of B with A's old unique value, was
refused when committed and would commit on replay). The exported
`UpdateIndexesBatch` and `UpdateCompoundIndexesBatch` are back to the
merge-base semantics: a new value that is not JSON returns at once and the
old entries are kept. The cleanup lives in the unexported
`updateIndexesBatch` / `updateCompoundIndexesBatch` with `ordered=true`,
called only from the apply of a transaction (KV and MVCC engines) or batch
(MVCC engine) proposed with `protocol.CmdFlagOrderedUnique`. A plain put's
value is the unframed payload remainder and carries no trailer, so a plain
non-JSON overwrite (and `PutWithIndexes`) keeps the stale entry as on the
base; versioning it needs a flagged put op behind `ordered-unique-check`
(owner decision, see docs/plans/1649.md). `UpdateIndexes` (no batch, no
replicated caller) still removes the old entries. Tests:
`TestKVEngine_LegacyNonJSONOverwriteReplay`,
`TestMVCCEngine_LegacyNonJSONOverwriteReplay`, and legacy plus
ordered/flagged twins of the NonJSONReplacement and NonJSONOverwrite tests.
