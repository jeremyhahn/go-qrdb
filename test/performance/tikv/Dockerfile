# Build stage: compile the TiKV benchmark binary
#
# Build context: go-qrdb root directory.
#
# NOTE: this Dockerfile requires go-quicraft source for the perfresult module
# dependency (github.com/jeremyhahn/go-quicraft/test/performance). Supply it
# via a named BuildKit context:
#   docker build --build-context go-quicraft=/path/to/go-quicraft ...
FROM golang:1.26.1-bookworm AS builder

WORKDIR /src

# Copy go.mod files first for layer caching.
COPY go.mod go.sum ./
COPY test/performance/tikv/go.mod test/performance/tikv/go.sum test/performance/tikv/

# Rewrite the perfresult replace directive to the Docker path.
WORKDIR /src/test/performance/tikv
RUN go mod edit \
        -replace "github.com/jeremyhahn/go-quicraft/test/performance=/go-quicraft/test/performance" && \
    go mod download

# Copy source code.
WORKDIR /src
COPY test/performance/tikv/*.go test/performance/tikv/

# Build benchmark binary.
WORKDIR /src/test/performance/tikv
RUN CGO_ENABLED=0 go test -c -tags=benchmark -o /tikv-bench .

# Run stage
FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates wget \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder /tikv-bench /tikv-bench
COPY test/performance/tikv/docker-compose.yml /tikv/docker-compose.yml
COPY test/performance/tikv/scripts/ /tikv/scripts/

WORKDIR /tikv
ENTRYPOINT ["/tikv-bench"]
CMD ["-test.bench=.", "-test.benchtime=1s", "-test.timeout=600s", "-test.benchmem", "-test.count=1", "-test.v"]
