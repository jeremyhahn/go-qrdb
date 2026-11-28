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

package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/feature"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/vector"
	vectorindex "github.com/jeremyhahn/go-qrdb/pkg/vector"
)

// Reserved IndexParams keys. The engine's namespace record has no name,
// tenant, string spellings, metadata or update time, so the host storage
// keeps them in the record's IndexParams under the "qrdb." prefix; every
// other parameter is the namespace's IndexConfig.
const (
	hostParamPrefix    = "qrdb."
	hostParamName      = hostParamPrefix + "name"
	hostParamTenant    = hostParamPrefix + "tenant"
	hostParamOwner     = hostParamPrefix + "owner"
	hostParamMetric    = hostParamPrefix + "metric"
	hostParamIndexType = hostParamPrefix + "index_type"
	hostParamUpdatedAt = hostParamPrefix + "updated_at"
	hostParamMetaPfx   = hostParamPrefix + "meta."
	hostParamLabelPfx  = hostParamPrefix + "label."
)

// hostVectorStorage is a vector.Storage whose every write is an OpVector*
// command proposed through host, so it lands in the host's own apply batch
// (the database's own store, under __vec: keys), and whose every read is an
// engine lookup (VectorNamespaceQuery, VectorGetQuery, ...) through the
// same host. It keeps no state of its own.
type hostVectorStorage struct {
	host    Host
	shardID uint64
}

var _ vector.Storage = (*hostVectorStorage)(nil)

// NewHostVectorStorage returns a vector.Storage that writes and reads the
// vectors of shardID through host.
func NewHostVectorStorage(host Host, shardID uint64) vector.Storage {
	return &hostVectorStorage{host: host, shardID: shardID}
}

// HostVectorNamespace is the engine namespace name the host storage stores
// tenantID's logical namespace under.
func HostVectorNamespace(tenantID, logical string) string {
	return hostNamespace(scopedNamespace(tenantID, logical))
}

// hostNamespace maps a service-scoped namespace (any non-control bytes, of
// any length) onto an engine namespace name, which must be a DNS label:
// "s" followed by the first 62 hex digits of the scoped name's SHA-256.
func hostNamespace(scoped string) string {
	sum := sha256.Sum256([]byte(scoped))
	return "s" + hex.EncodeToString(sum[:])[:62]
}

// VectorStorageConflictError is returned by NewVectorService when it is
// given both a storage and an apply host.
type VectorStorageConflictError struct{}

func (e *VectorStorageConflictError) Error() string {
	return "vector service: storage and apply host are mutually exclusive"
}

// HostVectorIDAllocationError is returned by the host storage's NextID and
// BatchAllocateIDs. The vector service never calls them: the engine
// allocates IDs in apply.
type HostVectorIDAllocationError struct {
	Namespace string
}

func (e *HostVectorIDAllocationError) Error() string {
	return "vector host storage: namespace " + strconv.Quote(e.Namespace) +
		": IDs are allocated by the engine in apply, not by the storage"
}

// HostVectorAnswerError reports a SyncRead answer of an unexpected type.
type HostVectorAnswerError struct {
	Query string
	Got   string
}

func (e *HostVectorAnswerError) Error() string {
	return "vector host storage: " + e.Query + " answered " + e.Got
}

func (s *hostVectorStorage) propose(ctx context.Context, cmd []byte, encErr error) error {
	if encErr != nil {
		return &EncodeRequestError{Err: encErr}
	}
	// Every OpVector* command needs every replica of the group to apply the
	// vector command set: refuse before proposing until the group has
	// activated featuregate.VectorEngine.
	if gated, ok := s.host.(FeatureGatedHost); ok && !gated.FeatureEnabled(s.shardID, featuregate.VectorEngine) {
		return &feature.NotActiveError{Feature: featuregate.VectorEngine, ShardID: s.shardID}
	}
	res, err := propose(ctx, s.host, s.shardID, cmd)
	if err != nil {
		return translateProposeError(err)
	}
	return checkResultError(res)
}

