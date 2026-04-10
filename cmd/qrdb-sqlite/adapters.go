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

package main

// adapters.go bridges the vtab package interfaces to the SDK client.
//
// Each adapter wraps a *sdk.Client (or the individual service clients it
// vends) and a pragmaReader so that every call uses the consistency level and
// write mode configured via PRAGMA statements for the current session.
//
// The adapters live in the main package so they can import both
// github.com/jeremyhahn/go-qrdb/sdk/go and the vtab sub-package without
// creating a circular dependency. The vtab package only depends on its own
// types and the standard library.

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"strconv"

	"github.com/jeremyhahn/go-qrdb/cmd/qrdb-sqlite/vtab"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// pragmaReader — narrow interface for session options
// ---------------------------------------------------------------------------

// pragmaReader is a narrow interface that exposes only the two session-level
// pragma values the adapters need. The vtab.pragmaStore satisfies this via the
// exported Consistency() and WriteMode() methods added in pragma.go.
type pragmaReader interface {
	Consistency() string
	WriteMode() string
}

// readOpts builds transport.ReadOption slice from the current session pragma.
func readOpts(p pragmaReader) []transport.ReadOption {
	level, err := transport.ParseConsistencyLevel(p.Consistency())
	if err != nil {
		level = transport.ConsistencyLinearizable
	}
	return []transport.ReadOption{transport.WithConsistency(level)}
}

// writeOpts builds transport.WriteOption slice from the current session pragma.
func writeOpts(p pragmaReader) []transport.WriteOption {
	mode, err := transport.ParseWriteMode(p.WriteMode())
	if err != nil {
		mode = transport.WriteModeSync
	}
	return []transport.WriteOption{transport.WithWriteMode(mode)}
}

// ---------------------------------------------------------------------------
// kvAdapter — satisfies vtab.kvStore
// ---------------------------------------------------------------------------

// kvAdapter wraps the SDK KVClient to satisfy the vtab.kvStore interface.
// Read/write options are resolved from the pragma store on every call so that
// PRAGMA changes take effect for subsequent SQL statements within the same
// connection.
type kvAdapter struct {
	kv     transport.KVClient
	pragma pragmaReader
}

// newKVAdapter constructs a kvAdapter from a connected SDK client and pragma
// store.
func newKVAdapter(c *sdk.Client, p pragmaReader) *kvAdapter {
	return &kvAdapter{kv: c.KV(), pragma: p}
}

func (a *kvAdapter) Get(ctx context.Context, key string) ([]byte, error) {
	return a.kv.Get(ctx, key, readOpts(a.pragma)...)
}

func (a *kvAdapter) Scan(ctx context.Context, prefix string) (map[string][]byte, error) {
	return a.kv.Scan(ctx, prefix, readOpts(a.pragma)...)
}

func (a *kvAdapter) Put(ctx context.Context, key string, value []byte) error {
	return a.kv.Put(ctx, key, value, writeOpts(a.pragma)...)
}

func (a *kvAdapter) Delete(ctx context.Context, key string) error {
	return a.kv.Delete(ctx, key, writeOpts(a.pragma)...)
}

// ---------------------------------------------------------------------------
// entityAdapter — satisfies vtab.entityStore
// ---------------------------------------------------------------------------

// entityKeyPrefix returns the KV key prefix for all entities of a given type.
// Entity keys follow the convention "<entityType>/<id>" where id is formatted
// as a zero-padded 20-digit decimal string to preserve lexicographic order.
func entityKeyPrefix(entityType string) string {
	return entityType + "/"
}

// entityKey returns the full KV key for a single entity.
func entityKey(entityType string, id uint64) string {
	return entityType + "/" + strconv.FormatUint(id, 10)
}

// entityIDFromKey extracts the uint64 ID from an entity KV key.
// Returns 0 on parse failure.
func entityIDFromKey(entityType, key string) uint64 {
	prefix := entityKeyPrefix(entityType)
	if len(key) <= len(prefix) {
		return 0
	}
	id, err := strconv.ParseUint(key[len(prefix):], 10, 64)
	if err != nil {
		return 0
	}
	return id
}

// entityAdapter wraps the SDK KV and Index clients to implement vtab.EntityStore.
// Entities are stored as JSON blobs under "<entityType>/<id>" keys.
type entityAdapter struct {
	kv     transport.KVClient
	index  transport.IndexClient
	pragma pragmaReader
}

