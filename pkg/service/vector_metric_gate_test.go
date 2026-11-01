package service

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/feature"
	"github.com/jeremyhahn/go-qrdb/pkg/interfaces"
	"github.com/jeremyhahn/go-qrdb/pkg/vector/hnsw"
)

// gatedRaftNode is a RaftNode whose groups gate features: FeatureEnabled
// answers from enabled and records every shard it was asked about.
type gatedRaftNode struct {
	mockRaftNode
	enabled map[feature.Feature]bool
	asked   []uint64
}

var (
	_ interfaces.RaftNode = (*gatedRaftNode)(nil)
	_ FeatureGatedHost    = (*gatedRaftNode)(nil)
)

func (g *gatedRaftNode) FeatureEnabled(shardID uint64, feature feature.Feature) bool {
	g.asked = append(g.asked, shardID)
	return g.enabled[feature]
}

func TestCheckGeometryFeature_RefusesManhattanOnEveryTypeUntilActive(t *testing.T) {
	for _, indexType := range []string{"FLAT", "IVF", "HNSW"} {
		node := &gatedRaftNode{}
		vs := &vectorService{raftNode: node, shardID: 9}
		err := vs.checkGeometryFeature(hnsw.ManhattanDistance, indexType)
		notActive := requireErrorAs[*VectorMetricNotActiveError](t, err)
		assert.Equal(t, indexType, notActive.IndexType)
		assert.Equal(t, featuregate.VectorManhattan, notActive.Feature)
		assert.Equal(t, uint64(9), notActive.ShardID)
		assert.Equal(t, []uint64{9}, node.asked)

		node.enabled = map[feature.Feature]bool{featuregate.VectorManhattan: true}
		assert.NoError(t, vs.checkGeometryFeature(hnsw.ManhattanDistance, indexType), "%s once the gate is active", indexType)
	}
}

func TestCheckGeometryFeature_AdmitsWhatNeedsNoGate(t *testing.T) {
	node := &gatedRaftNode{}
	vs := &vectorService{raftNode: node}
	// Every vector-engine binary applies the original three metrics.
	for _, metric := range []hnsw.DistanceMetric{hnsw.L2Distance, hnsw.CosineDistance, hnsw.DotProductDistance} {
		for _, indexType := range []string{"FLAT", "IVF", "HNSW"} {
			assert.NoError(t, vs.checkGeometryFeature(metric, indexType))
		}
	}
	assert.Empty(t, node.asked, "no gate is consulted when nothing needs one")
}

func TestCheckGeometryFeature_NoGatedHostRefusesNothing(t *testing.T) {
	for _, vs := range []*vectorService{{}, {raftNode: &mockRaftNode{}}} {
		assert.NoError(t, vs.checkGeometryFeature(hnsw.ManhattanDistance, "IVF"))
	}
}

func TestWithFeatureGates_GatesAServiceWithoutARaftNode(t *testing.T) {
	gates := &gatedRaftNode{}
	vs := &vectorService{shardID: 4}
	WithFeatureGates(gates)(vs)

	notActive := requireErrorAs[*VectorMetricNotActiveError](t, vs.checkGeometryFeature(hnsw.ManhattanDistance, "FLAT"))
	assert.Equal(t, featuregate.VectorManhattan, notActive.Feature)
	assert.Equal(t, []uint64{4}, gates.asked)

	gates.enabled = map[feature.Feature]bool{featuregate.VectorManhattan: true}
	assert.NoError(t, vs.checkGeometryFeature(hnsw.ManhattanDistance, "FLAT"))
}

func TestWithFeatureGates_TakesPrecedenceOverTheRaftNode(t *testing.T) {
	node := &gatedRaftNode{enabled: map[feature.Feature]bool{featuregate.VectorManhattan: true}}
	gates := &gatedRaftNode{}
	vs := &vectorService{raftNode: node}
	WithFeatureGates(gates)(vs)

	requireErrorAs[*VectorMetricNotActiveError](t, vs.checkGeometryFeature(hnsw.ManhattanDistance, "IVF"))
	assert.Empty(t, node.asked, "the raft node is not asked when gates were set")
}

