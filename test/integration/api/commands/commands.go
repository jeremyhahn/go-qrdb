// Copyright 2026 Jeremy Hahn
// Apache 2.0 License

// Package commands provides centralized CLI command definitions for integration
// testing across all protocols.
package commands

// CommandCategory represents a category of commands.
type CommandCategory string

const (
	CategoryKV          CommandCategory = "kv"
	CategoryAdmin       CommandCategory = "admin"
	CategoryVersion     CommandCategory = "version"
	CategoryLease       CommandCategory = "lease"
	CategoryIndex       CommandCategory = "index"
	CategoryBatch       CommandCategory = "batch"
	CategoryTransaction CommandCategory = "transaction"
	CategoryWatch       CommandCategory = "watch"
	CategoryTimeseries  CommandCategory = "timeseries"
	CategoryVector      CommandCategory = "vector"
	CategoryDatabase    CommandCategory = "database"
	CategoryUsers       CommandCategory = "users"
	CategoryRoles       CommandCategory = "roles"
	CategoryCerts       CommandCategory = "certs"
)

// CommandDefinition defines a CLI command with its arguments and expected behavior.
type CommandDefinition struct {
	Name                   string
	Category               CommandCategory
	Command                []string
	RequiredArgs           []ArgDefinition
	OptionalArgs           []ArgDefinition
	Description            string
	RequiresServer         bool
	RequiresSetup          bool
	IsDestructive          bool
	IsStreaming            bool
	RequiresBuildTag       string
	RequiresFileInput      bool
	ExpectedOutputContains []string
}

// ArgDefinition defines a command argument.
type ArgDefinition struct {
	Flag         string
	Value        string
	Description  string
	IsPositional bool
}

// AllCommands returns all CLI commands for testing.
func AllCommands() []CommandDefinition {
	var cmds []CommandDefinition
	cmds = append(cmds, KVCommands()...)
	cmds = append(cmds, AdminCommands()...)
	cmds = append(cmds, VersionCommands()...)
	cmds = append(cmds, LeaseCommands()...)
	cmds = append(cmds, IndexCommands()...)
	cmds = append(cmds, BatchCommands()...)
	cmds = append(cmds, TransactionCommands()...)
	cmds = append(cmds, WatchCommands()...)
	cmds = append(cmds, TimeseriesCommands()...)
	cmds = append(cmds, VectorCommands()...)
	cmds = append(cmds, DatabaseCommands()...)
	cmds = append(cmds, UsersCommands()...)
	cmds = append(cmds, RolesCommands()...)
	cmds = append(cmds, CertsCommands()...)
	return cmds
}

// VersionCommands returns version-related commands.
func VersionCommands() []CommandDefinition {
	return []CommandDefinition{
		{
			Name:                   "version",
			Category:               CategoryVersion,
			Command:                []string{"version"},
			Description:            "Show version information",
			RequiresServer:         false,
			ExpectedOutputContains: []string{"version"},
		},
	}
}