// newEntityAdapter constructs an entityAdapter from a connected SDK client and
// pragma store.
func newEntityAdapter(c *sdk.Client, p pragmaReader) *entityAdapter {
	return &entityAdapter{kv: c.KV(), index: c.Index(), pragma: p}
}

func (a *entityAdapter) GetEntity(ctx context.Context, entityType string, id uint64) (map[string]any, error) {
	key := entityKey(entityType, id)
	data, err := a.kv.Get(ctx, key, readOpts(a.pragma)...)
	if err != nil {
		return nil, err
	}
	if data == nil {
		return nil, nil
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, &AdapterDecodeError{Key: key, Err: err}
	}
	return fields, nil
}

func (a *entityAdapter) ScanEntities(ctx context.Context, entityType string) ([]vtab.EntityRow, error) {
	pairs, err := a.kv.Scan(ctx, entityKeyPrefix(entityType), readOpts(a.pragma)...)
	if err != nil {
		return nil, err
	}
	rows := make([]vtab.EntityRow, 0, len(pairs))
	for k, v := range pairs {
		id := entityIDFromKey(entityType, k)
		if id == 0 {
			continue
		}
		var fields map[string]any
		if err := json.Unmarshal(v, &fields); err != nil {
			continue
		}
		rows = append(rows, vtab.EntityRow{ID: id, Fields: fields})
	}
	return rows, nil
}

func (a *entityAdapter) PutEntity(ctx context.Context, entityType string, id uint64, data map[string]any) error {
	b, err := json.Marshal(data)
	if err != nil {
		return &AdapterEncodeError{Key: entityKey(entityType, id), Err: err}
	}
	return a.kv.Put(ctx, entityKey(entityType, id), b, writeOpts(a.pragma)...)
}

func (a *entityAdapter) DeleteEntity(ctx context.Context, entityType string, id uint64) error {
	return a.kv.Delete(ctx, entityKey(entityType, id), writeOpts(a.pragma)...)
}

func (a *entityAdapter) QueryByIndex(ctx context.Context, entityType, fieldName, value string) ([]vtab.EntityRow, error) {
	indexName := entityType + "." + fieldName
	resp, err := a.index.IndexQuery(ctx, indexName, value)
	if err != nil {
		return nil, err
	}
	return a.fetchEntityKeys(ctx, entityType, resp.Keys)
}

func (a *entityAdapter) ScanByIndex(ctx context.Context, entityType, fieldName, start, end string) ([]vtab.EntityRow, error) {
	indexName := entityType + "." + fieldName
	resp, err := a.index.IndexScan(ctx, indexName, start, end)
	if err != nil {
		return nil, err
	}
	var keys []string
	for _, entry := range resp.Entries {
		keys = append(keys, entry.Keys...)
	}
	return a.fetchEntityKeys(ctx, entityType, keys)
}

// fetchEntityKeys retrieves entities for the given KV keys. Keys that do not
// exist or cannot be decoded are silently skipped.
func (a *entityAdapter) fetchEntityKeys(ctx context.Context, entityType string, keys []string) ([]vtab.EntityRow, error) {
	rows := make([]vtab.EntityRow, 0, len(keys))
	for _, k := range keys {
		data, err := a.kv.Get(ctx, k, readOpts(a.pragma)...)
		if err != nil || data == nil {
			continue
		}
		id := entityIDFromKey(entityType, k)
		if id == 0 {
			continue
		}
		var fields map[string]any
		if err := json.Unmarshal(data, &fields); err != nil {
			continue
		}
		rows = append(rows, vtab.EntityRow{ID: id, Fields: fields})
	}
	return rows, nil
}

// ---------------------------------------------------------------------------
// collectionAdapter — satisfies vtab.collectionStore
// ---------------------------------------------------------------------------

// collectionAdapter wraps the SDK CollectionClient to implement
// vtab.collectionStore.
type collectionAdapter struct {
	col transport.CollectionClient
}

// newCollectionAdapter constructs a collectionAdapter from a connected SDK
// client.
func newCollectionAdapter(c *sdk.Client) *collectionAdapter {
	return &collectionAdapter{col: c.Collections()}
}

func (a *collectionAdapter) GetDocument(ctx context.Context, database, collection string, id uint64) (map[string]any, error) {
	resp, err := a.col.GetDocument(ctx, database, collection, id)
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, nil
	}
	return resp.Document, nil
}

