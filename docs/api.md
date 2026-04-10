# QRDB API Reference

QRDB exposes five API protocols: REST, gRPC, Unix socket, MCP, and etcd. All
protocols delegate to the same service layer, providing identical semantics.
Choose the protocol that best fits your client ecosystem.

| Protocol | Default Port/Path | Transport |
|----------|-------------------|-----------|
| REST | 63002 | HTTPS (HTTP/2) |
| gRPC | 63003 | gRPC with TLS 1.3 |
| Unix | /var/run/qrdbd/qrdbd.sock | gRPC over UDS |
| MCP | 63005 | MCP over HTTP |
| etcd | 2379 | gRPC + mTLS (per-database) |
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

### GET /v1/capabilities

Returns the feature capabilities of the server, reflecting compile-time build tags.

**Response** `200 OK`

```json
{
  "kv": true,
  "mvcc": true,
  "transaction": true,
  "batch": true,
  "watch": true,
  "lease": true,
  "index": true,
  "timeSeries": false,
  "vector": true
}
```

```bash
curl -sk https://localhost:63002/v1/capabilities | jq .
```

---

### Management Endpoints

User, role, and certificate management endpoints for RBAC and mTLS identity lifecycle.

#### User Management

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/v1/users` | Create user |
| `GET` | `/v1/users` | List users (query: `tenant_id`) |
| `GET` | `/v1/users/{id}` | Get user |
| `PUT` | `/v1/users/{id}` | Update user |
| `DELETE` | `/v1/users/{id}` | Delete user |
| `POST` | `/v1/users/{id}/enable` | Enable user |
| `POST` | `/v1/users/{id}/disable` | Disable user |

#### Role Management

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/v1/roles` | Create role |
| `GET` | `/v1/roles` | List roles |
| `GET` | `/v1/roles/{id}` | Get role |
| `PUT` | `/v1/roles/{id}` | Update role |
| `DELETE` | `/v1/roles/{id}` | Delete role |
| `POST` | `/v1/roles/assign` | Assign role to user |
| `POST` | `/v1/roles/unassign` | Unassign role from user |
| `GET` | `/v1/permissions` | List all permissions |

#### Certificate Management

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/v1/certs/generate` | Generate client certificate |
| `POST` | `/v1/certs/export` | Export as PKCS#12 |
| `POST` | `/v1/certs/revoke` | Revoke certificate |
| `GET` | `/v1/certs` | List certificates (query: `tenant_id`) |

---

### Lock Endpoints

Distributed lock operations with fencing tokens for stale holder detection.

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/v1/lock/acquire` | Blocking lock acquisition |
| `POST` | `/v1/lock/trylock` | Non-blocking lock attempt |
| `POST` | `/v1/lock/release` | Release held lock |
| `POST` | `/v1/lock/extend` | Refresh lock TTL |
| `GET` | `/v1/lock/status?key=...` | Query lock state |
| `GET` | `/v1/lock/list?prefix=...` | List active locks |

**Errors:**

| Code | Condition |
|------|-----------|
| 400 | Invalid TTL, empty key |
| 403 | Stale fencing token (NotLockHolderError) |
| 404 | Lock not found |
| 409 | Lock held by another holder |