// KVCommands returns key-value operation commands.
func KVCommands() []CommandDefinition {
	return []CommandDefinition{
		{
			Name:           "kv-put",
			Category:       CategoryKV,
			Command:        []string{"kv", "put"},
			Description:    "Store a key-value pair",
			RequiresServer: true,
			RequiredArgs: []ArgDefinition{
				{Value: "test-key", Description: "Key", IsPositional: true},
				{Value: "test-value", Description: "Value", IsPositional: true},
			},
			ExpectedOutputContains: []string{"OK"},
		},
		{
			Name:           "kv-get",
			Category:       CategoryKV,
			Command:        []string{"kv", "get"},
			Description:    "Retrieve a value by key",
			RequiresServer: true,
			RequiresSetup:  true,
			RequiredArgs: []ArgDefinition{
				{Value: "test-key", Description: "Key", IsPositional: true},
			},
		},
		{
			Name:           "kv-del",
			Category:       CategoryKV,
			Command:        []string{"kv", "del"},
			Description:    "Delete a key",
			RequiresServer: true,
			RequiresSetup:  true,
			RequiredArgs: []ArgDefinition{
				{Value: "test-key", Description: "Key", IsPositional: true},
			},
			ExpectedOutputContains: []string{"OK"},
		},
		{
			Name:           "kv-list",
			Category:       CategoryKV,
			Command:        []string{"kv", "list"},
			Description:    "List keys",
			RequiresServer: true,
		},
		{
			Name:           "kv-list-prefix",
			Category:       CategoryKV,
			Command:        []string{"kv", "list"},
			Description:    "List keys with prefix",
			RequiresServer: true,
			RequiredArgs: []ArgDefinition{
				{Value: "test-", Description: "Prefix", IsPositional: true},
			},
		},
		{
			Name:           "kv-exists",
			Category:       CategoryKV,
			Command:        []string{"kv", "exists"},
			Description:    "Check if key exists",
			RequiresServer: true,
			RequiresSetup:  true,
			RequiredArgs: []ArgDefinition{
				{Value: "test-key", Description: "Key", IsPositional: true},
			},
		},
		{
			Name:           "kv-range",
			Category:       CategoryKV,
			Command:        []string{"kv", "range"},
			Description:    "Query a key range with MVCC metadata",
			RequiresServer: true,
			RequiresSetup:  true,
			RequiredArgs: []ArgDefinition{
				{Value: "test-key", Description: "Start", IsPositional: true},
			},
			OptionalArgs: []ArgDefinition{
				{Flag: "limit", Value: "", Description: "Maximum results"},
				{Flag: "revision", Value: "", Description: "Query at revision"},
				{Flag: "count-only", Value: "", Description: "Return only count"},
				{Flag: "keys-only", Value: "", Description: "Omit values"},
				{Flag: "sort-order", Value: "", Description: "Sort order: asc, desc"},
				{Flag: "sort-by", Value: "", Description: "Sort target: key, value, version, create, mod"},
			},
		},
		{
			Name:           "kv-compact",
			Category:       CategoryKV,
			Command:        []string{"kv", "compact"},
			Description:    "Compact MVCC revisions",
			RequiresServer: true,
			RequiresSetup:  true,
			RequiredArgs: []ArgDefinition{
				{Flag: "revision", Value: "1", Description: "Compact to revision"},
			},
			ExpectedOutputContains: []string{"OK"},
		},
		{
			Name:           "kv-revision",
			Category:       CategoryKV,
			Command:        []string{"kv", "revision"},
			Description:    "Show current MVCC revision",
			RequiresServer: true,
		},
	}
}