// hostIndexBytes resolves a namespace's metric and index type spellings to
// the engine's bytes.
func hostIndexBytes(metric, indexType string) (byte, byte, error) {
	m, err := parseDistanceMetric(reportedDistanceMetric(metric))
	if err != nil {
		return 0, 0, err
	}
	mb, ok := liveIndexMetrics[m]
	if !ok {
		return 0, 0, &UnknownDistanceMetricError{Metric: metric}
	}
	canonical, err := parseIndexType(indexType)
	if err != nil {
		return 0, 0, err
	}
	if canonical == "HNSW" {
		return byte(mb), byte(vectorindex.IndexHNSW), nil
	}
	tb, ok := liveIndexTypes[canonical]
	if !ok {
		return 0, 0, &UnknownIndexTypeError{IndexType: indexType}
	}
	return byte(mb), byte(tb), nil
}

// hostParams builds a record's IndexParams from config and the reserved
// keys.
func hostParams(config map[string]string, reserved map[string]string, meta, labels map[string]string) (map[string]string, error) {
	params := make(map[string]string, len(config)+len(reserved)+len(meta)+len(labels))
	for k, v := range config {
		if strings.HasPrefix(k, hostParamPrefix) {
			return nil, fmt.Errorf("%w: index config key %q uses the reserved prefix %q", vector.ErrInvalidNamespace, k, hostParamPrefix)
		}
		params[k] = v
	}
	for k, v := range reserved {
		if v != "" {
			params[k] = v
		}
	}
	for k, v := range meta {
		params[hostParamMetaPfx+k] = v
	}
	for k, v := range labels {
		params[hostParamLabelPfx+k] = v
	}
	return params, nil
}

// infoFromAnswer converts an engine namespace answer back into the
// service's NamespaceInfo.
func infoFromAnswer(a VectorNamespaceAnswer) *vector.NamespaceInfo {
	p := a.Record.IndexParams
	info := &vector.NamespaceInfo{
		Name:           p[hostParamName],
		TenantID:       p[hostParamTenant],
		OwnerUserID:    p[hostParamOwner],
		DistanceMetric: p[hostParamMetric],
		IndexType:      p[hostParamIndexType],
		Dimension:      a.Record.Dimension,
		VectorCount:    a.Count,
		CreatedAt:      a.Record.CreatedAtUnixNano,
		UpdatedAt:      a.Record.CreatedAtUnixNano,
	}
	if v, err := strconv.ParseInt(p[hostParamUpdatedAt], 10, 64); err == nil {
		info.UpdatedAt = v
	}
	for k, v := range p {
		switch {
		case strings.HasPrefix(k, hostParamMetaPfx):
			if info.Metadata == nil {
				info.Metadata = map[string]string{}
			}
			info.Metadata[k[len(hostParamMetaPfx):]] = v
		case strings.HasPrefix(k, hostParamLabelPfx):
			if info.Labels == nil {
				info.Labels = map[string]string{}
			}
			info.Labels[k[len(hostParamLabelPfx):]] = v
		case strings.HasPrefix(k, hostParamPrefix):
		default:
			if info.IndexConfig == nil {
				info.IndexConfig = map[string]string{}
			}
			info.IndexConfig[k] = v
		}
	}
	return info
}

func (s *hostVectorStorage) namespaceAnswer(ctx context.Context, namespace string) (*VectorNamespaceAnswer, error) {
	got, err := s.host.SyncRead(ctx, s.shardID, VectorNamespaceQuery{Namespace: hostNamespace(namespace)})
	if err != nil {
		return nil, err
	}
	if got == nil {
		return nil, fmt.Errorf("namespace %q: %w", namespace, vector.ErrNamespaceNotFound)
	}
	a, ok := got.(*VectorNamespaceAnswer)
	if !ok || a == nil {
		return nil, &HostVectorAnswerError{Query: "VectorNamespaceQuery", Got: fmt.Sprintf("%T", got)}
	}
	return a, nil
}