func TestNewVectorService_WithFeatureGatesRefusesManhattanFlat(t *testing.T) {
	svc, err := NewVectorService(newVecMockStorage(), newVecTestLogger(), WithFeatureGates(&gatedRaftNode{}))
	require.NoError(t, err)

	_, err = svc.CreateNamespace(context.Background(), &CreateNamespaceRequest{
		Namespace: "ns", Dimension: 2, DistanceMetric: "manhattan", IndexType: "flat",
	})
	requireErrorAs[*VectorMetricNotActiveError](t, err)
}

func TestValidateCreateNamespaceRequest_GatesManhattanOnEveryType(t *testing.T) {
	for _, indexType := range []string{"flat", "ivf", "hnsw"} {
		vs := &vectorService{raftNode: &gatedRaftNode{}}
		err := vs.validateCreateNamespaceRequest(&CreateNamespaceRequest{
			Namespace: "ns", Dimension: 2, DistanceMetric: "manhattan", IndexType: indexType,
		})
		requireErrorAs[*VectorMetricNotActiveError](t, err)
	}
}

// TestCreateNamespace_ManhattanHNSWWaitsForEveryReplica models a mixed-version
// rollout: while one replica's binary predates VectorManhattan the group's gate
// is inactive, and an HNSW create with that metric is refused before anything
// is built or proposed, since the older replica would read the metric as L2.
// Once every replica runs a binary that knows it, the same create succeeds.
func TestCreateNamespace_ManhattanHNSWWaitsForEveryReplica(t *testing.T) {
	svc, _ := newBasicVecSvc(t)
	vs := svc.(*vectorService)
	node := &gatedRaftNode{}
	vs.raftNode = node
	req := &CreateNamespaceRequest{Namespace: "ns", Dimension: 2, DistanceMetric: "manhattan", IndexType: "hnsw"}

	_, err := svc.CreateNamespace(context.Background(), req)
	notActive := requireErrorAs[*VectorMetricNotActiveError](t, err)
	assert.Equal(t, "HNSW", notActive.IndexType)
	assert.Equal(t, featuregate.VectorManhattan, notActive.Feature)
	vs.indexMu.RLock()
	assert.Empty(t, vs.hnswIndexes, "a refused create builds no index")
	vs.indexMu.RUnlock()

	node.enabled = map[feature.Feature]bool{featuregate.VectorManhattan: true}
	_, err = svc.CreateNamespace(context.Background(), req)
	require.NoError(t, err)
}

func TestCreateNamespace_ManhattanIVFWaitsForTheGate(t *testing.T) {
	svc, _ := newBasicVecSvc(t)
	vs := svc.(*vectorService)
	node := &gatedRaftNode{}
	vs.raftNode = node
	req := &CreateNamespaceRequest{Namespace: "ns", Dimension: 2, DistanceMetric: "MANHATTAN", IndexType: "IVF"}

	_, err := svc.CreateNamespace(context.Background(), req)
	requireErrorAs[*VectorMetricNotActiveError](t, err)
	vs.indexMu.RLock()
	assert.Empty(t, vs.hnswIndexes, "a refused create builds no index")
	vs.indexMu.RUnlock()

	node.enabled = map[feature.Feature]bool{featuregate.VectorManhattan: true}
	_, err = svc.CreateNamespace(context.Background(), req)
	require.NoError(t, err)
}

func TestVectorMetricNotActiveError_NamesTheFeatureAndShard(t *testing.T) {
	msg := (&VectorMetricNotActiveError{IndexType: "FLAT", Feature: featuregate.VectorManhattan, ShardID: 3}).Error()
	for _, want := range []string{"FLAT", `"vector-manhattan"`, "shard 3"} {
		assert.True(t, strings.Contains(msg, want), "Error() = %q, missing %q", msg, want)
	}
}
