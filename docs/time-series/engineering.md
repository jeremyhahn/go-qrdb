# Time Series — Engineering Contract

> **Status: NORMATIVE for shipped behaviour; PROPOSED for the target design** ·
> Scope: go-qrdb · Last verified: 2026-09-28
>
> "Target" sections describe the time-series half of the multi-model database
> program ([../databases/architecture.md](../databases/architecture.md)); they
> are pending owner review and not implemented. OD-n refers to that document's
> open decisions.

> **Build-tag gated: `qrdb_ts`** (`WITH_QRDB_TS=1`). Without it the CLI verb
> group and transport handlers are compiled out and `pkg/capabilities` does not
> register `timeSeries`.

## Exported surface

`sdk/go`:

```go
func (c *Client) TimeSeries() transport.TimeSeriesClient
```

`sdk/go/transport`:

```go
type TimeSeriesClient interface {
	TSWrite(ctx context.Context, point *TSDataPoint) error
	TSWriteBatch(ctx context.Context, points []TSDataPoint) error
	TSWritePoint(ctx context.Context, point *TSDataPoint) error
	TSQuery(ctx context.Context, req *TSQueryRequest) (*TSQueryResponse, error)
	TSAggregate(ctx context.Context, req *TSAggregateRequest) (*TSAggregateResponse, error)
	TSRichQuery(ctx context.Context, req *TSRichQueryRequest) (*TSRichQueryResponse, error)
	TSDeleteSeries(ctx context.Context, req *TSDeleteRequest) (*TSDeleteResponse, error)
	TSGetRetention(ctx context.Context, req *TSGetRetentionRequest) (*TSGetRetentionResponse, error)
	TSSetRetention(ctx context.Context, req *TSSetRetentionRequest) error
	TSListMetrics(ctx context.Context, req *TSListMetricsRequest) (*TSListMetricsResponse, error)
}
```

### Types

```go
type TSTag struct{ Key, Value string }

type TSDataPoint struct {
	Metric      string  `json:"metric"`
	Value       float64 `json:"value"`
	TimestampNs int64   `json:"timestamp_ns"`
	Tags        []TSTag `json:"tags,omitempty"`
}

type TSQueryRequest struct {
	Metric      string  `json:"metric"`
	StartNs     int64   `json:"start_ns"`
	EndNs       int64   `json:"end_ns"`
	Tags        []TSTag `json:"tags,omitempty"`
	Limit       int32   `json:"limit,omitempty"`
	Descending  bool    `json:"descending,omitempty"`
	Consistency string  `json:"consistency,omitempty"`
}
type TSQueryResponse struct{ Points []TSDataPoint }

type TSAggregateRequest struct {
	Metric      string  `json:"metric"`
	StartNs     int64   `json:"start_ns"`
	EndNs       int64   `json:"end_ns"`
	Function    AggFunc `json:"function"`
	IntervalNs  int64   `json:"interval_ns,omitempty"`
	Consistency string  `json:"consistency,omitempty"`
}
type TSAggResult struct {
	TimestampNs int64   `json:"timestamp_ns"`
	Value       float64 `json:"value"`
	Count       int64   `json:"count"`
}
type TSAggregateResponse struct{ Results []TSAggResult }

type TSRichQueryRequest struct {
	Metric      string   `json:"metric"`
	StartNs     int64    `json:"start_ns"`
	EndNs       int64    `json:"end_ns"`
	Tags        []TSTag  `json:"tags,omitempty"`
	Aggregation AggFunc  `json:"aggregation,omitempty"`
	IntervalNs  int64    `json:"interval_ns,omitempty"`
	GroupBy     []string `json:"group_by,omitempty"`
	Limit       int      `json:"limit,omitempty"`
	Percentile  float64  `json:"percentile,omitempty"`
	Consistency string   `json:"consistency,omitempty"`
}
type TSRichQueryResponse struct {
	Metric          string
	Tags            []TSTag
	Points          []TSDataPoint
	AggregatedValue *float64          `json:"aggregated_value,omitempty"`
	GroupedResults  []TSGroupedResult `json:"grouped_results,omitempty"`
}
type TSGroupedResult struct {
	Tags            []TSTag
	Points          []TSDataPoint
	AggregatedValue *float64
}

type TSDeleteRequest struct {
	Metric  string  `json:"metric"`
	Tags    []TSTag `json:"tags,omitempty"`
	StartNs int64   `json:"start_ns,omitempty"`
	EndNs   int64   `json:"end_ns,omitempty"`
}
type TSDeleteResponse struct{}

type TSRetentionPolicy struct {
	DurationNs   int64   `json:"duration_ns"`
	ResolutionNs int64   `json:"resolution_ns,omitempty"`
	Aggregation  AggFunc `json:"aggregation,omitempty"`
}
type TSGetRetentionRequest  struct{ Metric string }
type TSGetRetentionResponse struct{ Policy *TSRetentionPolicy }
type TSSetRetentionRequest  struct{ Metric string; Policy *TSRetentionPolicy }

type TSListMetricsRequest  struct{ Prefix string; Limit int32; Consistency string }
type TSListMetricsResponse struct{ Metrics []string }
```