For detailed request/response formats and examples, see [18-distributed-locking.md](18-distributed-locking.md#rest-api).

---

### Vector Endpoints (build tag: qrdb_vector)

Vector database operations are available when the server is compiled with the `qrdb_vector` build tag.

#### Namespace Management

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/v1/vector/namespaces` | Create namespace |
| `GET` | `/v1/vector/namespaces` | List namespaces |
| `GET` | `/v1/vector/namespaces/{name}` | Get namespace |
| `PUT` | `/v1/vector/namespaces/{name}` | Update namespace |
| `DELETE` | `/v1/vector/namespaces/{name}` | Delete namespace |

#### Vector CRUD

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/v1/vector/{namespace}/upsert` | Upsert vector |
| `GET` | `/v1/vector/{namespace}/{id}` | Get vector |
| `DELETE` | `/v1/vector/{namespace}/{id}` | Delete vector |
| `POST` | `/v1/vector/{namespace}/batch-upsert` | Batch upsert vectors |
| `GET` | `/v1/vector/{namespace}` | List vectors |
| `POST` | `/v1/vector/{namespace}/metadata` | Update vector metadata |

#### Search

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/v1/vector/{namespace}/search` | Similarity search |
| `POST` | `/v1/vector/{namespace}/search-metadata` | Metadata-filtered search |
| `POST` | `/v1/vector/{namespace}/search-hybrid` | Hybrid search (vector + metadata) |
| `POST` | `/v1/vector/{namespace}/search-by-id` | Search by vector ID |

#### Maintenance

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/v1/vector/{namespace}/index/rebuild` | Rebuild vector index |
| `GET` | `/v1/vector/{namespace}/index/progress` | Get rebuild progress |
| `POST` | `/v1/vector/{namespace}/compact` | Compact namespace |
| `GET` | `/v1/vector/{namespace}/stats` | Get namespace statistics |

---

### CORS

When CORS is configured, the REST server adds Cross-Origin Resource Sharing headers for browser-based access. Configurable via the `cors` section in the YAML config:

```yaml
cors:
  allowed_origins: ["*"]
  allowed_headers: ["Authorization", "Content-Type"]
  max_age: 3600
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
    rpc Capabilities(CapabilitiesRequest) returns (CapabilitiesResponse);
    // User management
    rpc CreateUser(CreateUserRequest) returns (User);
    rpc GetUser(GetUserRequest) returns (User);
    rpc UpdateUser(UpdateUserRequest) returns (User);
    rpc DeleteUser(DeleteUserRequest) returns (google.protobuf.Empty);
    rpc ListUsers(ListUsersRequest) returns (ListUsersResponse);
    rpc EnableUser(EnableUserRequest) returns (google.protobuf.Empty);
    rpc DisableUser(DisableUserRequest) returns (google.protobuf.Empty);
    // Role management (Create, Get, Update, Delete, List, Assign, Unassign, ListPermissions)
    // Certificate management (Generate, Export, Revoke, List)
    // Vector operations (20 RPCs when qrdb_vector tag enabled)
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

### Lock Operations

Lock acquisition and management with fencing tokens.

| Method | Description |
|--------|-------------|
| `LockAcquire` | Blocking lock acquisition |
| `LockTryLock` | Non-blocking lock attempt |
| `LockRelease` | Release held lock |
| `LockExtend` | Refresh lock TTL |
| `LockStatus` | Query lock state |
| `LockList` | List active locks |

**Errors:**

| gRPC Code | Condition |
|-----------|-----------|
| `INVALID_ARGUMENT` | Empty key, invalid TTL |
| `ALREADY_EXISTS` | Lock held by another holder |
| `NOT_FOUND` | Lock does not exist |
| `FAILED_PRECONDITION` | Stale fencing token |

For detailed message definitions and examples, see [18-distributed-locking.md](18-distributed-locking.md#grpc-api).

```bash
# Acquire lock
grpcurl -insecure -d '{"key":"mylock","ttl":30}' \
  localhost:63003 qrdb.v1.QRDBService/LockAcquire

# Release lock
grpcurl -insecure -d '{"key":"mylock","fencing_token":42}' \
  localhost:63003 qrdb.v1.QRDBService/LockRelease

# Query lock status
grpcurl -insecure -d '{"key":"mylock"}' \
  localhost:63003 qrdb.v1.QRDBService/LockStatus

# List all locks
grpcurl -insecure -d '{}' \
  localhost:63003 qrdb.v1.QRDBService/LockList
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
| Users | `qrdb_user_create`, `qrdb_user_get`, `qrdb_user_update`, `qrdb_user_delete`, `qrdb_user_list`, `qrdb_user_enable`, `qrdb_user_disable` |
| Roles | `qrdb_role_create`, `qrdb_role_get`, `qrdb_role_update`, `qrdb_role_delete`, `qrdb_role_list`, `qrdb_role_assign`, `qrdb_role_unassign`, `qrdb_permissions_list` |
| Certs | `qrdb_cert_generate`, `qrdb_cert_export`, `qrdb_cert_revoke`, `qrdb_cert_list` |
| Vector | 20 tools for namespace, vector CRUD, search, and maintenance |
| Capabilities | `qrdb_capabilities` |

### Authentication

MCP uses OAuth 2.1 Bearer token validation via external HTTP mux wrapping:

```go
mux := http.NewServeMux()
mux.Handle("/mcp", authMiddleware(mcpHandler))
```

An `HTTPContextFunc` injects the authenticated identity into the MCP request context.

---

## etcd API

etcd v3 gRPC compatibility layer, exposing per-database etcd endpoints. Each
database with `etcd: enabled` spawns its own gRPC server implementing the core
etcd v3 API surface. See [etcd documentation](etcd/README.md) for full details.

### Configuration

etcd is configured per-database, not globally:

```yaml
databases:
  - name: k8s-prod
    engine: kv
    backend: raft
    protocol:
      etcd:
        enabled: true
        listen_addr: ":2379"
        advertise_client_urls:
          - "https://node1:2379"
```

### Implemented Services

| etcd gRPC Service | RPCs | Description |
|---|---|---|
| `KV` | `Range`, `Put`, `DeleteRange`, `Txn`, `Compact` | Key-value CRUD, transactions, compaction |
| `Watch` | `Watch` | Bidirectional streaming watch with prefix support |
| `Lease` | `LeaseGrant`, `LeaseRevoke`, `LeaseKeepAlive`, `LeaseTimeToLive`, `LeaseLeases` | Distributed lease management |
| `Cluster` | `MemberList` | Cluster membership (maps to QRDB peers) |
| `Maintenance` | `Status`, `Defragment`, `Alarm` | Node status, compaction, alarm state |
| `Auth` | -- | Stub (returns `Unimplemented`; mTLS used instead) |

### Connection

```bash
# With mTLS (required)
export ETCDCTL_API=3
export ETCDCTL_ENDPOINTS=https://localhost:2379
export ETCDCTL_CACERT=ca.pem
export ETCDCTL_CERT=client.pem
export ETCDCTL_KEY=client-key.pem

etcdctl put mykey "hello"
etcdctl get mykey
etcdctl watch --prefix /events/
etcdctl lease grant 60
etcdctl member list
etcdctl endpoint status
```

### Authentication

mTLS only. The etcd `Auth` service is stubbed out. Client identity is extracted
from the TLS client certificate (CN = username, OU = tenant ID). See the
[authentication documentation](auth/README.md) for details.

### Error Mapping

etcd gRPC errors map to standard gRPC status codes consistent with the etcd v3 API contract:

| gRPC Code | Condition |
|---|---|
| `OK` | Success |
| `NOT_FOUND` | Key not found (empty range response) |
| `UNAVAILABLE` | Not leader / node draining |
| `FAILED_PRECONDITION` | Transaction compare failed |
| `RESOURCE_EXHAUSTED` | Lease table full |
| `UNIMPLEMENTED` | Auth RPCs (mTLS used instead) |
| `INTERNAL` | Raft proposal/read failure |
