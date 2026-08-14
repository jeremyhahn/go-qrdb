// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build integration

package integration_test

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/cmd/qrdb-sqlite/vtab"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/qrdbdharness"
	sdktransport "github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// testServer — the shared qrdbd subprocess
//
// Building qrdbd, minting its TLS material, reserving listen ports, spawning
// the process and waiting for it to become ready all live in
// sdk/go/testutil/qrdbdharness, which this module shares with the root
// module's test/integration suite and the GORM conformance module. Only the
// protocol-address plumbing the SQLite gateway needs is local.
// ---------------------------------------------------------------------------

const (
	// serverOrganization is the X.509 Organization on this suite's harness
	// certificates, purely so a stray certificate is attributable.
	serverOrganization = "qrdb-sqlite-test"

	// adminUser and adminTenant are the CN/OU the qrdb mTLS authenticator maps
	// to the identity firstboot.Initialize provisions: the admin user in the
	// reserved system tenant, which the system-tenant rule lifts to
	// platform-wide authority.
	adminUser   = "admin"
	adminTenant = "system"
)

// testServer holds the addresses and resources of the shared qrdbd subprocess.
type testServer struct {
	restPort       int
	grpcPort       int
	quicPort       int // 0 when QUIC is disabled
	mcpPort        int
	unixSocket     string
	dataDir        string
	ca             *qrdbdharness.CA
	clientCertPath string
	clientKeyPath  string
	daemon         *qrdbdharness.Daemon
}

// restAddr returns the REST server address.
func (s *testServer) restAddr() string {
	return fmt.Sprintf("https://127.0.0.1:%d", s.restPort)
}

// grpcAddr returns the gRPC server address (host:port, no scheme).
func (s *testServer) grpcAddr() string {
	return fmt.Sprintf("127.0.0.1:%d", s.grpcPort)
}

// quicAddr returns the QUIC server address (host:port, no scheme).
func (s *testServer) quicAddr() string {
	return fmt.Sprintf("127.0.0.1:%d", s.quicPort)
}

// mcpAddr returns the MCP server address with https:// scheme required by the SSE transport.
func (s *testServer) mcpAddr() string {
	return fmt.Sprintf("https://127.0.0.1:%d", s.mcpPort)
}

// healthURL returns the REST /health endpoint.
func (s *testServer) healthURL() string {
	return s.restAddr() + "/health"
}

// statusURL returns the REST /status endpoint.
func (s *testServer) statusURL() string {
	return s.restAddr() + "/status"
}

// addrFor returns the protocol-appropriate address string.
func (s *testServer) addrFor(proto protocol) string {
	switch proto {
	case protoREST:
		return s.restAddr()
	case protoGRPC:
		return s.grpcAddr()
	case protoUnix:
		return s.unixSocket
	case protoQUIC:
		return s.quicAddr()
	case protoMCP:
		return s.mcpAddr()
	default:
		return ""
	}
}

// stop shuts the qrdbd process down and waits for it to exit.
func (s *testServer) stop() {
	if s.daemon == nil {
		return
	}
	_ = s.daemon.Stop()
}

