# go-qrdb Project Overview

## Purpose
go-qrdb (QuicRaft DB) is a high-performance distributed key-value store powered by multi-group Raft consensus with QUIC transport. Built on top of the `go-quicraft` consensus library.

## Version
- Current version: 0.1.0 (from VERSION file)
- Module: `github.com/jeremyhahn/go-qrdb`
- Go version: 1.26.1
- License: Apache License 2.0

## Tech Stack
- **Language**: Go 1.26.1
- **Consensus**: Multi-group Raft via go-quicraft (local dependency via replace directive at `../go-quicraft`)
- **Transport**: QUIC (quic-go v0.59.0)
- **Storage**: PebbleDB (cockroachdb/pebble/v2 v2.1.4)
- **APIs**: REST (HTTP/2 + HTTP/3) and gRPC (protobuf)
- **CLI**: Cobra commands (start, status, peers, version)
- **Metrics**: Prometheus (client_golang v1.23.2)
- **Config**: YAML-based (gopkg.in/yaml.v3)
- **Container**: Docker (multi-stage build, debian:bookworm-slim runtime)

## Ports
- 63001: Raft transport
- 63002: REST admin API
- 63003: gRPC API

## Critical Rules
- **NEVER run git commands in this repository!**
- Production-grade only - no shortcuts or dev-only approaches
- Always use Context7 MCP for library docs