func (a *collectionAdapter) ListDocuments(ctx context.Context, database, collection string) ([]vtab.DocumentRow, error) {
	resp, err := a.col.ListDocuments(ctx, transport.ListDocumentsRequest{
		Database:   database,
		Collection: collection,
	})
	if err != nil {
		return nil, err
	}
	rows := make([]vtab.DocumentRow, len(resp.Documents))
	for i, d := range resp.Documents {
		rows[i] = vtab.DocumentRow{ID: d.ID, Fields: d.Document}
	}
	return rows, nil
}

func (a *collectionAdapter) InsertDocument(ctx context.Context, database, collection string, doc map[string]any) (uint64, error) {
	resp, err := a.col.InsertDocument(ctx, transport.InsertDocumentRequest{
		Database:   database,
		Collection: collection,
		Document:   doc,
	})
	if err != nil {
		return 0, err
	}
	return resp.ID, nil
}

func (a *collectionAdapter) UpdateDocument(ctx context.Context, database, collection string, id uint64, doc map[string]any) error {
	return a.col.UpdateDocument(ctx, transport.UpdateDocumentRequest{
		Database:   database,
		Collection: collection,
		ID:         id,
		Document:   doc,
	})
}

func (a *collectionAdapter) DeleteDocument(ctx context.Context, database, collection string, id uint64) error {
	return a.col.DeleteDocument(ctx, database, collection, id)
}

// ---------------------------------------------------------------------------
// datapointAdapter — satisfies vtab.datapointStore
// ---------------------------------------------------------------------------

// datapointAdapter wraps the SDK TimeSeriesClient to implement
// vtab.datapointStore.
type datapointAdapter struct {
	ts transport.TimeSeriesClient
}

// newDatapointAdapter constructs a datapointAdapter from a connected SDK
// client.
func newDatapointAdapter(c *sdk.Client) *datapointAdapter {
	return &datapointAdapter{ts: c.TimeSeries()}
}

func (a *datapointAdapter) WriteDatapoint(ctx context.Context, metric string, timestampNs int64, value float64, tags string) error {
	point := &transport.TSDataPoint{
		Metric:      metric,
		Value:       value,
		TimestampNs: timestampNs,
		Tags:        parseTSTags(tags),
	}
	return a.ts.TSWrite(ctx, point)
}

func (a *datapointAdapter) QueryDatapoints(ctx context.Context, metric string, startNs, endNs int64) ([]vtab.DatapointRow, error) {
	resp, err := a.ts.TSQuery(ctx, &transport.TSQueryRequest{
		Metric:  metric,
		StartNs: startNs,
		EndNs:   endNs,
	})
	if err != nil {
		return nil, err
	}
	rows := make([]vtab.DatapointRow, len(resp.Points))
	for i, p := range resp.Points {
		rows[i] = vtab.DatapointRow{
			Metric:      p.Metric,
			TimestampNs: p.TimestampNs,
			Value:       p.Value,
			Tags:        marshalTSTags(p.Tags),
		}
	}
	return rows, nil
}

func (a *datapointAdapter) ScanAllDatapoints(ctx context.Context) ([]vtab.DatapointRow, error) {
	// List all known metrics then query each over the full time range.
	metricsResp, err := a.ts.TSListMetrics(ctx, &transport.TSListMetricsRequest{})
	if err != nil {
		return nil, err
	}
	const maxInt64 = int64(math.MaxInt64)
	var rows []vtab.DatapointRow
	for _, name := range metricsResp.Metrics {
		pts, err := a.QueryDatapoints(ctx, name, 0, maxInt64)
		if err != nil {
			return nil, err
		}
		rows = append(rows, pts...)
	}
	return rows, nil
}

// parseTSTags deserialises a JSON tags string back into a []transport.TSTag.
// An empty or invalid string returns nil.
func parseTSTags(tags string) []transport.TSTag {
	if tags == "" {
		return nil
	}
	var out []transport.TSTag
	if err := json.Unmarshal([]byte(tags), &out); err != nil {
		return nil
	}
	return out
}

// marshalTSTags serialises a []transport.TSTag to a compact JSON string.
// Returns an empty string when tags is nil or empty.
func marshalTSTags(tags []transport.TSTag) string {
	if len(tags) == 0 {
		return ""
	}
	b, err := json.Marshal(tags)
	if err != nil {
		return ""
	}
	return string(b)
}

// ---------------------------------------------------------------------------
// metricsAdapter — satisfies vtab.metricsStore
// ---------------------------------------------------------------------------

// metricsAdapter wraps the SDK TimeSeriesClient to implement
// vtab.metricsStore.
type metricsAdapter struct {
	ts transport.TimeSeriesClient
}

