package database

import "github.com/jeremyhahn/go-qrdb/pkg/config"

// DefaultsFromConfig converts the node's database_defaults block into the
// Defaults a Manager fills into a new database record.
func DefaultsFromConfig(c config.DatabaseDefaults) Defaults {
	return Defaults{
		Backend:          BackendType(c.Backend),
		Replicas:         c.Replicas,
		RaftClusterMode:  RaftClusterMode(c.Raft.ClusterMode),
		RaftStateStorage: BackendType(c.Raft.StateStorage),
	}
}

// WithDefaults sets the node's database defaults. A database created
// through the Manager has every field its caller left empty filled from
// them before the record is written; an existing database is opened from
// its record and never re-resolved against them.
func WithDefaults(d Defaults) ManagerOption {
	return func(m *Manager) {
		m.defaults = d
	}
}

// fill writes d into the fields of cfg its caller left empty, by the rule
// ResolveBinding applies: the backend first, then -- for a raft database
// only, since a raft option on any other backend is refused -- the replica
// count and the cluster mode. The cluster mode is filled into a raft block
// the caller supplied; fill never adds one, because a raft block is what
// has the Manager issue the database a shard. RaftStateStorage has no field
// in the record and is applied by ResolveBinding alone.
func (d Defaults) fill(cfg *DatabaseConfig) {
	if cfg.Backend == "" {
		cfg.Backend = d.Backend
	}
	if cfg.Backend != BackendRaft {
		return
	}
	if cfg.Replicas == 0 {
		cfg.Replicas = d.Replicas
	}
	if cfg.Raft == nil {
		return
	}
	raft := *cfg.Raft
	if raft.ClusterMode == "" {
		raft.ClusterMode = d.RaftClusterMode
	}
	if raft.StateStorage == "" {
		raft.StateStorage = d.RaftStateStorage
	}
	cfg.Raft = &raft
}