// CreateNamespace proposes OpVectorNamespaceCreate.
func (s *hostVectorStorage) CreateNamespace(ctx context.Context, info *vector.NamespaceInfo) error {
	if info == nil {
		return vector.ErrInvalidNamespace
	}
	metric, indexType, err := hostIndexBytes(info.DistanceMetric, info.IndexType)
	if err != nil {
		return err
	}
	updatedAt := ""
	if info.UpdatedAt != 0 {
		updatedAt = strconv.FormatInt(info.UpdatedAt, 10)
	}
	params, err := hostParams(info.IndexConfig, map[string]string{
		hostParamName:      info.Name,
		hostParamTenant:    info.TenantID,
		hostParamOwner:     info.OwnerUserID,
		hostParamMetric:    info.DistanceMetric,
		hostParamIndexType: info.IndexType,
		hostParamUpdatedAt: updatedAt,
	}, info.Metadata, info.Labels)
	if err != nil {
		return err
	}
	cmd, encErr := protocol.EncodeVectorNamespaceCreate(protocol.VectorNamespaceCreateParams{
		Name:              hostNamespace(info.Name),
		Dimension:         info.Dimension,
		DistanceMetric:    metric,
		IndexType:         indexType,
		CreatedAtUnixNano: info.CreatedAt,
		IndexParams:       params,
	})
	return s.propose(ctx, cmd, encErr)
}

// UpdateNamespace proposes OpVectorIndexReconfigure with the record's own
// index type and the rebuilt parameters.
func (s *hostVectorStorage) UpdateNamespace(ctx context.Context, namespace string, update *vector.NamespaceUpdate) error {
	if update == nil {
		return vector.ErrInvalidNamespace
	}
	a, err := s.namespaceAnswer(ctx, namespace)
	if err != nil {
		return err
	}
	old := infoFromAnswer(*a)
	params, err := hostParams(update.IndexConfig, map[string]string{
		hostParamName:      old.Name,
		hostParamTenant:    old.TenantID,
		hostParamOwner:     old.OwnerUserID,
		hostParamMetric:    old.DistanceMetric,
		hostParamIndexType: old.IndexType,
		hostParamUpdatedAt: strconv.FormatInt(update.UpdatedAt, 10),
	}, update.Metadata, old.Labels)
	if err != nil {
		return err
	}
	cmd, encErr := protocol.EncodeVectorIndexReconfigure(protocol.VectorIndexReconfigureParams{
		Name:        hostNamespace(namespace),
		IndexType:   byte(a.Record.IndexType),
		IndexParams: params,
	})
	return s.propose(ctx, cmd, encErr)
}

// DeleteNamespace proposes OpVectorNamespaceDelete.
func (s *hostVectorStorage) DeleteNamespace(ctx context.Context, namespace string) error {
	cmd, encErr := protocol.EncodeVectorNamespaceDelete(hostNamespace(namespace))
	return s.propose(ctx, cmd, encErr)
}