// startSharedServer builds qrdbd, generates ephemeral TLS certificates, starts
// a single-node cluster with all protocols enabled, and waits for it to be
// healthy and elected leader. It is called once from TestMain.
func startSharedServer() (*testServer, error) {
	bin, err := qrdbdharness.BuildBinary(qrdbdharness.DaemonPackage)
	if err != nil {
		return nil, err
	}

	// Create a persistent temp directory for all server files. This must
	// outlive TestMain so os.MkdirTemp is used instead of t.TempDir().
	dir, err := os.MkdirTemp("", "qrdbd-sqlite-server-*")
	if err != nil {
		return nil, fmt.Errorf("MkdirTemp: %w", err)
	}

	dataDir := filepath.Join(dir, "data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("mkdir data: %w", err)
	}
	certDir := filepath.Join(dir, "certs")

	// Generate CA + node cert + admin client cert.
	ca, err := qrdbdharness.NewCA(certDir, qrdbdharness.WithCAOrganization(serverOrganization))
	if err != nil {
		return nil, err
	}
	node, err := ca.Issue(certDir,
		qrdbdharness.WithSubject("test-node", ""),
		qrdbdharness.WithCertOrganization(serverOrganization),
		qrdbdharness.WithServerAuth(),
	)
	if err != nil {
		return nil, err
	}
	client, err := ca.Issue(certDir,
		qrdbdharness.WithSubject(adminUser, adminTenant),
		qrdbdharness.WithCertOrganization(serverOrganization),
	)
	if err != nil {
		return nil, err
	}

	// Reserve listen ports from the harness's non-ephemeral window: the Raft
	// transport listens on UDP (QUIC), everything else on TCP.
	raftPort, err := qrdbdharness.ReserveUDPPort()
	if err != nil {
		return nil, err
	}
	tcpPorts, err := qrdbdharness.ReserveTCPPorts(3)
	if err != nil {
		return nil, err
	}
	restPort, grpcPort, mcpPort := tcpPorts[0], tcpPorts[1], tcpPorts[2]

	unixSocket := filepath.Join(dir, "qrdbd.sock")

	args := []string{
		"start",
		"--listen", fmt.Sprintf("0.0.0.0:%d", raftPort),
		"--raft-address", fmt.Sprintf("127.0.0.1:%d", raftPort),
		"--data-dir", dataDir,
		"--sm", "mvcc",
		"--discovery", "static",
		"--peers", fmt.Sprintf("1=127.0.0.1:%d", raftPort),
		"--shard-id", "1",
		"--replica-id", "1",
		"--admin-port", fmt.Sprintf("%d", restPort),
		"--grpc-port", fmt.Sprintf("%d", grpcPort),
		"--mcp-port", fmt.Sprintf("%d", mcpPort),
		"--unix-socket-path", unixSocket,
		"--enable-rest=true",
		"--enable-grpc=true",
		"--enable-unix=true",
		"--enable-mcp=true",
		"--log-level", "warn",
		"--log-format", "json",
	}
	args = append(args, ca.TLSFlags(node)...)

	// Subprocess output goes to os.Stderr rather than t.Log: the shared server
	// outlives every individual test, so no *testing.T is in scope.
	daemon, err := qrdbdharness.Start(bin, args,
		qrdbdharness.WithOutput(qrdbdharness.NewPrefixWriter(os.Stderr, "[qrdbd] ")))
	if err != nil {
		return nil, err
	}

	srv := &testServer{
		restPort:       restPort,
		grpcPort:       grpcPort,
		quicPort:       0, // QUIC not enabled; avoids QUIC test failures on hosts without UDP
		mcpPort:        mcpPort,
		unixSocket:     unixSocket,
		dataDir:        dataDir,
		ca:             ca,
		clientCertPath: client.CertPath,
		clientKeyPath:  client.KeyPath,
		daemon:         daemon,
	}

	// Wait for the REST health endpoint to return 200 and for a leader to
	// be elected before returning. Use a 120-second deadline to accommodate
	// slow CI environments. /health is public; /status is not, so leader
	// polling presents the admin client certificate.
	if err := qrdbdharness.WaitForHTTPOK(srv.healthURL(), ca.Pool, nil, 120*time.Second); err != nil {
		srv.stop()
		return nil, err
	}

	adminCert, err := client.TLSCertificate()
	if err != nil {
		srv.stop()
		return nil, err
	}
	if err := qrdbdharness.WaitForLeader(srv.statusURL(), ca.Pool, &adminCert, 60*time.Second); err != nil {
		srv.stop()
		return nil, err
	}

	return srv, nil
}

// ---------------------------------------------------------------------------
// Test adapters — thin wrappers around the SDK client
//
// Each adapter satisfies the corresponding vtab store interface and delegates
// all operations to the real SDK client, which calls the live qrdbd server
// over the configured transport.
// ---------------------------------------------------------------------------

// testKVAdapter wraps the SDK KVClient to satisfy vtab.KVStore.
type testKVAdapter struct {
	kv *sdk.Client
}

