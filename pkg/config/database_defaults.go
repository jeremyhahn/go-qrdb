package config

import "fmt"

// DatabaseDefaults is the node's database_defaults block
// (docs/databases/architecture.md "What stays node-level"): the values
// filled into a database record at creation when the caller left the field
// empty. They are never consulted again for that database. pkg/database
// converts it with database.DefaultsFromConfig.
type DatabaseDefaults struct {
	// Backend is the default storage backend of a new database. Empty
	// selects the run mode's default.
	Backend string `yaml:"backend,omitempty" json:"backend,omitempty"`
	// Replicas is the default replication factor of a new raft database.
	// Zero selects one.
	Replicas int `yaml:"replicas,omitempty" json:"replicas,omitempty"`
	// Raft holds the raft defaults of a new raft database.
	Raft DatabaseRaftDefaults `yaml:"raft,omitempty" json:"raft,omitempty"`
}

// DatabaseRaftDefaults is the raft part of DatabaseDefaults.
type DatabaseRaftDefaults struct {
	// ClusterMode is the default cluster mode: none, local or distributed.
	ClusterMode string `yaml:"clusterMode,omitempty" json:"clusterMode,omitempty"`
	// StateStorage is the default state storage of the database's group:
	// pebble, file or memory. Empty selects pebble.
	StateStorage string `yaml:"state_storage,omitempty" json:"state_storage,omitempty"`
}

// DatabaseDefaultsError reports an invalid database_defaults field.
type DatabaseDefaultsError struct {
	// Field is the configuration key, e.g. "database_defaults.backend".
	Field string
	// Value is the refused value.
	Value string
	// Message says what is accepted.
	Message string
}

// Error implements error.
func (e *DatabaseDefaultsError) Error() string {
	return fmt.Sprintf("config: %s %q: %s", e.Field, e.Value, e.Message)
}

// validDefaultClusterModes are the accepted database_defaults.raft.clusterMode values.
var validDefaultClusterModes = map[string]struct{}{
	"none":        {},
	"local":       {},
	"distributed": {},
}

// validDefaultStateStorages are the accepted database_defaults.raft.state_storage values.
var validDefaultStateStorages = map[string]struct{}{
	"pebble": {},
	"file":   {},
	"memory": {},
}

// Validate refuses an unknown backend, cluster mode or state storage and a
// negative replica count. Empty fields are valid: they leave the choice to
// the run mode's default.
func (d DatabaseDefaults) Validate() error {
	if d.Backend != "" {
		if _, ok := validDatabaseBackends[d.Backend]; !ok {
			return &DatabaseDefaultsError{Field: "database_defaults.backend", Value: d.Backend,
				Message: "must be one of: memory, file, pebble, raft, objstore"}
		}
	}
	if d.Replicas < 0 {
		return &DatabaseDefaultsError{Field: "database_defaults.replicas", Value: fmt.Sprint(d.Replicas),
			Message: "must be zero or more"}
	}
	if d.Raft.ClusterMode != "" {
		if _, ok := validDefaultClusterModes[d.Raft.ClusterMode]; !ok {
			return &DatabaseDefaultsError{Field: "database_defaults.raft.clusterMode", Value: d.Raft.ClusterMode,
				Message: "must be one of: none, local, distributed"}
		}
	}
	if d.Raft.StateStorage != "" {
		if _, ok := validDefaultStateStorages[d.Raft.StateStorage]; !ok {
			return &DatabaseDefaultsError{Field: "database_defaults.raft.state_storage", Value: d.Raft.StateStorage,
				Message: "must be one of: pebble, file, memory"}
		}
	}
	return nil
}
