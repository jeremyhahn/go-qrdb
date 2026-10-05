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

package mcp

import (
	"context"
	"encoding/json"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// backupTools returns the MCP tool registrations for backup lifecycle
// management: create, list, get, delete, restore, and verify.
func backupTools(svc service.BackupServicer) []serverTool {
	return []serverTool{
		{Tool: toolBackupCreate(), Perm: permission.BackupCreate, Handler: handleBackupCreate(svc)},
		{Tool: toolBackupList(), Perm: permission.BackupList, Handler: handleBackupList(svc)},
		{Tool: toolBackupGet(), Perm: permission.BackupRead, Handler: handleBackupGet(svc)},
		{Tool: toolBackupDelete(), Perm: permission.BackupDelete, Handler: handleBackupDelete(svc)},
		{Tool: toolBackupRestore(), Perm: permission.BackupRestore, Handler: handleBackupRestore(svc)},
		{Tool: toolBackupVerify(), Perm: permission.BackupVerify, Handler: handleBackupVerify(svc)},
	}
}

// parseShardIDs reads an optional numeric array argument into a []uint64.
func parseShardIDs(req mcp.CallToolRequest, key string) []uint64 {
	raw := mcp.ParseArgument(req, key, nil)
	slice, ok := raw.([]interface{})
	if !ok || len(slice) == 0 {
		return nil
	}
	ids := make([]uint64, 0, len(slice))
	for _, v := range slice {
		if f, ok := v.(float64); ok {
			ids = append(ids, uint64(f))
		}
	}
	return ids
}

// toolBackupCreate defines the qrdb_backup_create tool schema.
func toolBackupCreate() mcp.Tool {
	return mcp.NewTool("qrdb_backup_create",
		mcp.WithDescription("Create a backup snapshot of the selected shards (or all shards)."),
		mcp.WithString("destination",
			mcp.Required(),
			mcp.Description("The backup destination identifier or path."),
		),
		mcp.WithString("compression",
			mcp.Description("Compression algorithm: 'none' (default), 'zstd', or 'snappy'."),
		),
		mcp.WithString("encryption",
			mcp.Description("Encryption algorithm: 'aes256' (default; requires key_ref) or 'none' (explicit plaintext override)."),
		),
		mcp.WithString("key_provider_id",
			mcp.Description("The registered keywrap.Provider to wrap this backup's DEK with. Defaults to 'software' when key_ref is set."),
		),
		mcp.WithString("key_ref",
			mcp.Description("The key reference the named provider resolves -- the bare name of a key file inside the server's configured key directory for 'software'. Required unless encryption is explicitly 'none'."),
		),
		mcp.WithArray("shard_ids",
			mcp.Description("Optional list of shard IDs to back up. Empty backs up all shards."),
		),
	)
}

// handleBackupCreate returns a tool handler that captures a new backup.
func handleBackupCreate(svc service.BackupServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		destination := mcp.ParseString(req, "destination", "")
		if destination == "" {
			return mcp.NewToolResultError("destination is required"), nil
		}

		info, err := svc.CreateBackup(ctx, service.BackupOptions{
			Destination:   destination,
			Compression:   service.CompressionType(mcp.ParseString(req, "compression", "")),
			Encryption:    service.EncryptionType(mcp.ParseString(req, "encryption", "")),
			KeyProviderID: mcp.ParseString(req, "key_provider_id", ""),
			KeyRef:        mcp.ParseString(req, "key_ref", ""),
			ShardIDs:      parseShardIDs(req, "shard_ids"),
		})
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_backup_create", Err: err}
		}

		return marshalToolResult("qrdb_backup_create", info)
	}
}

// toolBackupList defines the qrdb_backup_list tool schema.
func toolBackupList() mcp.Tool {
	return mcp.NewTool("qrdb_backup_list",
		mcp.WithDescription("List metadata for all stored backups."),
	)
}

// handleBackupList returns a tool handler that lists all backups.
func handleBackupList(svc service.BackupServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		backups, err := svc.ListBackups(ctx)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_backup_list", Err: err}
		}
		return marshalToolResult("qrdb_backup_list", backups)
	}
}

