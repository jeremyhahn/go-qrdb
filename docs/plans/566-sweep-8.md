# #566 sleep sweep 8 (rw566m)

Sweep 8 takes the `time.Sleep` polls left in the `sdk/go` module (conformance,
driver, node, testutil/qrdbdharness, testutil/raftharness, transport/embedded)
and in the `test/clusterproof` module (nemesis). Line numbers below are the
lines before the sweep. Following sweep 2's `sdk/go` precedent, a deadline loop
keeps its deadline, interval and failure text, and the sleep becomes a ticker
wait (`poll := time.NewTicker(d); defer poll.Stop()` before the loop, `<-poll.C`
in the sleep's place): either the package does not import testify
(`test/clusterproof` has none and must add no dependency), or the loop body
calls `t.Fatalf`, which must not run on `Eventually`'s goroutine. The one loop
whose body only asserts a condition over a window
(`prebound_listener_test.go`) becomes a bounded `require.Never`. The kept
sleeps and the `test/integration` polls left to a later sweep are recorded as
well.

| File | Line | Replaced by / kept and why |
|------|------|----------------------------|
| sdk/go/conformance/compaction_node_test.go | 125 | Ticker wait (`compactionInterval`): the no-policy window loop calls `t.Fatalf` |
| sdk/go/conformance/compaction_node_test.go | 149 | Ticker wait (25ms) until `isCompacted`; body calls `t.Fatalf` |
| sdk/go/conformance/compaction_policy_test.go | 98 | Ticker wait (25ms) until `isCompacted`; body calls `t.Fatalf` |
| sdk/go/conformance/coordination_parity_test.go | 269 | `assertLeaseExpiryDeletesKeys`: ticker wait (100ms) until `Get` fails |
| sdk/go/conformance/harness_test.go | 950 | `awaitWritable`: ticker wait (50ms) between Put retries on NotLeader / ReadIndexNotReady |
| sdk/go/conformance/share_holders_test.go | 467 | `(*holderE2E).dial`: ticker wait (50ms) between connect retries |
| sdk/go/driver/pool_test.go | 453 | `assertPoolDrained` (tag `qrdb_sql`): ticker wait (1ms) until `Stats().InUse == 0` |
| sdk/go/node/compaction_replicated_test.go | 157 | `setPolicy`: ticker wait (`clusterPollInterval`) while `feature.NotActiveError` |
| sdk/go/node/embedded_cluster_ca_test.go | 222 | `retryUntil` (no `t`): `select` on `ctx.Done()` or the 25ms ticker, so a cancelled ctx ends the wait at once; the next `f` call returns its error as before |
| sdk/go/node/prebound_listener_test.go | 245 | `require.Never(!h3Healthy, staleH3TimerWindow, 100ms)` |
| sdk/go/node/tenant_isolation_test.go | 48 | `awaitReadIndexReady`: ticker wait (100ms); still returns without failing at the deadline |
| sdk/go/testutil/qrdbdharness/daemon_test.go | 532 | `waitForTail`: ticker wait (10ms) |
| sdk/go/testutil/qrdbdharness/relay_test.go | 121 | `waitCount`: ticker wait (1ms) |
| sdk/go/testutil/raftharness/raftharness.go | 187 | `(*Cluster).WaitReadable` (non-test helper, callers in pkg/server, pkg/service, pkg/statemachine): ticker wait (10ms); signature, `Bound`, `pollAttempt` and failure text unchanged |
| sdk/go/transport/embedded/seal_watch_test.go | 137 | `TestWatch_OtherTenantSealLeavesTheStream`: ticker wait (5ms) until `streams.Len() == 0` |
| test/clusterproof/nemesis/target_test.go | 327 | `waitState`: ticker wait (5ms); no testify in the module and `procState` calls `t.Fatal` |
| sdk/go/coordination_raft_test.go | 832 | Kept: the measured renewal interval is the thing under test |
| sdk/go/coordination_test.go | 1036 | Kept (sweep 2): positions the renewal at half the lease lifetime |
| sdk/go/transport/coalescing_test.go | 262, 274 | Kept (sweep 2): synctest fake-clock advance |
| sdk/go/testutil/qrdbdharness/daemon_test.go | 59, 65 | Kept: subprocess stand-ins that must stay alive; not waits |
| sdk/go/testutil/qrdbdharness/startready_test.go | 66 | Kept: subprocess stand-in that must stay alive; not a wait |
| sdk/go/gorm/conformance/qrdb_harness_test.go | 539 | Kept: `waitForListener`, separate module, TestMain-time helper with no `*testing.T` |
| sdk/go/types_request_error.go | 26 | Kept: doc comment only |
| test/clusterproof/soak/soak_test.go | 58, 69, 80, 91 | Kept: injected fake-store latency (`opDelay`) |
| test/integration/cluster_seal_test.go | 449 | Kept: fixed absence window (#813) |
| test/integration/watch_cli_test.go | 394 | Kept: `watchSubscribeGrace`, no subscribe signal to wait on |
| test/integration/watch_cli_test.go | 833 | Kept: deliberate phase stagger |
| test/integration/helpers_test.go | 3025 | Left to a later sweep: `waitForNodeToTrustClient` (integration tag; multi-node cluster callers) |
| test/integration/isolation_raft_cluster_test.go | 610, 654 | Left to a later sweep (integration tag; multi-node cluster runs) |
| test/integration/tenant_undo_reuse_cli_test.go | 160 | Left to a later sweep (integration tag; multi-node cluster runs) |

## CHANGELOG line (ready to paste)

- Tests: sdk/go (conformance, driver, node, qrdbdharness, raftharness, embedded transport) and test/clusterproof wait on a ticker or a bounded testify poll instead of time.Sleep between attempts (#566)
