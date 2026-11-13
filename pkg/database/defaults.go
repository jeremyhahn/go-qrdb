package database

import "github.com/jeremyhahn/go-qrdb/pkg/config"

// DefaultsFromConfig converts the node's database_defaults block into the
// Defaults a Manager fills into a new database record.
// RunMode is a node's run mode, which picks a new database's backend when
// nothing else names one.
type RunMode string

const (
	// RunModeRaft is a node hosting Raft groups; its databases default to
	// the raft backend.
	RunModeRaft RunMode = "raft"
	// RunModeStandalone is a node without Raft; its databases default to
	// the pebble backend.
	RunModeStandalone RunMode = "standalone"
)

// runModeBackends maps a node run mode to its default database backend.
var runModeBackends = map[RunMode]BackendType{
	RunModeRaft:       BackendRaft,
	RunModeStandalone: BackendPebble,
}

// RunModeForBackend is the run mode of a node whose primary engine uses
// backend: raft for the raft backend, standalone for any other.
func RunModeForBackend(backend BackendType) RunMode {
	if backend == BackendRaft {
		return RunModeRaft
	}
	return RunModeStandalone
}

// DefaultsFromConfig converts the node's database_defaults key. RunMode is
// not part of the key; the caller sets it from the node's run mode.
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
// sent without a raft block gets one, because a raft block is what has the
// Manager issue the database a shard. A cluster mode neither the request's
// block nor the node names is none, for a supplied block (one naming only
// its state storage) exactly as for a synthesized one. The record then holds every resolved value, and opening it never
// re-resolves one from node config.
func (d Defaults) fill(cfg *DatabaseConfig) {
	d.fillFor(cfg, true)
}

// fillFor is fill, with synthesize selecting whether a raft database the
// request gave no raft block gets one. A create through the API does: the
// block records the dedicated group the manager issues a shard for. Boot
// provisioning does not: a configured raft database without a block runs
// on the node's shared group, as it always has, and no shard is allocated
// before the cluster's feature gate has started.
func (d Defaults) fillFor(cfg *DatabaseConfig, synthesize bool) {
	if cfg.Backend == "" {
		cfg.Backend = d.Backend
	}
	if cfg.Backend == "" {
		cfg.Backend = runModeBackends[d.RunMode]
	}
	if cfg.Backend != BackendRaft {
		return
	}
	if cfg.Replicas == 0 {
		cfg.Replicas = d.Replicas
	}
	if cfg.Raft == nil && !synthesize {
		return
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
	// A cluster mode neither the request nor the node names is none,
	// whether the request carried a block (one naming only its state
	// storage, say) or the block is synthesized here.
	if raft.ClusterMode == "" {
		raft.ClusterMode = RaftNone
	}
	if raft.StateStorage == "" {
		raft.StateStorage = d.RaftStateStorage
	}
	cfg.Raft = &raft
}

// ResolveDefaults returns a copy of cfg with the node's database_defaults
// applied, exactly as the manager applies them at create. A caller that
// authorizes, entitles, normalises or meters a create resolves first, so
// every gate sees the configuration the manager records. The raft block is
// copied, so cfg is left untouched. fill is idempotent: resolving twice, or
// resolving before the manager's own fill, records the same configuration.
func (m *Manager) ResolveDefaults(cfg DatabaseConfig) DatabaseConfig {
	if cfg.Raft != nil {
		raft := *cfg.Raft
		cfg.Raft = &raft
	}
	if m == nil {
		return cfg
	}
	m.defaults.fill(&cfg)
	return cfg
}