// ListNamespaces reads every namespace through the host.
func (s *hostVectorStorage) ListNamespaces(ctx context.Context) ([]vector.NamespaceInfo, error) {
	got, err := s.host.SyncRead(ctx, s.shardID, VectorNamespaceListQuery{})
	if err != nil {
		return nil, err
	}
	answers, ok := got.([]VectorNamespaceAnswer)
	if !ok && got != nil {
		return nil, &HostVectorAnswerError{Query: "VectorNamespaceListQuery", Got: fmt.Sprintf("%T", got)}
	}
	out := make([]vector.NamespaceInfo, 0, len(answers))
	for _, a := range answers {
		out = append(out, *infoFromAnswer(a))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// GetNamespaceInfo reads one namespace through the host.
func (s *hostVectorStorage) GetNamespaceInfo(ctx context.Context, namespace string) (*vector.NamespaceInfo, error) {
	a, err := s.namespaceAnswer(ctx, namespace)
	if err != nil {
		return nil, err
	}
	return infoFromAnswer(*a), nil
}

func upsertParams(vec *vector.VectorValue) protocol.VectorUpsertParams {
	return protocol.VectorUpsertParams{HasID: true, ID: vec.ID, Embedding: vec.Embedding, Metadata: vec.Metadata}
}

// UpsertVector proposes OpVectorUpsert at vec.ID.
func (s *hostVectorStorage) UpsertVector(ctx context.Context, namespace string, vec *vector.VectorValue) error {
	if vec == nil {
		return vector.ErrInvalidVector
	}
	cmd, encErr := protocol.EncodeVectorUpsert(hostNamespace(namespace), vec.UpdatedAt, upsertParams(vec))
	return s.propose(ctx, cmd, encErr)
}

// UpdateVectorMetadata proposes OpVectorUpdateMetadata: the merge with the
// stored metadata (when merge is set) and the removal of deleteKeys run at
// apply time against the committed record, and the embedding is never read
// or re-sent, so a concurrent upsert or delete of the ID is never reverted.
// A vector missing at apply is refused as not found, never re-created.
func (s *hostVectorStorage) UpdateVectorMetadata(ctx context.Context, namespace string, id uint64, metadata map[string]string, merge bool, deleteKeys []string, updatedAt int64) error {
	cmd, encErr := protocol.EncodeVectorUpdateMetadata(protocol.VectorUpdateMetadataParams{
		Name:              hostNamespace(namespace),
		ID:                id,
		TimestampUnixNano: updatedAt,
		Metadata:          metadata,
		Merge:             merge,
		DeleteKeys:        deleteKeys,
	})
	return s.propose(ctx, cmd, encErr)
}

// BatchUpsertVectors proposes one OpVectorBatchUpsert, all or nothing.
func (s *hostVectorStorage) BatchUpsertVectors(ctx context.Context, namespace string, vecs []*vector.VectorValue) error {
	if len(vecs) == 0 {
		return vector.ErrEmptyBatch
	}
	items := make([]protocol.VectorUpsertParams, 0, len(vecs))
	var ts int64
	for _, v := range vecs {
		if v == nil {
			return vector.ErrInvalidVector
		}
		if v.UpdatedAt > ts {
			ts = v.UpdatedAt
		}
		items = append(items, upsertParams(v))
	}
	cmd, encErr := protocol.EncodeVectorBatchUpsert(protocol.VectorBatchUpsertParams{
		Name: hostNamespace(namespace), TimestampUnixNano: ts, Vectors: items,
	})
	return s.propose(ctx, cmd, encErr)
}

// DeleteVector proposes OpVectorDelete.
func (s *hostVectorStorage) DeleteVector(ctx context.Context, namespace string, id uint64) error {
	cmd, encErr := protocol.EncodeVectorDelete(hostNamespace(namespace), id)
	return s.propose(ctx, cmd, encErr)
}

// BatchDeleteVectors proposes one OpVectorBatchDelete, all or nothing.
func (s *hostVectorStorage) BatchDeleteVectors(ctx context.Context, namespace string, ids []uint64) error {
	if len(ids) == 0 {
		return vector.ErrEmptyBatch
	}
	cmd, encErr := protocol.EncodeVectorBatchDelete(hostNamespace(namespace), ids)
	return s.propose(ctx, cmd, encErr)
}

func valueFromRecord(id uint64, rec vectorindex.VectorRecord) *vector.VectorValue {
	return &vector.VectorValue{
		ID:        id,
		Embedding: rec.Embedding,
		Metadata:  rec.Metadata,
		CreatedAt: rec.CreatedAtUnixNano,
		UpdatedAt: rec.UpdatedAtUnixNano,
		Version:   1,
	}
}

// GetVector reads one vector through the host.
func (s *hostVectorStorage) GetVector(ctx context.Context, namespace string, id uint64) (*vector.VectorValue, error) {
	got, err := s.host.SyncRead(ctx, s.shardID, VectorGetQuery{Namespace: hostNamespace(namespace), ID: id})
	if err != nil {
		return nil, err
	}
	if got == nil {
		return nil, fmt.Errorf("namespace %q vector %d: %w", namespace, id, vector.ErrVectorNotFound)
	}
	rec, ok := got.(*vectorindex.VectorRecord)
	if !ok || rec == nil {
		return nil, &HostVectorAnswerError{Query: "VectorGetQuery", Got: fmt.Sprintf("%T", got)}
	}
	return valueFromRecord(id, *rec), nil
}

// pageScan applies opts' Offset and Limit to results.
func pageScan(results []*vector.VectorValue, opts *vector.ScanOptions) []*vector.VectorValue {
	if opts == nil {
		return results
	}
	if opts.Offset > 0 {
		if opts.Offset >= len(results) {
			return []*vector.VectorValue{}
		}
		results = results[opts.Offset:]
	}
	if opts.Limit > 0 && len(results) > opts.Limit {
		results = results[:opts.Limit]
	}
	return results
}

func (s *hostVectorStorage) scan(ctx context.Context, namespace string, fromID uint64, limit int) ([]*vector.VectorValue, error) {
	got, err := s.host.SyncRead(ctx, s.shardID, VectorScanQuery{Namespace: hostNamespace(namespace), FromID: fromID, Limit: limit})
	if err != nil {
		return nil, err
	}
	items, ok := got.([]VectorScanItem)
	if !ok && got != nil {
		return nil, &HostVectorAnswerError{Query: "VectorScanQuery", Got: fmt.Sprintf("%T", got)}
	}
	out := make([]*vector.VectorValue, 0, len(items))
	for _, it := range items {
		out = append(out, valueFromRecord(it.ID, it.Record))
	}
	return out, nil
}

// ScanNamespace reads the namespace's vectors in ascending ID order from
// opts.FromID, then applies Offset and Limit.
func (s *hostVectorStorage) ScanNamespace(ctx context.Context, namespace string, opts *vector.ScanOptions) ([]*vector.VectorValue, error) {
	var fromID uint64
	limit := 0
	if opts != nil {
		fromID = opts.FromID
		if opts.Limit > 0 {
			limit = opts.Offset + opts.Limit
		}
	}
	results, err := s.scan(ctx, namespace, fromID, limit)
	if err != nil {
		return nil, err
	}
	return pageScan(results, opts), nil
}

// matchesFilter mirrors the memory storage's filter semantics: equal means
// present and equal, not-equal means absent or different, and In / NotIn
// always pass.
func matchesFilter(meta map[string]string, f vector.MetadataFilter) bool {
	v, ok := meta[f.Field]
	switch f.Operator {
	case vector.FilterEqual:
		return ok && v == f.Value
	case vector.FilterNotEqual:
		return !ok || v != f.Value
	default:
		return true
	}
}

// ScanByMetadata filters a full namespace scan on field == value. It does
// not read the engine's metadata index yet; index-backed lookups are a later
// slice of #923.
func (s *hostVectorStorage) ScanByMetadata(ctx context.Context, namespace, field, value string, opts *vector.ScanOptions) ([]*vector.VectorValue, error) {
	return s.ScanByMultipleFilters(ctx, namespace, []vector.MetadataFilter{{Field: field, Value: value, Operator: vector.FilterEqual}}, opts)
}

// ScanByMultipleFilters filters a full namespace scan on every filter, then
// applies Offset and Limit. Like ScanByMetadata it does not read the
// engine's metadata index yet.
func (s *hostVectorStorage) ScanByMultipleFilters(ctx context.Context, namespace string, filters []vector.MetadataFilter, opts *vector.ScanOptions) ([]*vector.VectorValue, error) {
	all, err := s.scan(ctx, namespace, 0, 0)
	if err != nil {
		return nil, err
	}
	out := make([]*vector.VectorValue, 0, len(all))
	for _, v := range all {
		keep := true
		for _, f := range filters {
			if !matchesFilter(v.Metadata, f) {
				keep = false
				break
			}
		}
		if keep {
			out = append(out, v)
		}
	}
	return pageScan(out, opts), nil
}

// GetStats reports the namespace's vector count.
func (s *hostVectorStorage) GetStats(ctx context.Context, namespace string) (*vector.NamespaceStats, error) {
	a, err := s.namespaceAnswer(ctx, namespace)
	if err != nil {
		return nil, err
	}
	return &vector.NamespaceStats{Namespace: namespace, Count: a.Count}, nil
}

// NextID is never called by the vector service: the engine allocates IDs.
func (s *hostVectorStorage) NextID(namespace string) (uint64, error) {
	return 0, &HostVectorIDAllocationError{Namespace: namespace}
}

// BatchAllocateIDs is never called by the vector service: the engine
// allocates IDs.
func (s *hostVectorStorage) BatchAllocateIDs(namespace string, _ int) ([]uint64, error) {
	return nil, &HostVectorIDAllocationError{Namespace: namespace}
}

// Compact is a no-op: the host's store compacts itself.
func (s *hostVectorStorage) Compact(context.Context, string) error { return nil }

// CleanupTombstones is a no-op: engine deletes leave no tombstones here.
func (s *hostVectorStorage) CleanupTombstones(context.Context, time.Duration) error { return nil }

// Close is a no-op: the host's owner closes the host.
func (s *hostVectorStorage) Close() error { return nil }