func (a *testKVAdapter) Get(ctx context.Context, key string) ([]byte, error) {
	val, err := a.kv.KV().Get(ctx, key)
	if err != nil {
		// KeyNotFoundError is not an error for the vtab layer — return nil.
		if isKeyNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return val, nil
}

func (a *testKVAdapter) Scan(ctx context.Context, prefix string) (map[string][]byte, error) {
	return a.kv.KV().Scan(ctx, prefix)
}

func (a *testKVAdapter) Put(ctx context.Context, key string, value []byte) error {
	return a.kv.KV().Put(ctx, key, value)
}

func (a *testKVAdapter) Delete(ctx context.Context, key string) error {
	return a.kv.KV().Delete(ctx, key)
}

// testDatapointAdapter wraps the SDK TimeSeriesClient to satisfy
// vtab.DatapointStore.
type testDatapointAdapter struct {
	ts *sdk.Client
}

func (a *testDatapointAdapter) WriteDatapoint(ctx context.Context, metric string, timestampNs int64, value float64, tagsJSON string) error {
	dp := &sdktransport.TSDataPoint{
		Metric:      metric,
		TimestampNs: timestampNs,
		Value:       value,
		Tags:        parseTSTagsJSON(tagsJSON),
	}
	return a.ts.TimeSeries().TSWrite(ctx, dp)
}

func (a *testDatapointAdapter) QueryDatapoints(ctx context.Context, metric string, startNs, endNs int64) ([]vtab.DatapointRow, error) {
	resp, err := a.ts.TimeSeries().TSQuery(ctx, &sdktransport.TSQueryRequest{
		Metric:  metric,
		StartNs: startNs,
		EndNs:   endNs,
	})
	if err != nil {
		return nil, err
	}
	rows := make([]vtab.DatapointRow, 0, len(resp.Points))
	for _, p := range resp.Points {
		rows = append(rows, vtab.DatapointRow{
			Metric:      p.Metric,
			TimestampNs: p.TimestampNs,
			Value:       p.Value,
			Tags:        tsTagsToJSON(p.Tags),
		})
	}
	return rows, nil
}

func (a *testDatapointAdapter) ScanAllDatapoints(ctx context.Context) ([]vtab.DatapointRow, error) {
	resp, err := a.ts.TimeSeries().TSQuery(ctx, &sdktransport.TSQueryRequest{
		Metric:  "",
		StartNs: 0,
		EndNs:   1<<62 - 1,
	})
	if err != nil {
		// If unsupported (server requires a non-empty metric), return empty so
		// the vtab falls back to per-metric queries from its filter.
		return nil, nil
	}
	rows := make([]vtab.DatapointRow, 0, len(resp.Points))
	for _, p := range resp.Points {
		rows = append(rows, vtab.DatapointRow{
			Metric:      p.Metric,
			TimestampNs: p.TimestampNs,
			Value:       p.Value,
			Tags:        tsTagsToJSON(p.Tags),
		})
	}
	return rows, nil
}

// parseTSTagsJSON converts a JSON-encoded tags string (the vtab format) into
// the SDK's []TSTag slice. The vtab canonical format is a JSON array of
// {"key": "...", "value": "..."} objects. Returns nil for empty or invalid input.
func parseTSTagsJSON(tagsJSON string) []sdktransport.TSTag {
	if tagsJSON == "" || tagsJSON == "[]" || tagsJSON == "null" {
		return nil
	}
	var tags []sdktransport.TSTag
	if err := json.Unmarshal([]byte(tagsJSON), &tags); err != nil {
		// Non-array format (e.g., a JSON object): treat as unparseable, discard.
		return nil
	}
	return tags
}

// tsTagsToJSON serialises a []TSTag slice to the vtab canonical JSON format.
// Returns "[]" for an empty or nil slice to keep the column non-NULL.
func tsTagsToJSON(tags []sdktransport.TSTag) string {
	if len(tags) == 0 {
		return "[]"
	}
	b, err := json.Marshal(tags)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// testMetricsAdapter wraps the SDK TimeSeriesClient to satisfy vtab.MetricsStore.
type testMetricsAdapter struct {
	ts *sdk.Client
}

func (a *testMetricsAdapter) ListMetrics(ctx context.Context) ([]vtab.MetricInfo, error) {
	resp, err := a.ts.TimeSeries().TSListMetrics(ctx, &sdktransport.TSListMetricsRequest{Prefix: ""})
	if err != nil {
		return nil, err
	}
	infos := make([]vtab.MetricInfo, 0, len(resp.Metrics))
	for _, m := range resp.Metrics {
		infos = append(infos, vtab.MetricInfo{Name: m})
	}
	return infos, nil
}

func (a *testMetricsAdapter) GetMetric(ctx context.Context, name string) (*vtab.MetricInfo, error) {
	resp, err := a.ts.TimeSeries().TSListMetrics(ctx, &sdktransport.TSListMetricsRequest{Prefix: name})
	if err != nil {
		return nil, err
	}
	for _, m := range resp.Metrics {
		if m == name {
			return &vtab.MetricInfo{Name: m}, nil
		}
	}
	return nil, nil
}

// testNamespaceAdapter wraps the SDK VectorClient to satisfy vtab.NamespaceStore.
type testNamespaceAdapter struct {
	vec *sdk.Client
}

func (a *testNamespaceAdapter) ListNamespaces(ctx context.Context) ([]vtab.NamespaceInfo, error) {
	resp, err := a.vec.Vector().ListNamespaces(ctx, &sdktransport.ListNamespacesRequest{})
	if err != nil {
		return nil, err
	}
	infos := make([]vtab.NamespaceInfo, 0, len(resp.Namespaces))
	for _, ns := range resp.Namespaces {
		infos = append(infos, vtab.NamespaceInfo{
			Name:           ns.Namespace,
			Dimension:      ns.Dimension,
			DistanceMetric: ns.DistanceMetric,
			IndexType:      ns.IndexType,
		})
	}
	return infos, nil
}

func (a *testNamespaceAdapter) GetNamespace(ctx context.Context, name string) (*vtab.NamespaceInfo, error) {
	resp, err := a.vec.Vector().GetNamespaceInfo(ctx, &sdktransport.GetNamespaceInfoRequest{Namespace: name})
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	if resp.Namespace == "" {
		return nil, nil
	}
	return &vtab.NamespaceInfo{
		Name:           resp.Namespace,
		Dimension:      resp.Dimension,
		DistanceMetric: resp.DistanceMetric,
		IndexType:      resp.IndexType,
	}, nil
}

func (a *testNamespaceAdapter) CreateNamespace(ctx context.Context, name string, dimension uint32, distanceMetric, indexType string) error {
	_, err := a.vec.Vector().CreateNamespace(ctx, &sdktransport.CreateNamespaceRequest{
		Namespace:      name,
		Dimension:      dimension,
		DistanceMetric: distanceMetric,
		IndexType:      indexType,
	})
	return err
}

func (a *testNamespaceAdapter) DeleteNamespace(ctx context.Context, name string) error {
	_, err := a.vec.Vector().DeleteNamespace(ctx, &sdktransport.DeleteNamespaceRequest{Namespace: name})
	return err
}

// testVectorAdapter wraps the SDK VectorClient to satisfy vtab.VectorStore.
// The vtab layer stores vectors as raw little-endian float32 bytes and metadata
// as a JSON string. The SDK uses []float32 and map[string]interface{}.
type testVectorAdapter struct {
	vec *sdk.Client
}

func (a *testVectorAdapter) UpsertVector(ctx context.Context, namespace string, id uint64, vector []byte, metadata string) error {
	floats := rawBytesToFloat32s(vector)
	metaMap := jsonStringToMap(metadata)
	_, err := a.vec.Vector().UpsertVector(ctx, &sdktransport.UpsertVectorRequest{
		Namespace: namespace,
		ID:        id,
		Vector:    floats,
		Metadata:  metaMap,
	})
	return err
}

func (a *testVectorAdapter) GetVector(ctx context.Context, namespace string, id uint64) (*vtab.VectorRow, error) {
	resp, err := a.vec.Vector().GetVector(ctx, &sdktransport.GetVectorRequest{
		Namespace:     namespace,
		ID:            id,
		IncludeVector: true,
	})
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	if !resp.Found {
		return nil, nil
	}
	return &vtab.VectorRow{
		ID:        resp.ID,
		Namespace: namespace,
		Vector:    float32sToRawBytes(resp.Vector),
		Metadata:  mapToJSONString(resp.Metadata),
	}, nil
}

func (a *testVectorAdapter) ListVectors(ctx context.Context, namespace string) ([]vtab.VectorRow, error) {
	resp, err := a.vec.Vector().ListVectors(ctx, &sdktransport.ListVectorsRequest{
		Namespace:      namespace,
		IncludeVectors: true,
	})
	if err != nil {
		return nil, err
	}
	rows := make([]vtab.VectorRow, 0, len(resp.Vectors))
	for _, v := range resp.Vectors {
		rows = append(rows, vtab.VectorRow{
			ID:        v.ID,
			Namespace: namespace,
			Vector:    float32sToRawBytes(v.Vector),
			Metadata:  mapToJSONString(v.Metadata),
		})
	}
	return rows, nil
}

func (a *testVectorAdapter) DeleteVector(ctx context.Context, namespace string, id uint64) error {
	_, err := a.vec.Vector().DeleteVector(ctx, &sdktransport.DeleteVectorRequest{
		Namespace: namespace,
		ID:        id,
	})
	return err
}

// testCollectionAdapter wraps the SDK CollectionClient to satisfy vtab.CollectionStore.
type testCollectionAdapter struct {
	coll *sdk.Client
}

func (a *testCollectionAdapter) GetDocument(ctx context.Context, database, collection string, id uint64) (map[string]any, error) {
	resp, err := a.coll.Collections().GetDocument(ctx, database, collection, id)
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	if resp == nil {
		return nil, nil
	}
	return resp.Document, nil
}

func (a *testCollectionAdapter) ListDocuments(ctx context.Context, database, collection string) ([]vtab.DocumentRow, error) {
	resp, err := a.coll.Collections().ListDocuments(ctx, sdktransport.ListDocumentsRequest{
		Database:   database,
		Collection: collection,
		PageSize:   10000,
	})
	if err != nil {
		return nil, err
	}
	rows := make([]vtab.DocumentRow, 0, len(resp.Documents))
	for _, d := range resp.Documents {
		rows = append(rows, vtab.DocumentRow{ID: d.ID, Fields: d.Document})
	}
	return rows, nil
}

func (a *testCollectionAdapter) InsertDocument(ctx context.Context, database, collection string, doc map[string]any) (uint64, error) {
	resp, err := a.coll.Collections().InsertDocument(ctx, sdktransport.InsertDocumentRequest{
		Database:   database,
		Collection: collection,
		Document:   doc,
	})
	if err != nil {
		return 0, err
	}
	return resp.ID, nil
}

func (a *testCollectionAdapter) UpdateDocument(ctx context.Context, database, collection string, id uint64, doc map[string]any) error {
	return a.coll.Collections().UpdateDocument(ctx, sdktransport.UpdateDocumentRequest{
		Database:   database,
		Collection: collection,
		ID:         id,
		Document:   doc,
	})
}

func (a *testCollectionAdapter) DeleteDocument(ctx context.Context, database, collection string, id uint64) error {
	return a.coll.Collections().DeleteDocument(ctx, database, collection, id)
}

// ---------------------------------------------------------------------------
// Type conversion helpers for vector adapter
// ---------------------------------------------------------------------------

// rawBytesToFloat32s converts a little-endian []byte representation of float32
// values back to []float32 for the SDK.
func rawBytesToFloat32s(b []byte) []float32 {
	if len(b) == 0 {
		return nil
	}
	n := len(b) / 4
	floats := make([]float32, n)
	for i := range floats {
		bits := uint32(b[i*4]) | uint32(b[i*4+1])<<8 | uint32(b[i*4+2])<<16 | uint32(b[i*4+3])<<24
		floats[i] = math.Float32frombits(bits)
	}
	return floats
}

// float32sToRawBytes converts []float32 back to the little-endian []byte
// format used by vtab.VectorRow.Vector.
func float32sToRawBytes(floats []float32) []byte {
	if len(floats) == 0 {
		return nil
	}
	b := make([]byte, len(floats)*4)
	for i, f := range floats {
		bits := math.Float32bits(f)
		b[i*4] = byte(bits)
		b[i*4+1] = byte(bits >> 8)
		b[i*4+2] = byte(bits >> 16)
		b[i*4+3] = byte(bits >> 24)
	}
	return b
}

// jsonStringToMap parses a JSON metadata string into a map. Returns nil when
// the string is empty or malformed.
func jsonStringToMap(s string) map[string]interface{} {
	if s == "" {
		return nil
	}
	var m map[string]interface{}
	_ = json.Unmarshal([]byte(s), &m)
	return m
}

// mapToJSONString serialises a metadata map to a JSON string. Returns an
// empty string when m is nil.
func mapToJSONString(m map[string]interface{}) string {
	if len(m) == 0 {
		return ""
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// ---------------------------------------------------------------------------
// Float32 / byte encoding helpers used by vector tests
// ---------------------------------------------------------------------------

// float32sToBytes encodes a []float32 as a little-endian byte slice.
func float32sToBytes(floats []float32) []byte {
	if len(floats) == 0 {
		return nil
	}
	b := make([]byte, len(floats)*4)
	for i, f := range floats {
		binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(f))
	}
	return b
}

// bytesToFloat32s decodes a little-endian byte slice into []float32.
func bytesToFloat32s(t *testing.T, b []byte) []float32 {
	t.Helper()
	if len(b)%4 != 0 {
		t.Fatalf("bytesToFloat32s: byte length %d is not a multiple of 4", len(b))
	}
	out := make([]float32, len(b)/4)
	for i := range out {
		bits := binary.LittleEndian.Uint32(b[i*4:])
		out[i] = math.Float32frombits(bits)
	}
	return out
}

// ---------------------------------------------------------------------------
// Error classification helpers
// ---------------------------------------------------------------------------

// isKeyNotFound returns true when err indicates a missing key.
func isKeyNotFound(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "key not found") ||
		strings.Contains(msg, "KeyNotFound") ||
		strings.Contains(msg, "not found") ||
		strings.Contains(msg, "404")
}

// isNotFound returns true when err indicates a resource was not found.
func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "not found") ||
		strings.Contains(msg, "NotFound") ||
		strings.Contains(msg, "404")
}