// toolBackupGet defines the qrdb_backup_get tool schema.
func toolBackupGet() mcp.Tool {
	return mcp.NewTool("qrdb_backup_get",
		mcp.WithDescription("Retrieve metadata for a single backup by ID."),
		mcp.WithString("id",
			mcp.Required(),
			mcp.Description("The backup ID to retrieve."),
		),
	)
}

// handleBackupGet returns a tool handler that retrieves a backup by ID.
func handleBackupGet(svc service.BackupServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id := mcp.ParseString(req, "id", "")
		if id == "" {
			return mcp.NewToolResultError("id is required"), nil
		}
		info, err := svc.GetBackup(ctx, id)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_backup_get", Err: err}
		}
		return marshalToolResult("qrdb_backup_get", info)
	}
}

// toolBackupDelete defines the qrdb_backup_delete tool schema.
func toolBackupDelete() mcp.Tool {
	return mcp.NewTool("qrdb_backup_delete",
		mcp.WithDescription("Delete a stored backup and its artifact."),
		mcp.WithString("id",
			mcp.Required(),
			mcp.Description("The backup ID to delete."),
		),
	)
}

// handleBackupDelete returns a tool handler that deletes a backup by ID.
func handleBackupDelete(svc service.BackupServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id := mcp.ParseString(req, "id", "")
		if id == "" {
			return mcp.NewToolResultError("id is required"), nil
		}
		if err := svc.DeleteBackup(ctx, id); err != nil {
			return nil, &ToolError{Tool: "qrdb_backup_delete", Err: err}
		}
		return mcp.NewToolResultText(`{"deleted":true}`), nil
	}
}

// toolBackupRestore defines the qrdb_backup_restore tool schema.
func toolBackupRestore() mcp.Tool {
	return mcp.NewTool("qrdb_backup_restore",
		mcp.WithDescription("Restore shards from a previously created backup."),
		mcp.WithString("backup_id",
			mcp.Required(),
			mcp.Description("The backup ID to restore from."),
		),
		mcp.WithArray("target_shards",
			mcp.Description("Optional list of shard IDs to restore. Empty restores all captured shards."),
		),
		mcp.WithBoolean("validate_first",
			mcp.Description("Verify backup integrity before restoring."),
		),
		mcp.WithString("decryption_key_ref",
			mcp.Description("Overrides the key reference stored in the backup's own envelope (e.g. the key file moved, or restore runs on a different node). Left unset, the stored reference is used."),
		),
	)
}

// handleBackupRestore returns a tool handler that restores from a backup.
func handleBackupRestore(svc service.BackupServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		backupID := mcp.ParseString(req, "backup_id", "")
		if backupID == "" {
			return mcp.NewToolResultError("backup_id is required"), nil
		}

		result, err := svc.RestoreBackup(ctx, service.RestoreOptions{
			BackupID:         backupID,
			TargetShards:     parseShardIDs(req, "target_shards"),
			ValidateFirst:    mcp.ParseBoolean(req, "validate_first", false),
			DecryptionKeyRef: mcp.ParseString(req, "decryption_key_ref", ""),
		})
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_backup_restore", Err: err}
		}
		return marshalToolResult("qrdb_backup_restore", result)
	}
}

// toolBackupVerify defines the qrdb_backup_verify tool schema.
func toolBackupVerify() mcp.Tool {
	return mcp.NewTool("qrdb_backup_verify",
		mcp.WithDescription("Verify the integrity (checksum) of a stored backup."),
		mcp.WithString("id",
			mcp.Required(),
			mcp.Description("The backup ID to verify."),
		),
	)
}

// handleBackupVerify returns a tool handler that verifies a backup's integrity.
func handleBackupVerify(svc service.BackupServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id := mcp.ParseString(req, "id", "")
		if id == "" {
			return mcp.NewToolResultError("id is required"), nil
		}
		result, err := svc.VerifyBackup(ctx, id)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_backup_verify", Err: err}
		}
		return marshalToolResult("qrdb_backup_verify", result)
	}
}

// marshalToolResult JSON-encodes v and returns it as an MCP text result,
// wrapping any marshal failure in a ToolError tagged with tool.
func marshalToolResult(tool string, v any) (*mcp.CallToolResult, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, &ToolError{Tool: tool, Err: err}
	}
	return mcp.NewToolResultText(string(data)), nil
}