// AdminCommands returns cluster administration commands.
func AdminCommands() []CommandDefinition {
	return []CommandDefinition{
		{
			Name:                   "admin-health",
			Category:               CategoryAdmin,
			Command:                []string{"admin", "health"},
			Description:            "Check node health",
			RequiresServer:         true,
			ExpectedOutputContains: []string{"Status"},
		},
		{
			Name:                   "admin-status",
			Category:               CategoryAdmin,
			Command:                []string{"admin", "status"},
			Description:            "Show node status",
			RequiresServer:         true,
			ExpectedOutputContains: []string{"NodeID"},
		},
		{
			Name:           "admin-peers",
			Category:       CategoryAdmin,
			Command:        []string{"admin", "peers"},
			Description:    "List shard peers",
			RequiresServer: true,
		},
		{
			Name:           "admin-member-add",
			Category:       CategoryAdmin,
			Command:        []string{"admin", "member", "add"},
			Description:    "Add a replica to a shard",
			RequiresServer: true,
			RequiresSetup:  true,
			RequiredArgs: []ArgDefinition{
				{Value: "127.0.0.1:63010", Description: "Address", IsPositional: true},
			},
			OptionalArgs: []ArgDefinition{
				{Flag: "shard-id", Value: "1", Description: "Shard ID"},
				{Flag: "replica-id", Value: "2", Description: "Replica ID"},
			},
		},
		{
			Name:           "admin-member-remove",
			Category:       CategoryAdmin,
			Command:        []string{"admin", "member", "remove"},
			Description:    "Remove a replica from a shard",
			RequiresServer: true,
			RequiresSetup:  true,
			RequiredArgs: []ArgDefinition{
				{Value: "2", Description: "Replica ID", IsPositional: true},
			},
			OptionalArgs: []ArgDefinition{
				{Flag: "shard-id", Value: "1", Description: "Shard ID"},
			},
		},
		{
			Name:           "admin-snapshot",
			Category:       CategoryAdmin,
			Command:        []string{"admin", "snapshot"},
			Description:    "Request shard snapshot",
			RequiresServer: true,
			RequiresSetup:  true,
			RequiredArgs: []ArgDefinition{
				{Flag: "shard-id", Value: "1", Description: "Shard ID"},
			},
			ExpectedOutputContains: []string{"OK"},
		},
		{
			Name:                   "admin-drain",
			Category:               CategoryAdmin,
			Command:                []string{"admin", "drain"},
			Description:            "Transition node into drain mode",
			RequiresServer:         true,
			RequiresSetup:          true,
			IsDestructive:          true,
			ExpectedOutputContains: []string{"OK"},
		},
		{
			Name:                   "admin-node-compaction",
			Category:               CategoryAdmin,
			Command:                []string{"admin", "node-compaction"},
			Description:            "Show the platform's node-keyspace compaction policy",
			RequiresServer:         true,
			ExpectedOutputContains: []string{"Compaction"},
		},
		{
			Name:           "admin-update-node-compaction",
			Category:       CategoryAdmin,
			Command:        []string{"admin", "update-node-compaction"},
			Description:    "Set the platform's node-keyspace compaction policy",
			RequiresServer: true,
			RequiredArgs: []ArgDefinition{
				{Flag: "mode", Value: "off", Description: "Compaction mode: off, revision, period"},
			},
			OptionalArgs: []ArgDefinition{
				{Flag: "retain-revisions", Value: "", Description: "Revision mode: keep the last N revisions"},
				{Flag: "retain-period", Value: "", Description: "Period mode: keep history newer than this"},
			},
			ExpectedOutputContains: []string{"Compaction"},
		},
	}
}

// LeaseCommands returns distributed lease management commands.
func LeaseCommands() []CommandDefinition {
	return []CommandDefinition{
		{
			Name:           "lease-grant",
			Category:       CategoryLease,
			Command:        []string{"lease", "grant"},
			Description:    "Create a new lease with TTL",
			RequiresServer: true,
			RequiredArgs: []ArgDefinition{
				{Flag: "ttl", Value: "60", Description: "Time-to-live in seconds"},
			},
		},
		{
			Name:           "lease-revoke",
			Category:       CategoryLease,
			Command:        []string{"lease", "revoke"},
			Description:    "Revoke an existing lease",
			RequiresServer: true,
			RequiresSetup:  true,
			RequiredArgs: []ArgDefinition{
				{Value: "0", Description: "Lease ID", IsPositional: true},
			},
			ExpectedOutputContains: []string{"OK"},
		},
		{
			Name:           "lease-keep-alive",
			Category:       CategoryLease,
			Command:        []string{"lease", "keep-alive"},
			Description:    "Reset TTL on existing lease",
			RequiresServer: true,
			RequiresSetup:  true,
			RequiredArgs: []ArgDefinition{
				{Value: "0", Description: "Lease ID", IsPositional: true},
			},
		},
		{
			Name:           "lease-ttl",
			Category:       CategoryLease,
			Command:        []string{"lease", "ttl"},
			Description:    "Show lease state",
			RequiresServer: true,
			RequiresSetup:  true,
			RequiredArgs: []ArgDefinition{
				{Value: "0", Description: "Lease ID", IsPositional: true},
			},
		},
		{
			Name:           "lease-list",
			Category:       CategoryLease,
			Command:        []string{"lease", "list"},
			Description:    "List all active leases",
			RequiresServer: true,
		},
	}
}

