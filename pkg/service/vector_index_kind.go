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
	"errors"
	"strings"

	"github.com/jeremyhahn/go-qrdb/pkg/interfaces"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/vector"
	vectorindex "github.com/jeremyhahn/go-qrdb/pkg/vector"
	"github.com/jeremyhahn/go-qrdb/pkg/vector/hnsw"
)

// defaultIndexType is the index type of a namespace created without one.
const defaultIndexType = "HNSW"

// vectorIndexTypes maps every index-type name a namespace may be created
// with, upper-cased, to its canonical name: the one GetIndexStats reports and
// the gRPC API maps to its enum. IVF_FLAT is the wire name the SDK sends; IVF
// is the one the gRPC and MCP surfaces send.
var vectorIndexTypes = map[string]string{
	"HNSW":     "HNSW",
	"FLAT":     "FLAT",
	"IVF":      "IVF",
	"IVF_FLAT": "IVF",
}

// liveIndexTypes maps each canonical index type built through the live-index
// path (pkg/vector) to its record enum. HNSW is absent: the service builds it
// directly, because the live-index HNSW does not take the namespace's
// index_config.
var liveIndexTypes = map[string]vectorindex.IndexType{
	"FLAT": vectorindex.IndexFlat,
	"IVF":  vectorindex.IndexIVFFlat,
}

// liveIndexMetrics maps each metric a namespace may be created with to its
// live-index record enum.
var liveIndexMetrics = map[hnsw.DistanceMetric]vectorindex.DistanceMetric{
	hnsw.L2Distance:         vectorindex.DistanceEuclidean,
	hnsw.CosineDistance:     vectorindex.DistanceCosine,
	hnsw.DotProductDistance: vectorindex.DistanceDotProduct,
	hnsw.ManhattanDistance:  vectorindex.DistanceManhattan,
}

// parseIndexType resolves an index-type name, case-insensitively, to its
// canonical name. An empty name is HNSW; any other name not in
// vectorIndexTypes is an *UnknownIndexTypeError.
func parseIndexType(indexType string) (string, error) {
	if indexType == "" {
		return defaultIndexType, nil
	}
	canonical, ok := vectorIndexTypes[strings.ToUpper(indexType)]
	if !ok {
		return "", &UnknownIndexTypeError{IndexType: indexType}
	}
	return canonical, nil
}

// reportedIndexType is the index type ListNamespaces, GetNamespaceInfo and
// GetIndexStats report for a stored
// namespace: the canonical name when the record's type is known, the record's
// own value when it is not.
func reportedIndexType(stored string) string {
	canonical, err := parseIndexType(stored)
	if err != nil {
		return stored
	}
	return canonical
}

// reportedDistanceMetric is the distance metric GetNamespaceInfo and
// GetIndexStats report for a stored namespace: the name it was created with,
// or L2 for a namespace recorded before its metric was.
func reportedDistanceMetric(stored string) string {
	if stored == "" {
		return "L2"
	}
	return stored
}

// parseNamespaceGeometry resolves a namespace's distance metric and index
// type together; every index type implements every metric.
func parseNamespaceGeometry(metricName, indexTypeName string) (hnsw.DistanceMetric, string, error) {
	metric, err := parseDistanceMetric(metricName)
	if err != nil {
		return 0, "", err
	}
	indexType, err := parseIndexType(indexTypeName)
	if err != nil {
		return 0, "", err
	}
	return metric, indexType, nil
}

// checkStoredGeometry refuses a stored namespace record whose distance metric
// or index type this server does not implement, with an
// *UnsupportedNamespaceGeometryError naming the logical namespace and the
// stored value. scoped is the namespace's storage key. A record that parses
// returns nil; the caller builds or reads the namespace as usual.
func checkStoredGeometry(scoped string, info *vector.NamespaceInfo) error {
	if _, err := parseDistanceMetric(info.DistanceMetric); err != nil {
		_, logical := unscopedNamespace(scoped)
		return &UnsupportedNamespaceGeometryError{Namespace: logical, Field: "distance metric", Value: info.DistanceMetric}
	}
	if _, err := parseIndexType(info.IndexType); err != nil {
		_, logical := unscopedNamespace(scoped)
		return &UnsupportedNamespaceGeometryError{Namespace: logical, Field: "index type", Value: info.IndexType}
	}
	return nil
}

