# Rebalance and Shard Stats Service

Cluster rebalancing with shard statistics, imbalance detection, and automated move planning.

## Overview

The QRDB Rebalance Service provides cluster-wide shard distribution analysis and automated rebalancing. It inspects per-node load (keys, bytes, leader count), computes an imbalance score using the coefficient of variation, identifies hotspots, and generates move plans to balance data across nodes.

## Key Features

| Feature | Description |
|---------|-------------|
| **Shard Statistics** | Per-shard key count, byte count, leader, and replica info |
| **Imbalance Detection** | Coefficient of variation (stddev / mean) of byte counts |
| **Hotspot Identification** | Nodes exceeding mean + 1.5 standard deviations |
| **Move Planning** | Optimal shard moves from overloaded to underloaded nodes |
| **Move Limits** | Cap the maximum number of moves per plan |
| **Execution** | Apply move plans via Raft membership changes |
| **Duration Estimates** | Transfer time based on 100 MB/s throughput estimate |

## API Reference

### GetShardStats

Returns statistics for a single shard.

```go
func (s *RebalanceService) GetShardStats(ctx context.Context, shardID uint64) (*ShardStats, error)
```

**ShardStats:**

| Field | Type | Description |
|-------|------|-------------|
| `ShardID` | `uint64` | Shard identifier |
| `KeyCount` | `uint64` | Number of keys in the shard |
| `ByteCount` | `uint64` | Total bytes (keys + values) |
| `LeaderID` | `uint64` | Current leader replica ID (0 if unknown) |
| `ReplicaCount` | `int` | Number of replicas |

**Errors:**
- `ShardStatsError` -- shard not loaded or stale read failed

### CreateRebalancePlan

Analyzes cluster distribution and generates an optimal move plan.

```go
func (s *RebalanceService) CreateRebalancePlan(ctx context.Context, opts *RebalancePlanOptions) (*RebalancePlanResult, error)
```

**RebalancePlanOptions:**

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `MaxMoves` | `int` | `0` (unlimited) | Maximum number of shard moves |
| `DryRun` | `bool` | `false` | Generate plan without executing |
| `TargetBalance` | `float64` | `0` | Target balance percentage (0-100) |

**Returns:** `*RebalancePlanResult` containing the move list, estimated duration, and current imbalance score.

**Errors:**
- `RebalancePlanError` -- failed to generate imbalance report

### ExecuteRebalance

Applies a rebalance plan by adding replicas on destination nodes.

```go
func (s *RebalanceService) ExecuteRebalance(ctx context.Context, plan *RebalancePlanResult) (*RebalanceExecutionResult, error)
```

**RebalanceExecutionResult:**

| Field | Type | Description |
|-------|------|-------------|
| `MovesCompleted` | `int` | Successful moves |
| `MovesFailed` | `int` | Failed moves |
| `Duration` | `time.Duration` | Actual execution time |
| `FinalImbalance` | `float64` | Imbalance score after rebalancing |

**Errors:**
- `RebalanceExecuteError` -- nil plan provided

### GetImbalanceReport

Returns the current cluster balance report.

```go
func (s *RebalanceService) GetImbalanceReport(ctx context.Context) (*ImbalanceReportResult, error)
```

**ImbalanceReportResult:**

| Field | Type | Description |
|-------|------|-------------|
| `Nodes` | `[]NodeLoadInfo` | Per-node load distribution |
| `ImbalanceScore` | `float64` | Coefficient of variation (0.0 = balanced) |
| `Hotspots` | `[]string` | Node IDs exceeding the hotspot threshold |

**NodeLoadInfo:**

| Field | Type | Description |
|-------|------|-------------|
| `NodeID` | `string` | Unique host identifier |
| `Address` | `string` | Raft network address |
| `ShardCount` | `int` | Shards hosted |
| `TotalKeys` | `uint64` | Total keys across all shards |
| `TotalBytes` | `uint64` | Total bytes across all shards |
| `LeaderCount` | `int` | Number of shards where this node is leader |

## Imbalance Detection Algorithm

The imbalance score is the **coefficient of variation** of byte counts across nodes:

```
imbalance = stddev(node_bytes) / mean(node_bytes)
```

