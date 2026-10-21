# Time Series — CLI

> **Status: NORMATIVE for shipped behaviour; APPROVED, not yet implemented, for the target design** ·
> Scope: go-qrdb · Last verified: 2026-10-21
> Conflicts with this document are defects. Fix them here first.
>
> "Target" sections describe the time-series half of the multi-model database
> program ([../databases/architecture.md](../databases/architecture.md)); they
> are approved and not implemented.

Command group: `qrdb ts`. **Present only in builds carrying the `qrdb_ts` tag**
(`make build WITH_QRDB_TS=1`); confirm with `qrdb admin capabilities`
(reported as `timeSeries`).

All verbs accept the root persistent flags documented in
[../key-value/cli.md](../key-value/cli.md#global-flags).

**All timestamps are nanoseconds.**

## `qrdb ts write`

Write a single data point.

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--metric` | string | — | metric name, required |
| `--value` | float | — | data point value, required |
| `--timestamp` | int | current time | nanosecond timestamp |
| `--tag` | strings | — | tags as `k=v`, repeatable |

```bash
qrdb ts write --metric cpu.usage --value 0.42 --tag host=web-01 --tag region=us-east
qrdb ts write --metric cpu.usage --value 0.51 --timestamp 1755561600000000000
```

## `qrdb ts write-batch`

Write a batch of data points from JSON.

| Flag | Type | Required |
|---|---|---|
| `--file` | string | yes — JSON file path, or `-` for stdin |

The file is an array of `TSDataPoint`: `metric`, `value`, `timestamp_ns`,
`tags` (`[{"key":...,"value":...}]`).

```bash
qrdb ts write-batch --file points.json
jq -c . points.json | qrdb ts write-batch --file -
```

## `qrdb ts query`

Query raw data points in a time range.

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--metric` | string | — | metric name, required |
| `--start` | int | — | start timestamp, nanoseconds (inclusive), required |
| `--end` | int | — | end timestamp, nanoseconds (inclusive), required |
| `--tag` | strings | — | filter tags as `k=v`, repeatable |
| `--limit` | int32 | unlimited | maximum results |
| `--desc` | bool | false | descending order |

```bash
qrdb ts query --metric cpu.usage \
     --start 1755561600000000000 --end 1755565200000000000 \
     --tag host=web-01 --limit 1000

# Most recent 10 points
qrdb ts query --metric cpu.usage --start 0 --end 9223372036854775807 \
     --desc --limit 10
```

The range is closed: `[start, end]`.

## `qrdb ts aggregate`

Aggregate data points, bucketed by interval.

| Flag | Type | Meaning |
|---|---|---|
| `--metric` | string | metric name, required |
| `--start` | int | start timestamp, nanoseconds, required |
| `--end` | int | end timestamp, nanoseconds, required |
| `--function` | string | `avg \| sum \| min \| max \| count \| percentile`, required |
| `--interval` | int | bucket interval, nanoseconds |

```bash
# Hourly averages (3600e9 ns = 1 hour)
qrdb ts aggregate --metric cpu.usage \
     --start 1755561600000000000 --end 1755648000000000000 \
     --function avg --interval 3600000000000
```

Each result carries the bucket timestamp, the value, and the count of points
that went into it.

## Capabilities with no CLI verb

Six of the ten time-series capabilities have **no `qrdb ts` verb**. They are
available on all six transports through the SDK and the API:

| Capability | SDK method | Reach it via |
|---|---|---|
| Write point (narrower permission) | `TSWritePoint` | SDK / REST / gRPC / QUIC / MCP / Unix |
| Rich query (`group_by`, percentile) | `TSRichQuery` | as above |
| Delete a series | `TSDeleteSeries` | as above |
| Get retention policy | `TSGetRetention` | as above |
| Set retention policy | `TSSetRetention` | as above |
| List metrics | `TSListMetrics` | as above |

This is a CLI coverage gap, not a deliberate scope fence. See
[api.md](api.md) for the full surface and
[engineering.md](engineering.md#known-gap--cli-does-not-cover-the-api).

## Parity

`TestTS_CLI_PerProtocol` (`test/integration/ts_cli_test.go`) drives the four
verbs above end to end over REST, gRPC, QUIC, MCP and the Unix socket. The six capabilities with no verb
have no CLI test.
