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
// count, the cluster mode and the state storage. A raft database the caller
// sent without a raft block gets one (cluster mode none when the node names
// none), because a raft block is what has the Manager issue the database a
// shard. The record then holds every resolved value, and opening it never
// re-resolves one from node config.
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
	// A raft database always records its raft block, so the manager issues
	// it a shard ID exactly as for a block the request carried.
	var raft DatabaseRaftConfig
	if cfg.Raft != nil {
		raft = *cfg.Raft
	}
	if raft.ClusterMode == "" {
		raft.ClusterMode = d.RaftClusterMode
	}
	if raft.ClusterMode == "" && cfg.Raft == nil {
		raft.ClusterMode = RaftNone
	}
	if raft.StateStorage == "" {
		raft.StateStorage = d.RaftStateStorage
	}
	cfg.Raft = &raft
}