// IndexCommands returns secondary index management commands.
func IndexCommands() []CommandDefinition {
	return []CommandDefinition{
		{
			Name:           "index-create",
			Category:       CategoryIndex,
			Command:        []string{"index", "create"},
			Description:    "Create a secondary index",
			RequiresServer: true,
			RequiredArgs: []ArgDefinition{
				{Value: "test-index", Description: "Index name", IsPositional: true},
				{Flag: "field", Value: "name", Description: "Field to index"},
			},
			OptionalArgs: []ArgDefinition{
				{Flag: "unique", Value: "", Description: "Enforce uniqueness"},
				{Flag: "sparse", Value: "", Description: "Skip null values"},
			},
			ExpectedOutputContains: []string{"OK"},
		},
		{
			Name:           "index-drop",
			Category:       CategoryIndex,
			Command:        []string{"index", "drop"},
			Description:    "Drop a secondary index",
			RequiresServer: true,
			RequiresSetup:  true,
			RequiredArgs: []ArgDefinition{
				{Value: "test-index", Description: "Index name", IsPositional: true},
			},
			ExpectedOutputContains: []string{"OK"},
		},
		{
			Name:           "index-query",
			Category:       CategoryIndex,
			Command:        []string{"index", "query"},
			Description:    "Query index for exact value",
			RequiresServer: true,
			RequiresSetup:  true,
			RequiredArgs: []ArgDefinition{
				{Value: "test-index", Description: "Index name", IsPositional: true},
				{Flag: "value", Value: "test", Description: "Exact value"},
			},
		},
		{
			Name:           "index-scan",
			Category:       CategoryIndex,
			Command:        []string{"index", "scan"},
			Description:    "Scan index over value range",
			RequiresServer: true,
			RequiresSetup:  true,
			RequiredArgs: []ArgDefinition{
				{Value: "test-index", Description: "Index name", IsPositional: true},
				{Flag: "start", Value: "a", Description: "Range start"},
				{Flag: "end", Value: "z", Description: "Range end"},
			},
		},
	}
}

// BatchCommands returns batch operation commands.
func BatchCommands() []CommandDefinition {
	return []CommandDefinition{
		{
			Name:              "batch-execute",
			Category:          CategoryBatch,
			Command:           []string{"batch", "execute"},
			Description:       "Execute batch operations from JSON",
			RequiresServer:    true,
			RequiresFileInput: true,
			RequiredArgs: []ArgDefinition{
				{Flag: "file", Value: "", Description: "JSON file path"},
			},
		},
	}
}

// TransactionCommands returns transaction operation commands.
func TransactionCommands() []CommandDefinition {
	return []CommandDefinition{
		{
			Name:              "tx-execute",
			Category:          CategoryTransaction,
			Command:           []string{"tx", "execute"},
			Description:       "Execute transaction from JSON",
			RequiresServer:    true,
			RequiresFileInput: true,
			RequiredArgs: []ArgDefinition{
				{Flag: "file", Value: "", Description: "JSON file path"},
			},
		},
	}
}

// WatchCommands returns watch operation commands.
func WatchCommands() []CommandDefinition {
	return []CommandDefinition{
		{
			Name:           "watch",
			Category:       CategoryWatch,
			Command:        []string{"watch"},
			Description:    "Watch for key changes",
			RequiresServer: true,
			IsStreaming:    true,
			RequiredArgs: []ArgDefinition{
				{Value: "test-key", Description: "Key", IsPositional: true},
			},
			OptionalArgs: []ArgDefinition{
				{Flag: "prefix", Value: "", Description: "Watch prefix"},
				{Flag: "start-revision", Value: "", Description: "Start revision"},
				{Flag: "prev-kv", Value: "", Description: "Include previous KV"},
			},
		},
	}
}

