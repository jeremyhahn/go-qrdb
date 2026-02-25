# Build stage: compile the TiKV benchmark binary
FROM golang:1.26-bookworm AS builder

WORKDIR /src

# Copy go.mod files first for layer caching.
COPY go.mod go.sum ./
COPY test/performance/go.mod test/performance/
COPY contrib/qrdb/test/performance/tikv/go.mod contrib/qrdb/test/performance/tikv/go.sum contrib/qrdb/test/performance/tikv/

# Download dependencies.
WORKDIR /src/contrib/qrdb/test/performance/tikv
RUN go mod download

# Copy source code.
WORKDIR /src
COPY test/performance/perfresult/ test/performance/perfresult/
COPY contrib/qrdb/test/performance/tikv/*.go contrib/qrdb/test/performance/tikv/

# Build benchmark binary.
WORKDIR /src/contrib/qrdb/test/performance/tikv
RUN CGO_ENABLED=0 go test -c -tags=benchmark -o /tikv-bench .

# Run stage
FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates wget \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder /tikv-bench /tikv-bench
COPY contrib/qrdb/test/performance/tikv/docker-compose.yml /tikv/docker-compose.yml
COPY contrib/qrdb/test/performance/tikv/scripts/ /tikv/scripts/

WORKDIR /tikv
ENTRYPOINT ["/tikv-bench"]
CMD ["-test.bench=.", "-test.benchtime=1s", "-test.timeout=600s", "-test.benchmem", "-test.count=1", "-test.v"]
