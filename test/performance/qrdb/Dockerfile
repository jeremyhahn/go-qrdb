# Copyright 2026 Jeremy Hahn
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
# http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Build stage: compile both the QRDB binary and the benchmark test binary.
#
# Build context: go-qrdb root directory.
#
# NOTE: this Dockerfile requires go-quicraft source for both the go-quicraft
# module dependency (used by go-qrdb) and the perfresult module dependency
# (used by the benchmark sub-module). Supply it via a named BuildKit context:
#   docker build --build-context go-quicraft=/path/to/go-quicraft ...
FROM golang:1.27.4-bookworm AS builder

WORKDIR /src
COPY . .

# Build the QRDB server binary used by SetupQRDBCluster.
# Rewrite the replace directive: ../go-quicraft (host) -> /go-quicraft (Docker).
WORKDIR /src
RUN go mod edit -replace github.com/jeremyhahn/go-quicraft=/go-quicraft && \
    go mod tidy && go build -o /usr/local/bin/qrdb ./cmd/qrdbd

# Build the benchmark test binary.
# Rewrite replace directives to absolute Docker paths:
#   go-qrdb                        -> /src                (QRDB module root)
#   go-quicraft                    -> /go-quicraft         (quicraft root)
#   go-quicraft/test/performance   -> /go-quicraft/test/performance
WORKDIR /src/test/performance/qrdb
RUN go mod edit \
        -replace github.com/jeremyhahn/go-qrdb=/src \
        -replace github.com/jeremyhahn/go-quicraft=/go-quicraft \
        -replace "github.com/jeremyhahn/go-quicraft/test/performance=/go-quicraft/test/performance" && \
    go mod tidy && go test -c -tags=benchmark -o /qrdb-bench .

# Run stage: minimal image with both binaries.
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/*
COPY --from=builder /usr/local/bin/qrdb /usr/local/bin/qrdb
COPY --from=builder /qrdb-bench /usr/local/bin/qrdb-bench

ENTRYPOINT ["/usr/local/bin/qrdb-bench"]
CMD ["-test.bench=.", "-test.benchtime=5s", "-test.timeout=600s", "-test.benchmem", "-test.count=1", "-test.v"]