// TimeseriesCommands returns time-series operation commands.
func TimeseriesCommands() []CommandDefinition {
	return []CommandDefinition{
		{
			Name:             "ts-write",
			Category:         CategoryTimeseries,
			Command:          []string{"ts", "write"},
			Description:      "Write time-series data point",
			RequiresServer:   true,
			RequiresBuildTag: "qrdb_ts",
			RequiredArgs: []ArgDefinition{
				{Flag: "metric", Value: "cpu.usage", Description: "Metric name"},
				{Flag: "value", Value: "42.5", Description: "Data point value"},
			},
			OptionalArgs: []ArgDefinition{
				{Flag: "timestamp", Value: "", Description: "Unix timestamp ns"},
				{Flag: "tag", Value: "", Description: "Key=value tag"},
			},
		},
		{
			Name:              "ts-write-batch",
			Category:          CategoryTimeseries,
			Command:           []string{"ts", "write-batch"},
			Description:       "Batch write from JSON file",
			RequiresServer:    true,
			RequiresBuildTag:  "qrdb_ts",
			RequiresFileInput: true,
			RequiredArgs: []ArgDefinition{
				{Flag: "file", Value: "", Description: "JSON file path"},
			},
		},
		{
			Name:             "ts-query",
			Category:         CategoryTimeseries,
			Command:          []string{"ts", "query"},
			Description:      "Query time-series data",
			RequiresServer:   true,
			RequiresBuildTag: "qrdb_ts",
			RequiresSetup:    true,
			RequiredArgs: []ArgDefinition{
				{Flag: "metric", Value: "cpu.usage", Description: "Metric name"},
				{Flag: "start", Value: "0", Description: "Start timestamp ns"},
				{Flag: "end", Value: "9223372036854775807", Description: "End timestamp ns"},
			},
			OptionalArgs: []ArgDefinition{
				{Flag: "tag", Value: "", Description: "Filter tag"},
				{Flag: "limit", Value: "", Description: "Max results"},
				{Flag: "desc", Value: "", Description: "Descending order"},
			},
		},
		{
			Name:             "ts-aggregate",
			Category:         CategoryTimeseries,
			Command:          []string{"ts", "aggregate"},
			Description:      "Aggregate time-series data",
			RequiresServer:   true,
			RequiresBuildTag: "qrdb_ts",
			RequiresSetup:    true,
			RequiredArgs: []ArgDefinition{
				{Flag: "metric", Value: "cpu.usage", Description: "Metric name"},
				{Flag: "start", Value: "0", Description: "Start timestamp ns"},
				{Flag: "end", Value: "9223372036854775807", Description: "End timestamp ns"},
				{Flag: "function", Value: "avg", Description: "Aggregation function"},
			},
			OptionalArgs: []ArgDefinition{
				{Flag: "interval", Value: "", Description: "Interval ns"},
			},
		},
	}
}

// VectorCommands returns vector operation commands.
func VectorCommands() []CommandDefinition {
	return []CommandDefinition{
		{
			Name:             "vector-namespace-create",
			Category:         CategoryVector,
			Command:          []string{"vector", "namespace", "create"},
			Description:      "Create vector namespace",
			RequiresServer:   true,
			RequiresBuildTag: "qrdb_vector",
			RequiredArgs: []ArgDefinition{
				{Flag: "namespace", Value: "test-ns", Description: "Namespace name"},
				{Flag: "dimension", Value: "4", Description: "Vector dimension"},
			},
			OptionalArgs: []ArgDefinition{
				{Flag: "distance-metric", Value: "", Description: "Distance metric"},
				{Flag: "index-type", Value: "", Description: "Index type"},
			},
		},
		{
			Name:             "vector-namespace-delete",
			Category:         CategoryVector,
			Command:          []string{"vector", "namespace", "delete"},
			Description:      "Delete vector namespace",
			RequiresServer:   true,
			RequiresSetup:    true,
			RequiresBuildTag: "qrdb_vector",
			RequiredArgs: []ArgDefinition{
				{Flag: "namespace", Value: "test-ns", Description: "Namespace name"},
			},
		},
		{
			Name:             "vector-namespace-list",
			Category:         CategoryVector,
			Command:          []string{"vector", "namespace", "list"},
			Description:      "List vector namespaces",
			RequiresServer:   true,
			RequiresBuildTag: "qrdb_vector",
		},
		{
			Name:             "vector-upsert",
			Category:         CategoryVector,
			Command:          []string{"vector", "upsert"},
			Description:      "Insert or update vector",
			RequiresServer:   true,
			RequiresSetup:    true,
			RequiresBuildTag: "qrdb_vector",
		},
		{
			Name:             "vector-search",
			Category:         CategoryVector,
			Command:          []string{"vector", "search"},
			Description:      "Similarity search",
			RequiresServer:   true,
			RequiresSetup:    true,
			RequiresBuildTag: "qrdb_vector",
		},
	}
}