### Aggregation functions

`AggFunc` is a `uint8` with **explicit numeric values**, so the wire form is a
number and the numbering is part of the contract:

| Constant | Value |
|---|---|
| `AggNone` | 0 |
| `AggAvg` | 1 |
| `AggSum` | 2 |
| `AggMin` | 3 |
| `AggMax` | 4 |
| `AggCount` | 5 |
| `AggPercentile` | 6 |
| `AggFirst` | 7 |
| `AggLast` | 8 |

`AggPercentile` reads `TSRichQueryRequest.Percentile`; it is meaningless on
`TSAggregateRequest`, which carries no percentile field.

## Invariants

- **All timestamps are nanoseconds** (`TimestampNs`, `StartNs`, `EndNs`,
  `IntervalNs`, `DurationNs`, `ResolutionNs`). There is no second- or
  millisecond-based field anywhere in this surface.
- **Ranges are `[start_ns, end_ns)`** — start inclusive, end exclusive, matching
  every other range in the store.
- **`AggFunc` values are fixed numbers**, not names. Renumbering them is a
  breaking wire change.
- **`TSWrite` and `TSWritePoint` are both single-point writes** with distinct
  permissions (`timeseries:write`, `timeseries:write_point`). They are separate
  RPCs on every transport; treat `TSWritePoint` as the narrower grant.
- **Retention is per metric and is a downsampling policy**, not just a delete
  horizon: `duration_ns` bounds age, `resolution_ns` sets downsampled
  resolution, and `aggregation` chooses the function that produces the
  downsampled value. **Measured: the policy is stored and returned and nothing
  applies it** — no point is ever expired or downsampled (OD-16).
- **`TSDeleteSeries` is bounded by its request**: metric, optionally tags,
  optionally a time range. With no range it deletes the whole series.
- **Time-series keys live under reserved prefixes** (`ts:`, `idx:metric:`,
  `idx:tag:`, `retention:`) and are granted **no** enumeration escape — the
  subsystem reads through `pkg/timeseries`, never through KV enumeration.
  They are therefore invisible to `kv list`/`kv scan` and unreachable by
  `kv restore`.
- **Tag cardinality is unbounded and unpoliced.** High-cardinality tags are the
  standard way to make a time-series store unusable, and nothing here stops it.

## Error semantics

| Condition | Behavior | Fails closed? |
|---|---|---|
| Built without `qrdb_ts` | capability absent; verbs and handlers unregistered | **yes** |
| Unknown metric on query | empty result (a metric is created by writing to it) | n/a |
| `end_ns <= start_ns` | invalid-argument | **yes** |
| Unknown `AggFunc` value | invalid-argument | **yes** |
| `AggPercentile` without `percentile` | invalid-argument | **yes** |
| Retention policy with `duration_ns <= 0` | invalid-argument | **yes** |
| Missing `timeseries:*` permission | permission denied | **yes** |
| Barrier sealed | refused | **yes** |

## Configuration

Retention is per metric through `TSSetRetention`, not a config file key.
Build flag: `WITH_QRDB_TS=1`.

## Persistence

- Points: composite keys under `ts:` — metric, tags and timestamp encoded so a
  metric's time range is a contiguous seek.
- Tag indexes: `idx:metric:` and `idx:tag:`.
- Retention policies: `retention:`.
- **Compatibility**: the composite key encoding and the `AggFunc` numbering are
  both on-disk/on-wire format. Changing either is a breaking change.

## Target: the time-series engine over the storage interface

### Contract