// newMetricsAdapter constructs a metricsAdapter from a connected SDK client.
func newMetricsAdapter(c *sdk.Client) *metricsAdapter {
	return &metricsAdapter{ts: c.TimeSeries()}
}

func (a *metricsAdapter) ListMetrics(ctx context.Context) ([]vtab.MetricInfo, error) {
	resp, err := a.ts.TSListMetrics(ctx, &transport.TSListMetricsRequest{})
	if err != nil {
		return nil, err
	}
	infos := make([]vtab.MetricInfo, len(resp.Metrics))
	for i, name := range resp.Metrics {
		infos[i] = vtab.MetricInfo{Name: name}
	}
	return infos, nil
}

func (a *metricsAdapter) GetMetric(ctx context.Context, name string) (*vtab.MetricInfo, error) {
	resp, err := a.ts.TSListMetrics(ctx, &transport.TSListMetricsRequest{Prefix: name})
	if err != nil {
		return nil, err
	}
	for _, n := range resp.Metrics {
		if n == name {
			return &vtab.MetricInfo{Name: name}, nil
		}
	}
	return nil, nil
}

// ---------------------------------------------------------------------------
// namespaceAdapter — satisfies vtab.namespaceStore
// ---------------------------------------------------------------------------

// namespaceAdapter wraps the SDK VectorClient to implement
// vtab.namespaceStore.
type namespaceAdapter struct {
	vec transport.VectorClient
}

// newNamespaceAdapter constructs a namespaceAdapter from a connected SDK
// client.
func newNamespaceAdapter(c *sdk.Client) *namespaceAdapter {
	return &namespaceAdapter{vec: c.Vector()}
}

func (a *namespaceAdapter) ListNamespaces(ctx context.Context) ([]vtab.NamespaceInfo, error) {
	resp, err := a.vec.ListNamespaces(ctx, &transport.ListNamespacesRequest{})
	if err != nil {
		return nil, err
	}
	infos := make([]vtab.NamespaceInfo, len(resp.Namespaces))
	for i, ns := range resp.Namespaces {
		infos[i] = vtab.NamespaceInfo{
			Name:           ns.Namespace,
			Dimension:      ns.Dimension,
			DistanceMetric: ns.DistanceMetric,
			IndexType:      ns.IndexType,
			VectorCount:    ns.VectorCount,
		}
	}
	return infos, nil
}

func (a *namespaceAdapter) GetNamespace(ctx context.Context, name string) (*vtab.NamespaceInfo, error) {
	resp, err := a.vec.GetNamespaceInfo(ctx, &transport.GetNamespaceInfoRequest{Namespace: name})
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, nil
	}
	return &vtab.NamespaceInfo{
		Name:           resp.Namespace,
		Dimension:      resp.Dimension,
		DistanceMetric: resp.DistanceMetric,
		IndexType:      resp.IndexType,
		VectorCount:    resp.VectorCount,
	}, nil
}

func (a *namespaceAdapter) CreateNamespace(ctx context.Context, name string, dimension uint32, distanceMetric, indexType string) error {
	_, err := a.vec.CreateNamespace(ctx, &transport.CreateNamespaceRequest{
		Namespace:      name,
		Dimension:      dimension,
		DistanceMetric: distanceMetric,
		IndexType:      indexType,
	})
	return err
}

func (a *namespaceAdapter) DeleteNamespace(ctx context.Context, name string) error {
	_, err := a.vec.DeleteNamespace(ctx, &transport.DeleteNamespaceRequest{Namespace: name})
	return err
}

// ---------------------------------------------------------------------------
// vectorAdapter — satisfies vtab.vectorStore
// ---------------------------------------------------------------------------

// vectorAdapter wraps the SDK VectorClient to implement vtab.vectorStore.
// Vectors are stored and retrieved as raw little-endian float32 byte slices.
type vectorAdapter struct {
	vec transport.VectorClient
}

// newVectorAdapter constructs a vectorAdapter from a connected SDK client.
func newVectorAdapter(c *sdk.Client) *vectorAdapter {
	return &vectorAdapter{vec: c.Vector()}
}

func (a *vectorAdapter) UpsertVector(ctx context.Context, namespace string, id uint64, vector []byte, metadata string) error {
	floats, err := bytesToFloat32s(vector)
	if err != nil {
		return err
	}
	var meta map[string]any
	if metadata != "" {
		if err := json.Unmarshal([]byte(metadata), &meta); err != nil {
			// Treat non-JSON metadata as a plain string value.
			meta = map[string]any{"_raw": metadata}
		}
	}
	_, err = a.vec.UpsertVector(ctx, &transport.UpsertVectorRequest{
		Namespace: namespace,
		ID:        id,
		Vector:    floats,
		Metadata:  meta,
	})
	return err
}

