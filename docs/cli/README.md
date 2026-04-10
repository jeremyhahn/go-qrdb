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
├── database                        # Database management
│   ├── create <name>               # Create database (--engine, --backend, --etcd, --etcd-listen-addr, --etcd-advertise-urls)
│   ├── list                        # List all databases
│   ├── info <name>                 # Get database details
│   ├── drop <name>                 # Drop a database
│   ├── update-etcd <name>          # Update etcd protocol config (--enabled, --disabled, --listen-addr, --advertise-urls)
├── admin                           # Cluster administration
│   ├── health / status / peers     # Node info
│   ├── member add|remove           # Membership management
│   ├── snapshot / drain            # Operational tasks
├── users                           # User management
│   ├── list [--tenant-id <id>]     # List users
│   ├── create --username <name>    # Create user (--tenant-id, --roles)
│   ├── get <id>                    # Get user details
│   ├── update <id>                 # Update user (--username, --roles)
│   ├── delete <id>                 # Delete user
│   ├── enable <id>                 # Enable user
│   └── disable <id>               # Disable user
├── roles                           # Role management
│   ├── list [--tenant-id <id>]     # List roles
│   ├── create --name <name>        # Create role (--permissions)
│   ├── get <id>                    # Get role details
│   ├── update <id>                 # Update role (--name, --permissions)
│   ├── delete <id>                 # Delete role
│   ├── assign --user <uid> --role <rid>   # Assign role to user
│   ├── unassign --user <uid> --role <rid> # Unassign role from user
│   └── permissions                 # List all permissions
├── certs                           # Certificate management
│   ├── generate --username <name>  # Generate client cert (--tenant-id, --ttl, --out)
│   ├── export --username <name>    # Export as PKCS#12 (--format p12, --passphrase, --out)
│   ├── revoke --serial <serial>    # Revoke certificate
│   └── list [--tenant-id <id>]     # List issued certificates
├── lock                            # Distributed locks
│   ├── acquire <key> --ttl <secs>  # Blocking lock acquisition
│   ├── trylock <key> --ttl <secs>  # Non-blocking lock attempt
│   ├── release <key> --token <tok> # Release held lock
│   ├── extend <key> --token <tok>  # Refresh lock TTL
│   ├── status <key>                # Query lock state
│   └── list [--prefix <prefix>]    # List active locks
├── vector                          # Vector operations (requires qrdb_vector build tag)
│   ├── namespace create|get|update|delete|list
│   ├── upsert|get|delete|list <namespace>
│   ├── search|search-by-id|search-metadata <namespace>
│   └── index rebuild|progress / compact / stats <namespace>
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
| `--shard-id` | (deprecated) | *DEPRECATED* Shard ID selection (ignored; use database routing instead) |

## Top-Level Aliases

`qrdb put` → `qrdb kv put`, `qrdb get` → `qrdb kv get`, `qrdb del` → `qrdb kv del`

## Transport Auto-Detection

| URL Scheme | Transport |
|------------|-----------|
| `unix://` | gRPC over Unix socket |
| `https://` | REST |
| `grpcs://` | gRPC |
| `mcp://` | MCP |

## Lock Commands

### lock acquire

Blocking lock acquisition with optional reentrant mode.

```bash
qrdb lock acquire <key> --ttl <seconds> [--holder <id>] [--reentrant] [--timeout <seconds>]
```

**Options:**
- `--ttl`: Required. Lock time-to-live in seconds
- `--holder`: Optional. Holder ID (UUID generated if not provided)
- `--reentrant`: Flag. Enable reentrant acquisition by same holder
- `--timeout`: Optional. Command timeout (default: 10s)

**Output:** Lock info with fencing token

```bash
qrdb lock acquire myresource --ttl 30
qrdb lock acquire leader --ttl 60 --holder node-1 --reentrant
```

### lock trylock

Non-blocking lock attempt. Returns immediately with error if held.

```bash
qrdb lock trylock <key> --ttl <seconds> [--holder <id>] [--reentrant]
```

**Options:**
- `--ttl`: Required. Lock time-to-live in seconds
- `--holder`: Optional. Holder ID
- `--reentrant`: Flag. Enable reentrant acquisition

```bash
qrdb lock trylock myresource --ttl 30
```

### lock release

Release a held lock using the fencing token from acquisition.

```bash
qrdb lock release <key> --token <fencing-token>
```

**Options:**
- `--token`: Required. The fencing token returned by acquire/trylock

```bash
qrdb lock release myresource --token 42
```

### lock extend

Extend the TTL of a held lock without releasing it.

```bash
qrdb lock extend <key> --token <fencing-token>
```

**Options:**
- `--token`: Required. The fencing token of the held lock

```bash
qrdb lock extend myresource --token 42
```

### lock status

Query the current state of a lock.

```bash
qrdb lock status <key>
```

**Output:** Lock info if held, error 404 if not held

```bash
qrdb lock status myresource
qrdb lock status myresource --output json
```

### lock list

List all active locks, optionally filtered by key prefix.

```bash
qrdb lock list [--prefix <prefix>]
```

**Options:**
- `--prefix`: Optional. Filter locks by key prefix

**Output:** Table or JSON array of lock info

```bash
qrdb lock list
qrdb lock list --prefix resource-
qrdb lock list --output json
```

## Examples

```bash
# Basic KV operations
qrdb put mykey myvalue
qrdb get mykey
qrdb del mykey

# Remote server with TLS
qrdb --server https://qrdb.example.com:63002 --ca-file ca.pem kv list

# Lock operations
qrdb lock acquire myresource --ttl 30
qrdb lock status myresource
qrdb lock release myresource --token 42
qrdb lock list --prefix resource-

# Admin operations
qrdb admin health
qrdb admin status --output json

# Barrier management
qrdb barrier init --shares 5 --threshold 3
qrdb barrier unseal --share <share1>

# Database management
qrdb database create --name k8s --engine kv --backend raft --etcd --etcd-listen-addr :2379
qrdb database create --name mydb --engine kv --backend pebble
qrdb database list
qrdb database info k8s

# Update etcd protocol on an existing database
qrdb database update-etcd k8s --enabled --listen-addr :2379 --advertise-urls https://node1:2379
qrdb database update-etcd k8s --disabled
```