// newLiveNamespaceIndex builds a FLAT or IVF namespace index through
// vectorindex.NewLiveIndex, the path the replicated engine replays, so the
// index a namespace answers from is the one its record names.
func newLiveNamespaceIndex(info *vector.NamespaceInfo, indexType vectorindex.IndexType, metric hnsw.DistanceMetric, params map[string]string) (interfaces.HNSWIndex, error) {
	live, err := vectorindex.NewLiveIndex(info.Name, vectorindex.NamespaceRecord{
		Dimension:      info.Dimension,
		DistanceMetric: liveIndexMetrics[metric],
		IndexType:      indexType,
		IndexParams:    params,
	})
	if err != nil {
		return nil, err
	}
	return &liveIndexAdapter{namespace: info.Name, live: live, dimension: info.Dimension}, nil
}

// checkGeometryFeature refuses a create of any index type whose metric's
// record value needs a cluster feature (vectorindex.MetricFeature) that is not
// yet active for this service's group, with *VectorMetricNotActiveError. HNSW
// is gated as FLAT and IVF are: its namespace replicates the metric too, and a
// replica running an older binary reads a metric it does not know as L2, so a
// Manhattan HNSW namespace would rank differently on that replica. The gates
// are the ones WithFeatureGates set, else the raft node's. A service with
// neither -- standalone, or a test double -- has every replica running this
// binary, so nothing is refused.
func (v *vectorService) checkGeometryFeature(metric hnsw.DistanceMetric, indexType string) error {
	feature, gated := vectorindex.MetricFeature(liveIndexMetrics[metric])
	if !gated {
		return nil
	}
	gates := v.featureGates
	if gates == nil {
		gates, _ = v.raftNode.(FeatureGatedHost)
	}
	if gates == nil || gates.FeatureEnabled(v.shardID, feature) {
		return nil
	}
	return &VectorMetricNotActiveError{IndexType: indexType, Feature: feature, ShardID: v.shardID}
}

// liveIndexAdapter presents a vectorindex.LiveIndex as the service's
// namespace index. Upsert stands in for Insert, an empty index answers
// hnsw.ErrEmptyIndex as the HNSW index does, and an IVF index below its
// training floor answers *VectorIndexUntrainedError.
type liveIndexAdapter struct {
	namespace string
	live      vectorindex.LiveIndex
	dimension uint32
}

var _ interfaces.HNSWIndex = (*liveIndexAdapter)(nil)

// Insert adds id, replacing it if present.
func (a *liveIndexAdapter) Insert(id uint64, vec []float32) error {
	return a.live.Upsert(id, vec)
}

// Delete removes id; an absent id is a no-op.
func (a *liveIndexAdapter) Delete(id uint64) error {
	return a.live.Delete(id)
}

// Search returns up to k nearest neighbours of query.
func (a *liveIndexAdapter) Search(query []float32, k int) ([]*interfaces.HNSWSearchResult, error) {
	if a.live.Count() == 0 {
		return nil, hnsw.ErrEmptyIndex
	}
	hits, err := a.live.Search(query, k)
	switch {
	case errors.Is(err, vectorindex.ErrIndexNotReady):
		_, logical := unscopedNamespace(a.namespace)
		return nil, &VectorIndexUntrainedError{Namespace: logical}
	case err != nil:
		return nil, err
	}
	results := make([]*interfaces.HNSWSearchResult, len(hits))
	for i, hit := range hits {
		results[i] = &interfaces.HNSWSearchResult{ID: hit.ID, Distance: hit.Distance}
	}
	return results, nil
}

// Count returns the number of vectors held.
func (a *liveIndexAdapter) Count() int64 {
	return a.live.Count()
}

// GetStats reports the vector count and dimension; a FLAT or IVF index has no
// graph, so its layer and connection figures are zero.
func (a *liveIndexAdapter) GetStats() *interfaces.HNSWStats {
	return &interfaces.HNSWStats{NodeCount: a.live.Count(), Dimensions: int(a.dimension)}
}