```go
// Storage is the time-series engine over one database's store. It holds no
// handle to a storage engine.
type Storage struct{ /* codec, clock seam for tests only */ }

func (s *Storage) Write(b storage.Batch, r storage.Reader, p DataPoint) error
func (s *Storage) WriteBatch(b storage.Batch, r storage.Reader, ps []DataPoint) error
func (s *Storage) DeleteSeries(b storage.Batch, r storage.Reader, req DeleteRequest) error
func (s *Storage) SetRetentionPolicy(b storage.Batch, metric string, p RetentionPolicy) error
func (s *Storage) ApplyRetention(b storage.Batch, r storage.Reader, metric string, cutoff time.Time) error

func (s *Storage) Query(r storage.Reader, metric string, start, end time.Time, opts ...TSQueryOption) ([]DataPoint, error)
func (s *Storage) QueryRich(r storage.Reader, req *QueryRequest) (*QueryResult, error)
func (s *Storage) Aggregate(r storage.Reader, metric string, start, end time.Time, fn AggFunc, interval time.Duration) ([]AggResult, error)
func (s *Storage) GetRetentionPolicy(r storage.Reader, metric string) (*RetentionPolicy, error)
func (s *Storage) ListMetrics(r storage.Reader, prefix string, limit int) ([]string, error)
```

- Write methods take the apply batch and a reader that sees the batch's own
  pending writes, so a point written earlier in the same apply is visible to a
  later one (the metric index is written once, not once per point).
- `timeseries.New(*pebble.DB)`, `NewWithDir`, `WithBarrier`, `WithEncryptor` and
  `Storage.DB()` are deleted. Encryption is the binding's, applied by the store.
- The engine's time-series handlers pass the batch they are given; the
  engine's `WithMVCCTimeSeries` option takes no Pebble handle.
- `TSApplyRetention` is a new extended-protocol command (OD-16 A); its opcode is
  assigned in the implementing change.

### Target: acceptance tests

Lane 1A (storage-generic engine):

1. **In the batch.** A time-series write and a KV write in one entry whose batch
   commit is forced to fail leave neither the point, nor the metric index entry,
   nor the tag index entry.
2. **Every backend.** The time-series conformance suite — write, batch write,
   `[start, end)` boundaries, tag filters, every `AggFunc`, interval bucketing,
   `group_by`, percentile, delete with and without a range, retention round
   trip — passes on memory, file, pebble, objstore and raft bindings.
3. **No engine import.** A build-gate test that `pkg/timeseries` imports no
   storage engine package.
4. **Placement.** On a barrier, volume or raft tenant's database the `ts:` keys
   are found by exact key in that database's own store and not in any other,
   and cannot be read with the node's encryptor.

Lane 4C (retention, OD-16 A):

5. **Deterministic sweep.** Three replicas apply the same `TSApplyRetention`
   and hold byte-identical `ts:` ranges afterwards, with their clocks
   deliberately skewed.
6. **Downsampling.** A policy with a resolution replaces expired points by one
   point per bucket whose value is the policy's aggregation of the bucket.
7. **Bounded entries.** A metric with more expired points than one entry's
   bound is fully swept across several commands, and no entry exceeds the
   bound.
8. **The drop fence.** A sweep proposed before the database's drop fence and
   applied after it is refused.

## Test strategy

- **Unit**, under `qrdb_ts`: write and batch write, range query boundary
  behaviour (`[start, end)`), tag filtering, each `AggFunc`, interval bucketing,
  `group_by` in rich query, percentile, delete with and without a range, and
  retention round-trip.
- **Error tests** per function.
- **No `time.Sleep`** — retention expiry is driven through the clock seam.
- **Integration**: time-series parity across transports in the standalone stage.

## Known gap — CLI does not cover the API

The `qrdb ts` command group exposes four of the ten capabilities: `write`,
`write-batch`, `query`, `aggregate`. Six have **no CLI verb**:
`TSWritePoint`, `TSRichQuery`, `TSDeleteSeries`, `TSGetRetention`,
`TSSetRetention`, `TSListMetrics`. They are reachable on all six transports
through the SDK. Recorded in [cli.md](cli.md) and [api.md](api.md).

## Observability

- Writes and deletes are audited through [../audit/](../audit/).
- `TSListMetrics` is the discovery surface — it is how an operator finds out
  what is being written, since metrics are created implicitly by writing.

## Wire-field bounds

The length-field contract for this plane — what each field can describe, that a
longer value is refused rather than truncated, that encoders return
`([]byte, error)` and validate before allocating, and that a refusal is a client
error mapping to `InvalidArgument`/400 — is one rule for the whole extended wire
protocol and is stated once, in
[../key-value/engineering.md](../key-value/engineering.md#wire-field-bounds--the-owning-contract-for-every-plane).

It is not restated here on purpose: four copies of a bound is how the four drift
apart.
