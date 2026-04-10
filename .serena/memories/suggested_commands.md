# go-qrdb Suggested Commands

## Build
- `make build` - Compile server and client binaries
- `make build-server` - Compile daemon binary to bin/qrdbd
- `make build-client` - Compile client binary in devcontainer (CLI + GUI)
- `make clean` - Remove build artifacts and coverage reports
- `make proto` - Regenerate protobuf code
- `make all` - Build, test, and lint

## Unit Tests
- `make test` - Run all unit tests with race detector
- `make test-config` - Test config package
- `make test-service` - Test service package
- `make test-store` - Test store package
- `make test-rest` - Test REST API package
- `make test-grpc` - Test gRPC package
- `make test-server` - Test server package
- `make test-cmd` - Test CLI commands

## Integration Tests (run in devcontainer)
- `make integration-test` - Run all integration tests in Docker

## Benchmarks (run in devcontainer)
- `make bench-qrdb-kv-fair` - Run KV fair benchmarks (KV_BENCHTIME=5s default)

## Code Coverage (minimum 91%)
- `make coverage` - Run total coverage report
- `make coverage-config` - Coverage for config package
- `make coverage-service` - Coverage for service package
- `make coverage-store` - Coverage for store package
- `make coverage-rest` - Coverage for REST package
- `make coverage-grpc` - Coverage for gRPC package
- `make coverage-server` - Coverage for server package
- `make coverage-cmd` - Coverage for CLI commands

## Linting & Static Analysis
- `make lint` - Run golangci-lint (5min timeout)
- `make vet` - Run go vet
- `make fmt` - Check gofmt formatting

## Security
- `make gosec` - Run gosec security scanner
- `make vuln` - Run govulncheck vulnerability scanner

## CI Pipeline (runs in devcontainer)
- `make ci` - Full CI: fmt, vet, lint, gosec, vuln, build, test

## Docker
- `make docker-build` - Build production image
- `make docker-run` - Run production container
- `make docker-run-devcontainer` - Run devcontainer shell

## Running the Application
- `bin/qrdbd start --config configs/qrdb.yaml` - Start the server daemon
- `bin/qrdbd status` - Check server status
- `bin/qrdbd peers` - List cluster peers

## System Utilities (Linux)
- `ls`, `cd`, `grep`, `find` - Standard Linux utilities
- `gofmt` - Go code formatter
- `golangci-lint` - Go linter
- `gosec` - Go security scanner
- `govulncheck` - Go vulnerability checker
- `protoc` - Protobuf compiler