// ---------------------------------------------------------------------------
// Unique key helpers
// ---------------------------------------------------------------------------

// uniqueKey generates a protocol-namespaced key to prevent collisions between
// protocol subtests that share a qrdbd instance.
func uniqueKey(proto protocol, prefix string) string {
	return fmt.Sprintf("%s/%s", proto, prefix)
}

// uniqueMetric generates a protocol-namespaced metric name.
func uniqueMetric(proto protocol, name string) string {
	return fmt.Sprintf("%s.%s", proto, name)
}

// uniqueNamespace generates a protocol-namespaced vector namespace name.
func uniqueNamespace(proto protocol, name string) string {
	return fmt.Sprintf("%s-%s", proto, name)
}

// protoDocID generates a protocol-unique document _id to avoid collisions
// between parallel protocol subtests that share the same collection. Each
// protocol gets a separate range: rest=10xx, grpc=20xx, unix=30xx, quic=40xx, mcp=50xx.
func protoDocID(proto protocol, base int64) int64 {
	switch proto {
	case protoREST:
		return 1000000 + base
	case protoGRPC:
		return 2000000 + base
	case protoUnix:
		return 3000000 + base
	case protoQUIC:
		return 4000000 + base
	case protoMCP:
		return 5000000 + base
	default:
		return base
	}
}

