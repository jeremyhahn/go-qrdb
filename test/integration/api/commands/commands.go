// Copyright 2026 Jeremy Hahn
// Apache 2.0 License

// Package commands provides centralized CLI command definitions for integration
// testing across all protocols.
package commands

// CommandCategory represents a category of commands.
type CommandCategory string

const (
	CategoryKV      CommandCategory = "kv"
	CategoryAdmin   CommandCategory = "admin"
	CategoryVersion CommandCategory = "version"
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
			RequiresSetup:  true, // Requires key to exist
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
			RequiresSetup:  true, // Requires key to exist
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
			RequiresSetup:  true, // Requires key to exist
			RequiredArgs: []ArgDefinition{
				{Value: "test-key", Description: "Key", IsPositional: true},
			},
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
			RequiresSetup:  true, // Requires valid shard
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
			RequiresSetup:  true, // Requires valid member
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
			RequiresSetup:  true, // Requires valid shard
			OptionalArgs: []ArgDefinition{
				{Flag: "shard-id", Value: "1", Description: "Shard ID"},
			},
		},
		{
			Name:           "admin-drain",
			Category:       CategoryAdmin,
			Command:        []string{"admin", "drain"},
			Description:    "Transition node into drain mode",
			RequiresServer: true,
			RequiresSetup:  true, // Destructive: moves shards off node
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
