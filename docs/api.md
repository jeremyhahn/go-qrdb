# QRDB API Reference

QRDB exposes four API protocols: REST, gRPC, Unix socket, and MCP. All protocols
delegate to the same service layer, providing identical semantics. Choose the
protocol that best fits your client ecosystem.

| Protocol | Default Port/Path | Transport |
|----------|-------------------|-----------|
| REST | 63002 | HTTPS (HTTP/2) |
| gRPC | 63003 | gRPC with TLS 1.3 |
| Unix | /var/run/qrdbd/qrdbd.sock | gRPC over UDS |
| MCP | 63005 | MCP over HTTP |
| Raft | 63001 | QUIC (internal, mTLS) |

---

## REST HTTP API

The REST server supports HTTP/2 via ALPN, HTTP/3 via QUIC, and HTTP/1.1 as a
fallback. TLS is enabled by default with auto-generated self-signed certificates.

Server timeouts: 5s read, 30s write.

### Configuration

| Flag           | Default | Description                   |
|----------------|---------|-------------------------------|
| `--admin-port` | `63002` | TCP port for the REST server  |
| `--config`     | (none)  | YAML config file path         |

---

### Operational Endpoints

#### GET /status

Returns host identity, Raft address, and per-shard status.

**Response** `200 OK`

```json
{
  "node_id": "nhid-abc123",
  "raft_address": "127.0.0.1:63001",
  "is_leader": true,
  "leader_id": 1,
  "shard_count": 1,
  "is_draining": false,
  "shards": [
    {
      "shard_id": 1,
      "replica_id": 1,
      "leader_id": 1,
      "term": 3,
      "is_leader": true,
      "is_observer": false,
      "is_witness": false,
      "state_machine_type": 0,
      "pending": 0,
      "nodes": {
        "1": "127.0.0.1:63001",
        "2": "127.0.0.1:63003"
      },
      "health": 0,
      "last_applied": 42
    }
  ]
}
```

```bash
curl -sk https://localhost:63002/status | jq .
```

---

#### GET /peers

Returns the peer list for every loaded shard, including leader identification.

**Response** `200 OK`

```json
{
  "shards": [
    {
      "shard_id": 1,
      "leader_id": 1,
      "peers": [
        {
          "replica_id": 1,
          "address": "127.0.0.1:63001",
          "is_leader": true
        },
        {
          "replica_id": 2,
          "address": "127.0.0.1:63003",
          "is_leader": false
        }
      ]
    }
  ]
}
```

```bash
curl -sk https://localhost:63002/peers | jq .
```

---

#### GET /health

Returns aggregate host health. Used by load balancers and orchestrators.

**Response** `200 OK` when healthy, `503 Service Unavailable` when degraded.

```json
{
  "status": "ok",
  "is_leader": true
}
```

The `status` field is one of: `ok`, `degraded`, `sealed`, `unhealthy`.

```bash
curl -sk -o /dev/null -w '%{http_code}' https://localhost:63002/health
```

---

#### POST /drain

Transitions the host into drain mode. New proposals and reads are rejected while
in-flight operations complete.

**Response** `200 OK` on success.

```json
{
  "status": "drained"
}
```

**Response** `503 Service Unavailable` on timeout.

```json
{
  "status": "drain_timeout",
  "error": "drain timed out after 30s"
}
```

```bash
curl -sk -X POST https://localhost:63002/drain | jq .
```

---

#### GET /metrics

Prometheus metrics endpoint. Only available when the server is started with a
Prometheus gatherer configured.

**Response** `200 OK` with `text/plain` Prometheus exposition format.

```bash
curl -sk https://localhost:63002/metrics
```

---

### KV Data Endpoints

All KV operations target shard 1 by default.

#### PUT /kv/{key}

Stores a key-value pair. The request body is the raw value (not JSON-encoded).

| Component    | Detail                     |
|--------------|----------------------------|
| Method       | `PUT`                      |
| Path         | `/kv/{key}`                |
| Content-Type | any (body is stored as-is) |
| Request body | raw value bytes            |

**Response** `204 No Content` on success (no body).

**Errors**

| Code | Condition           |
|------|---------------------|
| 400  | Cannot read body    |
| 500  | Raft proposal error |