// ---------------------------------------------------------------------------
// SQL assertion and query helpers
// ---------------------------------------------------------------------------

// assertRowCount executes query and asserts that exactly expected rows are
// returned.
func assertRowCount(t *testing.T, db *sql.DB, query string, expected int, args ...any) {
	t.Helper()
	rows, err := db.Query(query, args...)
	require.NoError(t, err, "query: %s", query)
	defer rows.Close()
	n := 0
	for rows.Next() {
		n++
	}
	require.NoError(t, rows.Err(), "rows.Err: %s", query)
	require.Equal(t, expected, n, "row count mismatch for: %s", query)
}

// assertKVValue queries the kv virtual table for key and asserts that the
// value column equals expected.
func assertKVValue(t *testing.T, db *sql.DB, key, expected string) {
	t.Helper()
	var got string
	err := db.QueryRow("SELECT value FROM kv WHERE key = ?", key).Scan(&got)
	require.NoError(t, err, "SELECT kv key=%q", key)
	require.Equal(t, expected, got)
}

// insertKVBatch inserts count key-value pairs with keys "<prefix>/<i>" and
// values "<prefix>-value-<i>".
func insertKVBatch(t *testing.T, db *sql.DB, prefix string, count int) {
	t.Helper()
	for i := 0; i < count; i++ {
		key := fmt.Sprintf("%s/%d", prefix, i)
		val := fmt.Sprintf("%s-value-%d", prefix, i)
		_, err := db.Exec("INSERT INTO kv (key, value) VALUES (?, ?)", key, val)
		require.NoError(t, err, "INSERT kv key=%q", key)
	}
}

// scanAllRows executes query and returns every result row as a map from column
// name to raw value.
func scanAllRows(t *testing.T, db *sql.DB, query string, args ...any) []map[string]any {
	t.Helper()
	rows, err := db.Query(query, args...)
	require.NoError(t, err, "query: %s", query)
	defer rows.Close()

	cols, err := rows.Columns()
	require.NoError(t, err)

	var result []map[string]any
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		require.NoError(t, rows.Scan(ptrs...))
		row := make(map[string]any, len(cols))
		for i, col := range cols {
			row[col] = vals[i]
		}
		result = append(result, row)
	}
	require.NoError(t, rows.Err())
	return result
}