// DatabaseCommands returns database management commands.
func DatabaseCommands() []CommandDefinition {
	return []CommandDefinition{
		{
			Name:           "database-create",
			Category:       CategoryDatabase,
			Command:        []string{"database", "create"},
			Description:    "Create a new database",
			RequiresServer: true,
			RequiredArgs: []ArgDefinition{
				{Value: "test-db", Description: "Database name", IsPositional: true},
			},
			OptionalArgs: []ArgDefinition{
				{Flag: "engine", Value: "", Description: "Engine type"},
				{Flag: "backend", Value: "", Description: "Storage backend"},
			},
		},
		{
			Name:           "database-list",
			Category:       CategoryDatabase,
			Command:        []string{"database", "list"},
			Description:    "List all databases",
			RequiresServer: true,
		},
		{
			Name:           "database-info",
			Category:       CategoryDatabase,
			Command:        []string{"database", "info"},
			Description:    "Show database details",
			RequiresServer: true,
			RequiresSetup:  true,
			RequiredArgs: []ArgDefinition{
				{Value: "test-db", Description: "Database name", IsPositional: true},
			},
		},
		{
			Name:           "database-drop",
			Category:       CategoryDatabase,
			Command:        []string{"database", "drop"},
			Description:    "Drop a database",
			RequiresServer: true,
			RequiresSetup:  true,
			IsDestructive:  true,
			RequiredArgs: []ArgDefinition{
				{Value: "test-db", Description: "Database name", IsPositional: true},
			},
			ExpectedOutputContains: []string{"OK"},
		},
		{
			Name:           "database-update-compaction",
			Category:       CategoryDatabase,
			Command:        []string{"database", "update-compaction"},
			Description:    "Set a database's scheduled-compaction policy",
			RequiresServer: true,
			RequiresSetup:  true,
			RequiredArgs: []ArgDefinition{
				{Value: "test-db", Description: "Database name", IsPositional: true},
				{Flag: "mode", Value: "off", Description: "Compaction mode: off, revision, period"},
			},
			OptionalArgs: []ArgDefinition{
				{Flag: "retain-revisions", Value: "", Description: "Revision mode: keep the last N revisions"},
				{Flag: "retain-period", Value: "", Description: "Period mode: keep history newer than this"},
			},
		},
		{
			Name:           "database-update-etcd",
			Category:       CategoryDatabase,
			Command:        []string{"database", "update-etcd"},
			Description:    "Update etcd protocol config",
			RequiresServer: true,
			RequiresSetup:  true,
			RequiredArgs: []ArgDefinition{
				{Value: "test-db", Description: "Database name", IsPositional: true},
			},
			OptionalArgs: []ArgDefinition{
				{Flag: "enabled", Value: "", Description: "Enable etcd"},
				{Flag: "disabled", Value: "", Description: "Disable etcd"},
				{Flag: "listen-addr", Value: "", Description: "Listen address"},
			},
		},
	}
}