func (a *vectorAdapter) GetVector(ctx context.Context, namespace string, id uint64) (*vtab.VectorRow, error) {
	resp, err := a.vec.GetVector(ctx, &transport.GetVectorRequest{
		Namespace:       namespace,
		ID:              id,
		IncludeVector:   true,
		IncludeMetadata: true,
	})
	if err != nil {
		return nil, err
	}
	if resp == nil || !resp.Found {
		return nil, nil
	}
	return &vtab.VectorRow{
		ID:        resp.ID,
		Namespace: namespace,
		Vector:    float32sToBytes(resp.Vector),
		Metadata:  marshalMetadata(resp.Metadata),
	}, nil
}

func (a *vectorAdapter) ListVectors(ctx context.Context, namespace string) ([]vtab.VectorRow, error) {
	resp, err := a.vec.ListVectors(ctx, &transport.ListVectorsRequest{
		Namespace:       namespace,
		IncludeVectors:  true,
		IncludeMetadata: true,
	})
	if err != nil {
		return nil, err
	}
	rows := make([]vtab.VectorRow, len(resp.Vectors))
	for i, v := range resp.Vectors {
		rows[i] = vtab.VectorRow{
			ID:        v.ID,
			Namespace: namespace,
			Vector:    float32sToBytes(v.Vector),
			Metadata:  marshalMetadata(v.Metadata),
		}
	}
	return rows, nil
}

func (a *vectorAdapter) DeleteVector(ctx context.Context, namespace string, id uint64) error {
	_, err := a.vec.DeleteVector(ctx, &transport.DeleteVectorRequest{
		Namespace: namespace,
		ID:        id,
	})
	return err
}

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

// bytesToFloat32s decodes a little-endian byte slice into a []float32.
func bytesToFloat32s(b []byte) ([]float32, error) {
	if len(b)%4 != 0 {
		return nil, &AdapterDecodeError{Key: "vector", Err: fmt.Errorf("byte length %d is not a multiple of 4", len(b))}
	}
	floats := make([]float32, len(b)/4)
	for i := range floats {
		bits := binary.LittleEndian.Uint32(b[i*4:])
		floats[i] = math.Float32frombits(bits)
	}
	return floats, nil
}

// marshalMetadata serialises a metadata map to a JSON string. Returns an empty
// string when meta is nil or marshalling fails.
func marshalMetadata(meta map[string]any) string {
	if len(meta) == 0 {
		return ""
	}
	b, err := json.Marshal(meta)
	if err != nil {
		return ""
	}
	return string(b)
}

// ---------------------------------------------------------------------------
// batchAdapter — satisfies vtab.batchExecutor
// ---------------------------------------------------------------------------

// batchAdapter wraps the SDK BatchClient to implement vtab.batchExecutor.
type batchAdapter struct {
	batch  transport.BatchClient
	pragma pragmaReader
}

// newBatchAdapter constructs a batchAdapter from a connected SDK client and
// pragma store.
func newBatchAdapter(c *sdk.Client, p pragmaReader) *batchAdapter {
	return &batchAdapter{batch: c.Batch(), pragma: p}
}

func (a *batchAdapter) ExecuteBatch(ctx context.Context, ops []vtab.BufferedOp) error {
	sdkOps := make([]transport.Op, len(ops))
	for i, op := range ops {
		switch op.Op {
		case vtab.OpPut:
			sdkOps[i] = transport.Op{
				Type:  transport.OpPut,
				Key:   []byte(op.Key),
				Value: op.Value,
			}
		case vtab.OpDelete:
			sdkOps[i] = transport.Op{
				Type: transport.OpDelete,
				Key:  []byte(op.Key),
			}
		}
	}
	_, err := a.batch.ExecuteBatch(ctx, sdkOps, writeOpts(a.pragma)...)
	return err
}

// ---------------------------------------------------------------------------
// discoveryAdapter — satisfies vtab.metadataProvider
// ---------------------------------------------------------------------------

// discoveryAdapter aggregates metadata from multiple SDK service clients to
// implement vtab.metadataProvider. It populates tableMeta entries for KV,
// collection, time-series, and vector tables by querying the live server.
type discoveryAdapter struct {
	admin    transport.AdminClient
	col      transport.CollectionClient
	ts       transport.TimeSeriesClient
	vec      transport.VectorClient
	database string
}

