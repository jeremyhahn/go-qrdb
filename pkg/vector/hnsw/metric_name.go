package hnsw

import (
	"strconv"
	"strings"
)

// distanceMetricNames maps every distance-metric name a namespace may be
// created with to the metric the index computes. The keys are the canonical
// upper-case names the gRPC, QUIC and MCP surfaces send; the SDK's REST and
// embedded transports send the same names in lower case, which
// ParseDistanceMetric folds. It is the one name table in the tree: the
// service, the consensus state machine and the index factory all resolve a
// name through it, so a namespace's metric cannot differ between them.
var distanceMetricNames = map[string]DistanceMetric{
	"L2":          L2Distance,
	"EUCLIDEAN":   L2Distance,
	"COSINE":      CosineDistance,
	"DOT_PRODUCT": DotProductDistance,
	"MANHATTAN":   ManhattanDistance,
}

// ParseDistanceMetric resolves a distance-metric name case-insensitively. An
// empty name is L2, the metric of a namespace recorded before the metric was.
// A name not in the table is an *UnknownDistanceMetricError, never read as
// L2: doing so would answer every search under a metric the caller never
// asked for.
func ParseDistanceMetric(name string) (DistanceMetric, error) {
	if name == "" {
		return L2Distance, nil
	}
	m, ok := distanceMetricNames[strings.ToUpper(name)]
	if !ok {
		return 0, &UnknownDistanceMetricError{Name: name}
	}
	return m, nil
}

// UnknownDistanceMetricError reports a distance-metric name the index does
// not implement.
type UnknownDistanceMetricError struct {
	Name string
}

// Error names the refused metric and the names accepted.
func (e *UnknownDistanceMetricError) Error() string {
	return "hnsw: unknown distance metric " + strconv.Quote(e.Name) +
		" (want L2, EUCLIDEAN, COSINE, DOT_PRODUCT or MANHATTAN)"
}