// UsersCommands returns user management commands.
func UsersCommands() []CommandDefinition {
	return []CommandDefinition{
		{
			Name:           "users-create",
			Category:       CategoryUsers,
			Command:        []string{"users", "create"},
			Description:    "Create a new user",
			RequiresServer: true,
			RequiredArgs: []ArgDefinition{
				{Flag: "username", Value: "test-user", Description: "Username"},
				// The canonical login key is required unless the omission is
				// declared; this definition exercises the ordinary shape.
				{Flag: "email", Value: "test-user@example.com", Description: "Canonical login key"},
			},
			OptionalArgs: []ArgDefinition{
				{Flag: "name", Value: "", Description: "Display name"},
				{Flag: "without-email-login-key", Value: "", Description: "Create a certificate-only identity with no email login key"},
				{Flag: "tenant-id", Value: "", Description: "Tenant ID"},
				{Flag: "roles", Value: "", Description: "Role IDs"},
			},
		},
		{
			Name:           "users-list",
			Category:       CategoryUsers,
			Command:        []string{"users", "list"},
			Description:    "List users",
			RequiresServer: true,
			OptionalArgs: []ArgDefinition{
				{Flag: "tenant-id", Value: "", Description: "Filter by tenant"},
				{Flag: "page", Value: "", Description: "Page number"},
				{Flag: "page-size", Value: "", Description: "Results per page"},
			},
		},
		{
			Name:           "users-get",
			Category:       CategoryUsers,
			Command:        []string{"users", "get"},
			Description:    "Get user by ID",
			RequiresServer: true,
			RequiresSetup:  true,
			RequiredArgs: []ArgDefinition{
				{Value: "", Description: "User ID", IsPositional: true},
			},
		},
		{
			Name:           "users-update",
			Category:       CategoryUsers,
			Command:        []string{"users", "update"},
			Description:    "Update user",
			RequiresServer: true,
			RequiresSetup:  true,
			RequiredArgs: []ArgDefinition{
				{Value: "", Description: "User ID", IsPositional: true},
			},
			OptionalArgs: []ArgDefinition{
				{Flag: "username", Value: "", Description: "New username"},
				{Flag: "email", Value: "", Description: "New canonical login key"},
				{Flag: "name", Value: "", Description: "New display name"},
				{Flag: "roles", Value: "", Description: "New role IDs"},
			},
		},
		{
			Name:           "users-delete",
			Category:       CategoryUsers,
			Command:        []string{"users", "delete"},
			Description:    "Delete user",
			RequiresServer: true,
			RequiresSetup:  true,
			RequiredArgs: []ArgDefinition{
				{Value: "", Description: "User ID", IsPositional: true},
			},
			ExpectedOutputContains: []string{"OK"},
		},
		{
			Name:           "users-enable",
			Category:       CategoryUsers,
			Command:        []string{"users", "enable"},
			Description:    "Enable user",
			RequiresServer: true,
			RequiresSetup:  true,
			RequiredArgs: []ArgDefinition{
				{Value: "", Description: "User ID", IsPositional: true},
			},
			ExpectedOutputContains: []string{"OK"},
		},
		{
			Name:           "users-disable",
			Category:       CategoryUsers,
			Command:        []string{"users", "disable"},
			Description:    "Disable user",
			RequiresServer: true,
			RequiresSetup:  true,
			RequiredArgs: []ArgDefinition{
				{Value: "", Description: "User ID", IsPositional: true},
			},
			ExpectedOutputContains: []string{"OK"},
		},
	}
}