**Leader Forwarding**: When the local node is not the leader and leader
forwarding is configured, the request is transparently proxied to the leader.

```bash
curl -sk -X PUT -d 'hello world' https://localhost:63002/kv/mykey
```

---

#### GET /kv/{key}

Retrieves the value for a single key. Returns raw bytes with `application/octet-stream`.

| Component     | Detail                     |
|---------------|----------------------------|
| Method        | `GET`                      |
| Path          | `/kv/{key}`                |
| Response type | `application/octet-stream` |
| Response body | raw value bytes            |

**Response** `200 OK` with the value as the body.

**Errors**

| Code | Condition       |
|------|-----------------|
| 404  | Key not found   |
| 500  | Raft read error |

```bash
curl -sk https://localhost:63002/kv/mykey
```

---

#### DELETE /kv/{key}

Removes a key from the store.

| Component | Detail      |
|-----------|-------------|
| Method    | `DELETE`    |
| Path      | `/kv/{key}` |

**Response** `204 No Content` on success (no body).

**Errors**

| Code | Condition           |
|------|---------------------|
| 500  | Raft proposal error |

```bash
curl -sk -X DELETE https://localhost:63002/kv/mykey
```

---

#### GET /kv?prefix=

Lists keys matching a prefix. Returns a sorted JSON array of key names.
An empty or omitted `prefix` parameter returns all keys.

| Component    | Detail            |
|--------------|-------------------|
| Method       | `GET`             |
| Path         | `/kv`             |
| Query params | `prefix` (string) |

**Response** `200 OK`

```json
{
  "keys": ["config/a", "config/b", "config/c"]
}
```

**Errors**

| Code | Condition       |
|------|-----------------|
| 500  | Raft read error |

```bash
curl -sk 'https://localhost:63002/kv?prefix=config/' | jq .
```

---

### Membership Endpoints

#### POST /membership/add

Adds a node to a shard. The request is proposed through Raft and blocks until
committed.

**Request body** `application/json`

```json
{
  "shard_id": 1,
  "replica_id": 3,
  "address": "10.0.0.3:63001"
}
```

**Response** `200 OK`

```json
{
  "status": "ok"
}
```

**Errors**

| Code | Condition                    |
|------|------------------------------|
| 400  | Malformed JSON               |
| 500  | Raft membership change error |

```bash
curl -sk -X POST -H 'Content-Type: application/json' \
  -d '{"shard_id":1,"replica_id":3,"address":"10.0.0.3:63001"}' \
  https://localhost:63002/membership/add | jq .
```

---

#### POST /membership/remove

Removes a node from a shard.

**Request body** `application/json`

```json
{
  "shard_id": 1,
  "replica_id": 3
}
```

**Response** `200 OK`

```json
{
  "status": "ok"
}
```

**Errors**

| Code | Condition                    |
|------|------------------------------|
| 400  | Malformed JSON               |
| 500  | Raft membership change error |

```bash
curl -sk -X POST -H 'Content-Type: application/json' \
  -d '{"shard_id":1,"replica_id":3}' \
  https://localhost:63002/membership/remove | jq .
```

---

#### POST /snapshot

Requests a snapshot for a shard. Blocks until the snapshot is created.

**Request body** `application/json`

```json
{
  "shard_id": 1
}
```

**Response** `200 OK`

```json
{
  "status": "ok"
}
```

**Errors**

| Code | Condition              |
|------|------------------------|
| 400  | Malformed JSON         |
| 500  | Snapshot request error |

```bash
curl -sk -X POST -H 'Content-Type: application/json' \
  -d '{"shard_id":1}' \
  https://localhost:63002/snapshot | jq .
```

---

### REST Error Handling

All endpoints return errors as JSON with an `error` field:

```json
{
  "error": "description of the error"
}
```

| Code | Meaning                                                    |
|------|------------------------------------------------------------|
| 200  | Success (JSON body)                                        |
| 204  | Success, no content (PUT/DELETE KV)                        |
| 400  | Bad request (malformed JSON, missing key, unreadable body) |
| 404  | Not found (unknown path or KV key)                         |
| 405  | Method not allowed (wrong HTTP method)                     |
| 500  | Internal server error (Raft proposal/read failure)         |
| 503  | Service unavailable (unhealthy host or drain timeout)      |
| 508  | Loop detected (leader forwarding loop)                     |

