<!--
Copyright 2026 Jeremy Hahn
Licensed under the Apache License, Version 2.0
-->

# qrdb Client CLI

The `qrdb` binary is the client CLI for interacting with a qrdbd server. It uses the Go SDK with configurable transport.

## Command Tree

```
qrdb
├── kv                              # Key-value operations
│   ├── put <key> <value>           # Store a key-value pair (--lease <id>)
│   ├── get <key>                   # Retrieve value (--revision, --at-revision N)
│   ├── del <key>                   # Delete a key
│   ├── list [prefix]               # List keys by prefix
│   └── exists <key>                # Check key existence
├── mvcc                            # MVCC operations
│   ├── revision                    # Current revision number
│   ├── range <start> <end>         # Range query (--limit, --keys-only, --sort-order)
│   └── compact <revision>          # Compact old revisions
├── watch                           # Real-time notifications
│   ├── key <key>                   # Watch single key (--start-revision, --prev-kv)
│   └── prefix <prefix>            # Watch prefix (--progress-notify)
├── txn exec                        # Compare-and-swap transaction (--file <txn.json>)
├── batch exec                      # Atomic multi-op batch (--file <ops.json>)
├── lease                           # Distributed leases
│   ├── grant <ttl>                 # Create lease
│   ├── revoke <id>                 # Revoke lease
│   ├── keepalive <id>              # Reset TTL
│   ├── ttl <id>                    # Show remaining TTL
│   └── list                        # List active leases
├── index                           # Secondary indexes
│   ├── create <name>               # Create index (--field, --unique, --sparse)
│   ├── drop <name>                 # Remove index
│   ├── query <name> <value>        # Exact lookup
│   └── scan <name> <start> <end>   # Range scan
├── ts                              # Time series
│   ├── write <metric> <value>      # Write point (--timestamp, --tags)
│   ├── write-batch                 # Batch write (--file)
│   ├── query <metric>              # Query range (--start, --end, --tags)
│   └── aggregate <metric>          # Aggregate (--func, --interval)
├── barrier                         # Seal/unseal operations
│   ├── init                        # Initialize (--shares N --threshold M)
│   ├── seal / unseal / status      # Barrier lifecycle
│   ├── rotate / rekey              # Key management
│   ├── recovery generate|restore   # Disaster recovery
│   ├── root-token generate         # One-time admin token
│   └── tenant init|seal|unseal|status <tenantID>
├── admin                           # Cluster administration
│   ├── health / status / peers     # Node info
│   ├── member add|remove           # Membership management
│   ├── snapshot / drain            # Operational tasks
└── version                         # Version info
```

## Global Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--server` | auto-detect | Server address (URL scheme selects transport) |
| `--transport` | auto | Transport: rest, grpc, unix, mcp |
| `--ca-file` | | TLS CA certificate file |
| `--cert-file` | | TLS client certificate |
| `--key-file` | | TLS client key |
| `--output` | table | Output format: table, json, yaml |
| `--timeout` | 10s | Request timeout |

## Top-Level Aliases

`qrdb put` → `qrdb kv put`, `qrdb get` → `qrdb kv get`, `qrdb del` → `qrdb kv del`

## Transport Auto-Detection

| URL Scheme | Transport |
|------------|-----------|
| `unix://` | gRPC over Unix socket |
| `https://` | REST |
| `grpcs://` | gRPC |
| `mcp://` | MCP |

## Examples

```bash
# Basic KV operations
qrdb put mykey myvalue
qrdb get mykey
qrdb del mykey

# Remote server with TLS
qrdb --server https://qrdb.example.com:63002 --ca-file ca.pem kv list

# Admin operations
qrdb admin health
qrdb admin status --output json

# Barrier management
qrdb barrier init --shares 5 --threshold 3
qrdb barrier unseal --share <share1>
```