// RolesCommands returns role management commands.
func RolesCommands() []CommandDefinition {
	return []CommandDefinition{
		{
			Name:           "roles-create",
			Category:       CategoryRoles,
			Command:        []string{"roles", "create"},
			Description:    "Create a new role",
			RequiresServer: true,
			RequiredArgs: []ArgDefinition{
				{Flag: "name", Value: "test-role", Description: "Role name"},
				{Flag: "permissions", Value: "kv:read,kv:write", Description: "Permissions"},
			},
			OptionalArgs: []ArgDefinition{
				{Flag: "tenant-id", Value: "", Description: "Tenant ID"},
			},
		},
		{
			Name:           "roles-list",
			Category:       CategoryRoles,
			Command:        []string{"roles", "list"},
			Description:    "List roles",
			RequiresServer: true,
		},
		{
			Name:           "roles-get",
			Category:       CategoryRoles,
			Command:        []string{"roles", "get"},
			Description:    "Get role by ID",
			RequiresServer: true,
			RequiresSetup:  true,
			RequiredArgs: []ArgDefinition{
				{Value: "", Description: "Role ID", IsPositional: true},
			},
		},
		{
			Name:           "roles-update",
			Category:       CategoryRoles,
			Command:        []string{"roles", "update"},
			Description:    "Update role",
			RequiresServer: true,
			RequiresSetup:  true,
			RequiredArgs: []ArgDefinition{
				{Value: "", Description: "Role ID", IsPositional: true},
			},
			OptionalArgs: []ArgDefinition{
				{Flag: "name", Value: "", Description: "New name"},
				{Flag: "permissions", Value: "", Description: "New permissions"},
			},
		},
		{
			Name:           "roles-delete",
			Category:       CategoryRoles,
			Command:        []string{"roles", "delete"},
			Description:    "Delete role",
			RequiresServer: true,
			RequiresSetup:  true,
			RequiredArgs: []ArgDefinition{
				{Value: "", Description: "Role ID", IsPositional: true},
			},
			ExpectedOutputContains: []string{"OK"},
		},
		{
			Name:           "roles-assign",
			Category:       CategoryRoles,
			Command:        []string{"roles", "assign"},
			Description:    "Assign role to user",
			RequiresServer: true,
			RequiresSetup:  true,
			RequiredArgs: []ArgDefinition{
				{Flag: "user", Value: "", Description: "User ID"},
				{Flag: "role", Value: "", Description: "Role ID"},
			},
			ExpectedOutputContains: []string{"OK"},
		},
		{
			Name:           "roles-unassign",
			Category:       CategoryRoles,
			Command:        []string{"roles", "unassign"},
			Description:    "Remove role from user",
			RequiresServer: true,
			RequiresSetup:  true,
			RequiredArgs: []ArgDefinition{
				{Flag: "user", Value: "", Description: "User ID"},
				{Flag: "role", Value: "", Description: "Role ID"},
			},
			ExpectedOutputContains: []string{"OK"},
		},
		{
			Name:           "roles-permissions",
			Category:       CategoryRoles,
			Command:        []string{"roles", "permissions"},
			Description:    "List available permissions",
			RequiresServer: true,
		},
	}
}

// CertsCommands returns certificate management commands.
func CertsCommands() []CommandDefinition {
	return []CommandDefinition{
		{
			Name:           "certs-generate",
			Category:       CategoryCerts,
			Command:        []string{"certs", "generate"},
			Description:    "Generate client certificate",
			RequiresServer: true,
			RequiredArgs: []ArgDefinition{
				{Flag: "username", Value: "test-user", Description: "Certificate subject"},
			},
			OptionalArgs: []ArgDefinition{
				{Flag: "tenant-id", Value: "", Description: "Tenant ID"},
				{Flag: "ttl", Value: "", Description: "Validity duration"},
				{Flag: "out", Value: "", Description: "Output directory"},
			},
		},
		{
			Name:           "certs-list",
			Category:       CategoryCerts,
			Command:        []string{"certs", "list"},
			Description:    "List issued certificates",
			RequiresServer: true,
			OptionalArgs: []ArgDefinition{
				{Flag: "tenant-id", Value: "", Description: "Filter by tenant"},
			},
		},
		{
			Name:           "certs-revoke",
			Category:       CategoryCerts,
			Command:        []string{"certs", "revoke"},
			Description:    "Revoke certificate",
			RequiresServer: true,
			RequiresSetup:  true,
			RequiredArgs: []ArgDefinition{
				{Flag: "serial", Value: "", Description: "Certificate serial"},
			},
			ExpectedOutputContains: []string{"OK"},
		},
		{
			Name:              "certs-export",
			Category:          CategoryCerts,
			Command:           []string{"certs", "export"},
			Description:       "Export certificate as PKCS#12",
			RequiresServer:    true,
			RequiresSetup:     true,
			RequiresFileInput: true,
			RequiredArgs: []ArgDefinition{
				{Flag: "cert-pem", Value: "", Description: "Cert PEM path"},
				{Flag: "key-pem", Value: "", Description: "Key PEM path"},
				{Flag: "passphrase", Value: "", Description: "PKCS#12 passphrase"},
				{Flag: "out", Value: "", Description: "Output file"},
			},
		},
	}
}

// CommandsByCategory returns commands filtered by category.
func CommandsByCategory(category CommandCategory) []CommandDefinition {
	var result []CommandDefinition
	for _, cmd := range AllCommands() {
		if cmd.Category == category {
			result = append(result, cmd)
		}
	}
	return result
}