---

## gRPC API

The gRPC server uses the `qrdb.v1.QRDBService` protobuf service definition.
Proto file: `pkg/api/grpc/proto/qrdbv1/qrdb.proto`.

### Connection

```bash
# Without TLS
grpcurl -plaintext localhost:63003 list

# With TLS (self-signed)
grpcurl -insecure localhost:63003 list
```

### Service Definition

```protobuf
service QRDBService {
    rpc Put(PutRequest) returns (PutResponse);
    rpc Get(GetRequest) returns (GetResponse);
    rpc Delete(DeleteRequest) returns (DeleteResponse);
    rpc List(ListRequest) returns (ListResponse);
    rpc Health(google.protobuf.Empty) returns (HealthResponse);
    rpc Status(google.protobuf.Empty) returns (StatusResponse);
    rpc Peers(google.protobuf.Empty) returns (PeersResponse);
    rpc AddMember(AddMemberRequest) returns (google.protobuf.Empty);
    rpc RemoveMember(RemoveMemberRequest) returns (google.protobuf.Empty);
    rpc RequestSnapshot(RequestSnapshotMessage) returns (google.protobuf.Empty);
    rpc Drain(google.protobuf.Empty) returns (google.protobuf.Empty);
}
```

---

### KV Operations

#### Put

Stores a key-value pair via Raft consensus.

**Request**

```protobuf
message PutRequest {
    string key = 1;
    bytes value = 2;
}
```

**Response**: `PutResponse` (empty message).

```bash
grpcurl -insecure -d '{"key":"mykey","value":"aGVsbG8="}' \
  localhost:63003 qrdb.v1.QRDBService/Put
```

---

#### Get

Retrieves a value by key using a linearizable read.

**Request**

```protobuf
message GetRequest {
    string key = 1;
}
```

**Response**

```protobuf
message GetResponse {
    bytes value = 1;
}
```

Returns gRPC `NOT_FOUND` when the key does not exist.

```bash
grpcurl -insecure -d '{"key":"mykey"}' \
  localhost:63003 qrdb.v1.QRDBService/Get
```

---

#### Delete

Removes a key via Raft consensus.

**Request**

```protobuf
message DeleteRequest {
    string key = 1;
}
```

**Response**: `DeleteResponse` (empty message).

```bash
grpcurl -insecure -d '{"key":"mykey"}' \
  localhost:63003 qrdb.v1.QRDBService/Delete
```

---

#### List

Returns sorted keys matching a prefix.

**Request**

```protobuf
message ListRequest {
    string prefix = 1;
}
```

**Response**

```protobuf
message ListResponse {
    repeated string keys = 1;
}
```

```bash
grpcurl -insecure -d '{"prefix":"config/"}' \
  localhost:63003 qrdb.v1.QRDBService/List
```

---

### Admin Operations

#### Health

Returns the node health status.

**Request**: `google.protobuf.Empty`

**Response**

```protobuf
message HealthResponse {
    string status = 1;  // "ok", "degraded", "sealed", "unhealthy"
    bool leader = 2;
}
```

```bash
grpcurl -insecure localhost:63003 qrdb.v1.QRDBService/Health
```

---

#### Status

Returns detailed node status information.

**Request**: `google.protobuf.Empty`

**Response**

```protobuf
message StatusResponse {
    string node_id = 1;
    string raft_address = 2;
    bool is_leader = 3;
    uint64 leader_id = 4;
    int32 shard_count = 5;
    bool is_draining = 6;
}
```

```bash
grpcurl -insecure localhost:63003 qrdb.v1.QRDBService/Status
```

---

#### Peers

Returns the peer list for all loaded shards.

**Request**: `google.protobuf.Empty`

**Response**

```protobuf
message PeersResponse {
    repeated ShardPeers shards = 1;
}

message ShardPeers {
    uint64 shard_id = 1;
    uint64 leader_id = 2;
    repeated PeerInfo peers = 3;
}

message PeerInfo {
    uint64 replica_id = 1;
    string address = 2;
    bool is_leader = 3;
}
```

```bash
grpcurl -insecure localhost:63003 qrdb.v1.QRDBService/Peers
```

---