// newDiscoveryAdapter constructs a discoveryAdapter from a connected SDK
// client and the database name configured for this connection.
func newDiscoveryAdapter(c *sdk.Client, database string) *discoveryAdapter {
	return &discoveryAdapter{
		admin:    c.Admin(),
		col:      c.Collections(),
		ts:       c.TimeSeries(),
		vec:      c.Vector(),
		database: database,
	}
}

func (a *discoveryAdapter) DiscoverTables(ctx context.Context) ([]vtab.TableMeta, error) {
	var tables []vtab.TableMeta

	// Always expose the flat KV table when the server reports KV capability.
	caps, err := a.admin.Capabilities(ctx)
	if err == nil && caps.KV {
		tables = append(tables, vtab.TableMeta{
			Name:     "kv",
			Type:     vtab.TableKV,
			Database: a.database,
			Columns: []vtab.ColumnMeta{
				{Name: "key", SQLiteType: "TEXT", PrimaryKey: true},
				{Name: "value", SQLiteType: "BLOB", Nullable: true},
			},
		})
	}

	// Discover collections.
	if a.database != "" {
		colResp, err := a.col.ListCollections(ctx, a.database)
		if err == nil {
			for _, info := range colResp.Collections {
				cols := []vtab.ColumnMeta{{Name: "_id", SQLiteType: "INTEGER", PrimaryKey: true}}
				for field, sqlType := range info.Schema {
					cols = append(cols, vtab.ColumnMeta{
						Name:       field,
						SQLiteType: sqlType,
						Nullable:   true,
					})
				}
				tables = append(tables, vtab.TableMeta{
					Name:     info.Name,
					Type:     vtab.TableCollection,
					Database: a.database,
					Columns:  cols,
				})
			}
		}
	}

	// Discover time-series metrics.
	if caps == nil || caps.TimeSeries {
		metricsResp, err := a.ts.TSListMetrics(ctx, &transport.TSListMetricsRequest{})
		if err == nil && len(metricsResp.Metrics) > 0 {
			tables = append(tables, vtab.TableMeta{
				Name:     "ts_datapoints",
				Type:     vtab.TableTimeSeries,
				Database: a.database,
				Columns: []vtab.ColumnMeta{
					{Name: "metric", SQLiteType: "TEXT"},
					{Name: "timestamp_ns", SQLiteType: "INTEGER"},
					{Name: "value", SQLiteType: "REAL"},
					{Name: "tags", SQLiteType: "TEXT", Nullable: true},
				},
			})
		}
	}

	// Discover vector namespaces.
	if caps == nil || caps.Vector {
		nsResp, err := a.vec.ListNamespaces(ctx, &transport.ListNamespacesRequest{})
		if err == nil && len(nsResp.Namespaces) > 0 {
			tables = append(tables, vtab.TableMeta{
				Name:     "vectors",
				Type:     vtab.TableVector,
				Database: a.database,
				Columns: []vtab.ColumnMeta{
					{Name: "id", SQLiteType: "INTEGER", PrimaryKey: true},
					{Name: "namespace", SQLiteType: "TEXT"},
					{Name: "vector", SQLiteType: "BLOB", Nullable: true},
					{Name: "metadata", SQLiteType: "TEXT", Nullable: true},
				},
			})
		}
	}

	return tables, nil
}

// ---------------------------------------------------------------------------
// Adapter error types
// ---------------------------------------------------------------------------

// AdapterDecodeError is returned when a value retrieved from QRDB cannot be
// decoded into the expected Go type. Key identifies the KV key or field that
// caused the failure; Err is the underlying decode error.
type AdapterDecodeError struct {
	Key string
	Err error
}

// Error returns a human-readable description of the decode failure.
func (e *AdapterDecodeError) Error() string {
	return "adapter: decode failed for " + e.Key + ": " + e.Err.Error()
}

// Unwrap returns the underlying decode error.
func (e *AdapterDecodeError) Unwrap() error { return e.Err }

// AdapterEncodeError is returned when a value cannot be encoded for storage in
// QRDB. Key identifies the KV key or field; Err is the underlying encode error.
type AdapterEncodeError struct {
	Key string
	Err error
}

// Error returns a human-readable description of the encode failure.
func (e *AdapterEncodeError) Error() string {
	return "adapter: encode failed for " + e.Key + ": " + e.Err.Error()
}

// Unwrap returns the underlying encode error.
func (e *AdapterEncodeError) Unwrap() error { return e.Err }
