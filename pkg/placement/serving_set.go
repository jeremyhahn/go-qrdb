package placement

import (
	"context"
	"strconv"
)

// ServingSetFunc reports the serving set of tenantID for a group named
// database: the Raft addresses of the nodes that may host its replicas.
// confined is false when the group is not confined (a shared tenant, or an
// isolated tenant's home group, which defines the serving set itself); nodes
// is then ignored. A non-nil err fails the placement closed.
type ServingSetFunc func(ctx context.Context, tenantID, database string) (nodes []string, confined bool, err error)

// WithServingSet confines every group the scheduler places to its tenant's
// serving set (docs/multi-tenancy/engineering.md, phase 6): Place and
// PlaceAdditional consider only nodes serving returns, and refuse with
// *ServingSetError when the serving set has too few eligible nodes. A nil
// serving leaves placement unconfined.
func WithServingSet(serving ServingSetFunc) SchedulerOption {
	return func(s *Scheduler) { s.serving = serving }
}

// ServingSetError refuses a placement of an isolated tenant's database
// group that would need a node outside the tenant's serving set (the nodes
// hosting its home group).
type ServingSetError struct {
	// TenantID owns the group.
	TenantID string

	// Database is the group's database name.
	Database string

	// Node is the refused node's Raft address, when one node was named
	// (a scale-out or rebalance destination); empty for a planner refusal.
	Node string

	// Have is the number of eligible serving-set nodes the planner found.
	Have int

	// Need is the number of nodes the placement needed.
	Need int
}

// Error implements error.
func (e *ServingSetError) Error() string {
	if e.Node != "" {
		return "placement: node " + strconv.Quote(e.Node) + " is outside the serving set of tenant " +
			strconv.Quote(e.TenantID) + "; database " + strconv.Quote(e.Database) + " is confined to it"
	}
	return "placement: database " + strconv.Quote(e.Database) + " of tenant " + strconv.Quote(e.TenantID) +
		" is confined to the tenant's serving set: have " + strconv.Itoa(e.Have) +
		" eligible serving nodes, need " + strconv.Itoa(e.Need)
}

// CheckServingSet returns *ServingSetError when raftAddr is not in nodes.
func CheckServingSet(nodes []string, tenantID, database, raftAddr string) error {
	for _, n := range nodes {
		if n == raftAddr {
			return nil
		}
	}
	return &ServingSetError{TenantID: tenantID, Database: database, Node: raftAddr}
}

// confine narrows candidates to the serving set of (tenantID, database) and
// refuses with *ServingSetError when fewer than need remain. It returns
// candidates unchanged when the scheduler has no serving-set lookup or the
// group is not confined.
func (s *Scheduler) confine(ctx context.Context, tenantID, database string, candidates []NodeLoad, need int) ([]NodeLoad, error) {
	if s.serving == nil {
		return candidates, nil
	}
	nodes, confined, err := s.serving(ctx, tenantID, database)
	if err != nil {
		return nil, err
	}
	if !confined {
		return candidates, nil
	}
	in := make(map[string]struct{}, len(nodes))
	for _, n := range nodes {
		in[n] = struct{}{}
	}
	out := candidates[:0:0]
	for _, n := range candidates {
		if _, ok := in[n.RaftAddr]; ok {
			out = append(out, n)
		}
	}
	if len(out) < need {
		return nil, &ServingSetError{TenantID: tenantID, Database: database, Have: len(out), Need: need}
	}
	return out, nil
}