#### AddMember

Adds a new node to a shard.

**Request**

```protobuf
message AddMemberRequest {
    uint64 shard_id = 1;
    uint64 replica_id = 2;
    string raft_address = 3;
}
```

**Response**: `google.protobuf.Empty`

```bash
grpcurl -insecure -d '{"shard_id":1,"replica_id":3,"raft_address":"10.0.0.3:63001"}' \
  localhost:63003 qrdb.v1.QRDBService/AddMember
```

---

#### RemoveMember

Removes a node from a shard.

**Request**

```protobuf
message RemoveMemberRequest {
    uint64 shard_id = 1;
    uint64 replica_id = 2;
}
```

**Response**: `google.protobuf.Empty`

```bash
grpcurl -insecure -d '{"shard_id":1,"replica_id":3}' \
  localhost:63003 qrdb.v1.QRDBService/RemoveMember
```

---

#### RequestSnapshot

Triggers a snapshot for the given shard.

**Request**

```protobuf
message RequestSnapshotMessage {
    uint64 shard_id = 1;
}
```

**Response**: `google.protobuf.Empty`

```bash
grpcurl -insecure -d '{"shard_id":1}' \
  localhost:63003 qrdb.v1.QRDBService/RequestSnapshot
```

---

#### Drain

Transitions the node into drain mode.

**Request**: `google.protobuf.Empty`

**Response**: `google.protobuf.Empty`

```bash
grpcurl -insecure localhost:63003 qrdb.v1.QRDBService/Drain
```

---

### gRPC Error Codes

The gRPC server maps service-layer errors to standard gRPC status codes:

| gRPC Code     | Condition                          |
|---------------|------------------------------------|
| `OK`          | Success                            |
| `NOT_FOUND`   | Key not found                      |
| `UNAVAILABLE` | Not leader / drain in progress     |
| `INTERNAL`    | Propose, read, membership failure  |

### Interceptors

The gRPC server includes two unary interceptors:

1. **Recovery**: Catches panics in handlers and returns `INTERNAL` instead of crashing the server.
2. **Logging**: Logs each RPC call with method name, duration, and gRPC status code.

---

## Unix Socket API

gRPC over Unix domain socket for local access. Reuses the same gRPC service
implementation and interceptors — identical method surface to the gRPC API above.

### Configuration

```yaml
unix:
  socket_path: /var/run/qrdbd/qrdbd.sock
```

The Unix socket server is enabled via `protocols.unix: true`. No TLS is needed
since Unix sockets are inherently local. Socket permissions restrict access to
the socket owner and group.

### CLI Usage

```bash
qrdb --server unix:///var/run/qrdbd/qrdbd.sock kv get mykey
```

---

## MCP API

Model Context Protocol server for AI/LLM tool integration. Exposes all service
operations as MCP tools via `github.com/mark3labs/mcp-go`.

### Configuration

```yaml
mcp:
  port: 63005
  endpoint: /mcp
```

### Tool Surface

All 9 service interfaces are exposed as MCP tools:

| Category | Tools |
|----------|-------|
| KV | `put`, `get`, `delete`, `list`, `exists` |
| MVCC | `put_with_lease`, `get_revision`, `get_at_revision`, `range`, `compact`, `current_revision` |
| Transaction | `transaction` |
| Watch | `watch_key`, `watch_prefix` |
| Batch | `batch` |
| Lease | `lease_grant`, `lease_revoke`, `lease_keepalive`, `lease_ttl`, `lease_list` |
| Index | `index_create`, `index_drop`, `index_query`, `index_scan` |
| TimeSeries | `ts_write`, `ts_write_batch`, `ts_query`, `ts_aggregate` |
| Admin | `health`, `status`, `peers`, `add_member`, `remove_member`, `snapshot`, `drain` |
| Barrier | `barrier_init`, `barrier_seal`, `barrier_unseal`, `barrier_status`, `barrier_rotate`, `barrier_rekey`, and tenant operations |

### Authentication

MCP uses OAuth 2.1 Bearer token validation via external HTTP mux wrapping:

```go
mux := http.NewServeMux()
mux.Handle("/mcp", authMiddleware(mcpHandler))
```

An `HTTPContextFunc` injects the authenticated identity into the MCP request context.
