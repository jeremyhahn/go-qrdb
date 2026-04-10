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

# Build stage: compile the QRDB binary (with etcd) and the benchmark test binary.
FROM golang:1.26.1-bookworm AS builder

WORKDIR /src
COPY . .

# Build the QRDB server binary with etcd support.
# Rewrite the replace directive: host layout (../go-quicraft) -> Docker layout (../..)
WORKDIR /src/contrib/qrdb
RUN sed -i 's|=> ../go-quicraft|=> ../..|' go.mod && \
    go mod tidy && \
    go build -tags="codec_json raft qrdb_etcd" -o /usr/local/bin/qrdb ./cmd/qrdbd

# Build the benchmark test binary.
WORKDIR /src/contrib/qrdb/test/performance/etcd-benchmark
RUN go mod tidy && go test -c -tags=benchmark -o /etcd-bench .

# Run stage: minimal image with both binaries.
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/*
COPY --from=builder /usr/local/bin/qrdb /usr/local/bin/qrdb
COPY --from=builder /etcd-bench /usr/local/bin/etcd-bench

ENTRYPOINT ["/usr/local/bin/etcd-bench"]
CMD ["-test.bench=.", "-test.benchtime=5s", "-test.timeout=600s", "-test.benchmem", "-test.count=1", "-test.v"]
