# go-qrdb Architecture

## Directory Structure
```
cmd/qrdbd/          - Server daemon entrypoint (cobra commands: start, status, peers, tls, errors)
pkg/
  api/
    rest/            - REST API server (HTTP/2 + HTTP/3, leader forwarding, Prometheus)
      server.go      - Server struct with functional options, route dispatch
      handlers.go    - HTTP handler functions
      errors.go      - REST-specific error types
    grpc/            - gRPC API server
      server.go      - gRPC server setup
      service.go     - gRPC service implementation
      errors.go      - gRPC-specific error types
      proto/qrdbv1/  - Protobuf definitions and generated code
  config/            - YAML config loading and validation
    config.go        - Config structs, DefaultConfig(), Load(), Validate()
    errors.go        - Config-specific error types
  server/            - Multi-protocol server orchestrator
    server.go        - Coordinates REST + gRPC + Raft startup
  service/           - Service layer (shared by REST + gRPC)
    service.go       - KVService interface (Put/Get/Delete/List)
                       AdminService interface (Health/Status/Peers/AddMember/RemoveMember/Drain)
                       Host interface (abstraction over Raft host)
    errors.go        - Service error types
  store/             - Storage backend
    pebble.go        - PebbleDB DiskStateMachine implementation
    factory.go       - Store factory
    errors.go        - Store-specific error types
configs/             - YAML configuration files (qrdb.yaml, qrdb-dev.yaml)
test/
  integration/       - Integration tests (run in devcontainer)
  performance/       - Benchmark tests and performance docs
docs/                - Documentation (api.md)
.devcontainer/       - VS Code devcontainer (Dockerfile, docker-compose.yml, entrypoint.sh)
```

## Key Interfaces
- **KVService**: Put, Get, Delete, List operations
- **AdminService**: Health, Status, Peers, AddMember, RemoveMember, RequestSnapshot, Drain
- **Host**: Abstraction over Raft node host (ID, SyncPropose, SyncRead, etc.)

## Design Principles
- Service layer is the CRITICAL abstraction: ALL protocol handlers delegate to service interfaces
- Clean abstraction layers between API → Service → Store
- Map-based dispatch over switch statements
- Functional options pattern for server configuration (e.g., WithGatherer, WithTLS, WithHost)