| Score | Interpretation |
|-------|----------------|
| `0.0` | Perfectly balanced |
| `0.0 - 0.1` | Well balanced |
| `0.1 - 0.3` | Moderate imbalance |
| `> 0.3` | Significant imbalance, rebalance recommended |

**Hotspot detection:** A node is flagged as a hotspot when its byte count exceeds `mean + 1.5 * stddev`.

**Move planning algorithm:**
1. Compute target bytes per node: `total_bytes / node_count`
2. Sort nodes by delta from target (most overloaded first)
3. For each overloaded node, move shards to the most underloaded node
4. Adjust deltas after each move until balanced or `MaxMoves` reached
5. Estimate duration at 100 MB/s transfer rate

## SDK Usage

### Get Shard Statistics

```go
ctx := context.Background()

stats, err := client.Rebalance().GetShardStats(ctx, 1)
if err != nil {
    log.Fatal(err)
}
fmt.Printf("Shard %d: %d keys, %d bytes, leader=%d, replicas=%d\n",
    stats.ShardID, stats.KeyCount, stats.ByteCount, stats.LeaderID, stats.ReplicaCount)
```

### Check Cluster Balance

```go
report, err := client.Rebalance().GetImbalanceReport(ctx)
if err != nil {
    log.Fatal(err)
}

fmt.Printf("Imbalance score: %.4f\n", report.ImbalanceScore)
for _, node := range report.Nodes {
    fmt.Printf("  %s: %d shards, %d keys, %d bytes, %d leaders\n",
        node.Address, node.ShardCount, node.TotalKeys, node.TotalBytes, node.LeaderCount)
}
if len(report.Hotspots) > 0 {
    fmt.Printf("Hotspots: %v\n", report.Hotspots)
}
```

### Create and Execute a Rebalance Plan

```go
// Generate a plan with at most 5 moves
plan, err := client.Rebalance().CreateRebalancePlan(ctx, &transport.RebalancePlanRequest{
    MaxMoves: 5,
})
if err != nil {
    log.Fatal(err)
}

fmt.Printf("Plan: %d moves, estimated %s, current imbalance=%.4f\n",
    len(plan.Moves), plan.EstimatedDuration, plan.CurrentImbalance)

for _, m := range plan.Moves {
    fmt.Printf("  Shard %d: %s -> %s (~%d bytes)\n",
        m.ShardID, m.FromNode, m.ToNode, m.EstimatedBytes)
}

// Execute the plan
result, err := client.Rebalance().ExecuteRebalance(ctx, plan)
if err != nil {
    log.Fatal(err)
}
fmt.Printf("Completed: %d, Failed: %d, Duration: %s, Final imbalance: %.4f\n",
    result.MovesCompleted, result.MovesFailed, result.Duration, result.FinalImbalance)
```

### Dry Run

```go
plan, err := client.Rebalance().CreateRebalancePlan(ctx, &transport.RebalancePlanRequest{
    DryRun: true,
})
// Inspect plan.Moves without executing
```

## Transport Exposure & Authorization

The Rebalance Service is exposed on **every** API transport. Each operation is
guarded by a dedicated permission (default-deny: an unmapped route/method/tool
is rejected). The `Owner` role holds all permissions and therefore all rebalance
operations.

| Operation | Permission | gRPC RPC (`qrdb.v1.QRDBService/`) | REST | QUIC method | MCP tool |
|-----------|-----------|-----------------------------------|------|-------------|----------|
| Shard stats | `rebalance:stats` | `RebalanceGetShardStats` | `GET /v1/rebalance/shards/{id}` | `rebalance.GetShardStats` | `qrdb_rebalance_shard_stats` |
| Create plan | `rebalance:plan` | `RebalanceCreatePlan` | `POST /v1/rebalance/plan` | `rebalance.CreatePlan` | `qrdb_rebalance_plan` |
| Execute | `rebalance:execute` | `RebalanceExecute` | `POST /v1/rebalance/execute` | `rebalance.Execute` | `qrdb_rebalance_execute` |
| Imbalance report | `rebalance:report` | `RebalanceGetImbalanceReport` | `GET /v1/rebalance/report` | `rebalance.GetImbalanceReport` | `qrdb_rebalance_report` |

The Unix-domain-socket transport inherits the full gRPC surface (gRPC over UDS).
The execute operation takes the full plan returned by the create-plan operation;
on the MCP transport the plan is passed as a JSON object string.
