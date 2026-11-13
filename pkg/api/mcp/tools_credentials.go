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
	"encoding/base64"
	"encoding/json"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/jeremyhahn/go-qrdb/pkg/credentials"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// credentialTools returns the MCP tool registrations for credential
// management operations: non-versioned submission/strategy/deletion plus
// the versioned-rotation surface (list, rotate, retrieve-version,
// list-versions, gc-expired).
func credentialTools(svc service.CredentialServicer) []serverTool {
	return []serverTool{
		{Tool: toolCredentialSubmit(), Perm: permission.CredentialsSubmit, Handler: handleCredentialSubmit(svc)},
		{Tool: toolCredentialStrategy(), Perm: permission.CredentialsStrategy, Handler: handleCredentialStrategy(svc)},
		{Tool: toolCredentialDelete(), Perm: permission.CredentialsDelete, Handler: handleCredentialDelete(svc)},
		{Tool: toolCredentialList(), Perm: permission.CredentialsList, Handler: handleCredentialList(svc)},
		{Tool: toolCredentialRotate(), Perm: permission.CredentialsRotate, Handler: handleCredentialRotate(svc)},
		{Tool: toolCredentialSetRotationPolicy(), Perm: permission.CredentialsRotate, Handler: handleCredentialSetRotationPolicy(svc)},
		{Tool: toolCredentialRotationPolicy(), Perm: permission.CredentialsRotate, Handler: handleCredentialRotationPolicy(svc)},
		{Tool: toolCredentialMigrationConflicts(), Perm: permission.CredentialsList, Handler: handleCredentialMigrationConflicts(svc)},
		{Tool: toolCredentialResolveMigrationConflict(), Perm: permission.CredentialsRotate, Handler: handleCredentialResolveMigrationConflict(svc)},
		{Tool: toolCredentialRetrieveVersion(), Perm: permission.CredentialsRetrieveVersion, Handler: handleCredentialRetrieveVersion(svc)},
		{Tool: toolCredentialListVersions(), Perm: permission.CredentialsListVersions, Handler: handleCredentialListVersions(svc)},
		{Tool: toolCredentialGCExpired(), Perm: permission.CredentialsGCExpired, Handler: handleCredentialGCExpired(svc)},
		{Tool: toolCredentialStore(), Perm: permission.CredentialsStore, Handler: handleCredentialStore(svc)},
		{Tool: toolCredentialRetrieve(), Perm: permission.CredentialsRetrieve, Handler: handleCredentialRetrieve(svc)},
	}
}

// toolCredentialStore defines the qrdb_credential_store tool schema.
func toolCredentialStore() mcp.Tool {
	return mcp.NewTool("qrdb_credential_store",
		mcp.WithDescription("Persist a credential value under the given name."),
		mcp.WithString("name", mcp.Required(), mcp.Description("The credential name.")),
		mcp.WithString("value", mcp.Required(), mcp.Description("The credential value to store, base64-encoded (RFC 4648). Encoding as base64 preserves arbitrary binary content byte-for-byte; UTF-8 text must also be base64-encoded.")),
	)
}

// handleCredentialStore returns a tool handler that stores a credential.
func handleCredentialStore(svc service.CredentialServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name := mcp.ParseString(req, "name", "")
		if name == "" {
			return mcp.NewToolResultError("name is required"), nil
		}
		valueArg, _, argErr := stringArg(req, "value")
		if argErr != nil {
			return mcp.NewToolResultError(argErr.Error()), nil
		}
		if valueArg == "" {
			return mcp.NewToolResultError("value is required"), nil
		}
		value, decodeErr := base64.StdEncoding.DecodeString(valueArg)
		if decodeErr != nil {
			return mcp.NewToolResultError((&ValueDecodeError{Field: "value", Err: decodeErr}).Error()), nil
		}
		if err := svc.CredentialStore(ctx, name, value); err != nil {
			return nil, &ToolError{Tool: "qrdb_credential_store", Err: err}
		}
		return mcp.NewToolResultText("credential stored"), nil
	}
}

// toolCredentialRetrieve defines the qrdb_credential_retrieve tool schema.
func toolCredentialRetrieve() mcp.Tool {
	return mcp.NewTool("qrdb_credential_retrieve",
		mcp.WithDescription("Retrieve the credential value for the given name. The returned value is base64-encoded (RFC 4648) to preserve arbitrary binary content byte-for-byte."),
		mcp.WithString("name", mcp.Required(), mcp.Description("The credential name.")),
	)
}

// handleCredentialRetrieve returns a tool handler that reads a credential
// value. The value is emitted base64-encoded so binary content round-trips
// byte-for-byte.
func handleCredentialRetrieve(svc service.CredentialServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name := mcp.ParseString(req, "name", "")
		if name == "" {
			return mcp.NewToolResultError("name is required"), nil
		}
		buf, err := svc.CredentialRetrieveBuffer(ctx, name)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_credential_retrieve", Err: err}
		}
		// The base64 tool result is the wire DTO: the one boundary where the
		// value leaves protected memory. The plain slice is wiped as soon as it
		// has been encoded.
		value, err := service.CredentialWireValue(buf)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_credential_retrieve", Err: err}
		}
		defer secure.Wipe(value)
		return mcp.NewToolResultText(base64.StdEncoding.EncodeToString(value)), nil
	}
}

// toolCredentialSubmit defines the qrdb_credential_submit tool schema.
func toolCredentialSubmit() mcp.Tool {
	return mcp.NewTool("qrdb_credential_submit",
		mcp.WithDescription("Submit a named credential, notifying any blocked retrievers for the manual strategy."),
		mcp.WithString("name",
			mcp.Required(),
			mcp.Description("The credential name."),
		),
		mcp.WithString("value",
			mcp.Required(),
			mcp.Description("The credential value, base64-encoded (RFC 4648). Encoding as base64 preserves arbitrary binary content byte-for-byte; UTF-8 text must also be base64-encoded."),
		),
	)
}

// handleCredentialSubmit returns a tool handler that delegates to
// svc.CredentialSubmit.
func handleCredentialSubmit(svc service.CredentialServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name := mcp.ParseString(req, "name", "")
		if name == "" {
			return mcp.NewToolResultError("name is required"), nil
		}

		valueArg, _, argErr := stringArg(req, "value")
		if argErr != nil {
			return mcp.NewToolResultError(argErr.Error()), nil
		}
		if valueArg == "" {
			return mcp.NewToolResultError("value is required"), nil
		}
		value, decodeErr := base64.StdEncoding.DecodeString(valueArg)
		if decodeErr != nil {
			return mcp.NewToolResultError((&ValueDecodeError{Field: "value", Err: decodeErr}).Error()), nil
		}

		if err := svc.CredentialSubmit(ctx, name, value); err != nil {
			return nil, &ToolError{Tool: "qrdb_credential_submit", Err: err}
		}

		return mcp.NewToolResultText("credential submitted"), nil
	}
}

// toolCredentialStrategy defines the qrdb_credential_strategy tool schema.
func toolCredentialStrategy() mcp.Tool {
	return mcp.NewTool("qrdb_credential_strategy",
		mcp.WithDescription("Return the active credential strategy name."),
	)
}

// handleCredentialStrategy returns a tool handler that delegates to
// svc.CredentialStrategy.
func handleCredentialStrategy(svc service.CredentialServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		strategy, err := svc.CredentialStrategy(ctx)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_credential_strategy", Err: err}
		}

		result := struct {
			Strategy string `json:"strategy"`
		}{
			Strategy: strategy,
		}

		data, jsonErr := json.Marshal(result)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_credential_strategy", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolCredentialDelete defines the qrdb_credential_delete tool schema.
func toolCredentialDelete() mcp.Tool {
	return mcp.NewTool("qrdb_credential_delete",
		mcp.WithDescription("Delete a named credential and all of its non-current historical versions."),
		mcp.WithString("name",
			mcp.Required(),
			mcp.Description("The credential name to delete."),
		),
	)
}

// handleCredentialDelete returns a tool handler that delegates to
// svc.CredentialDelete.
func handleCredentialDelete(svc service.CredentialServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name := mcp.ParseString(req, "name", "")
		if name == "" {
			return mcp.NewToolResultError("name is required"), nil
		}

		if err := svc.CredentialDelete(ctx, name); err != nil {
			return nil, &ToolError{Tool: "qrdb_credential_delete", Err: err}
		}

		return mcp.NewToolResultText("credential deleted"), nil
	}
}

// ---------------------------------------------------------------------------
// Versioned rotation surface
// ---------------------------------------------------------------------------

// toolCredentialList defines the qrdb_credential_list tool schema.
func toolCredentialList() mcp.Tool {
	return mcp.NewTool("qrdb_credential_list",
		mcp.WithDescription("Enumerate the names of every stored credential. Values are never returned."),
	)
}

// handleCredentialList returns a tool handler that delegates to
// svc.CredentialList. The response is encoded as
// {"names": ["name1", "name2", ...]} so the client can decode it into
// transport.CredentialListResponse.
func handleCredentialList(svc service.CredentialServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		names, err := svc.CredentialList(ctx)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_credential_list", Err: err}
		}
		if names == nil {
			names = []string{}
		}

		result := struct {
			Names []string `json:"names"`
		}{
			Names: names,
		}

		data, jsonErr := json.Marshal(result)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_credential_list", Err: jsonErr}
		}
		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolCredentialRotate defines the qrdb_credential_rotate tool schema.
func toolCredentialRotate() mcp.Tool {
	return mcp.NewTool("qrdb_credential_rotate",
		mcp.WithDescription("Atomically replace the current value of a credential, optionally keeping the previous version retrievable for a grace period."),
		mcp.WithString("name",
			mcp.Required(),
			mcp.Description("The credential name to rotate."),
		),
		mcp.WithString("new_value",
			mcp.Required(),
			mcp.Description("The new credential value, base64-encoded (RFC 4648). Encoding as base64 preserves arbitrary binary content byte-for-byte; UTF-8 text must also be base64-encoded."),
		),
		mcp.WithNumber("grace_period_seconds",
			mcp.Description("How long (in seconds) the previous value remains retrievable by version number. Zero means immediate wipe."),
		),
	)
}

// handleCredentialRotate returns a tool handler that delegates to
// svc.CredentialRotate. The response is encoded as
// {"name", "previous_version", "new_version", "grace_until_unix"}
// matching transport.CredentialRotateResponse.
func handleCredentialRotate(svc service.CredentialServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name := mcp.ParseString(req, "name", "")
		if name == "" {
			return mcp.NewToolResultError("name is required"), nil
		}

		newValueArg, _, argErr := stringArg(req, "new_value")
		if argErr != nil {
			return mcp.NewToolResultError(argErr.Error()), nil
		}
		if newValueArg == "" {
			return mcp.NewToolResultError("new_value is required"), nil
		}
		newValue, decodeErr := base64.StdEncoding.DecodeString(newValueArg)
		if decodeErr != nil {
			return mcp.NewToolResultError((&ValueDecodeError{Field: "new_value", Err: decodeErr}).Error()), nil
		}

		gracePeriodSeconds := mcp.ParseInt64(req, "grace_period_seconds", 0)
		if gracePeriodSeconds < 0 {
			return mcp.NewToolResultError("grace_period_seconds must be non-negative"), nil
		}

		res, err := svc.CredentialRotate(ctx, name, credentials.RotateOptions{
			NewValue:    newValue,
			GracePeriod: time.Duration(gracePeriodSeconds) * time.Second,
		})
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_credential_rotate", Err: err}
		}

		result := struct {
			Name            string `json:"name"`
			PreviousVersion uint64 `json:"previous_version"`
			NewVersion      uint64 `json:"new_version"`
			GraceUntilUnix  int64  `json:"grace_until_unix,omitempty"`
		}{
			Name:            res.Name,
			PreviousVersion: res.PreviousVersion,
			NewVersion:      res.NewVersion,
		}
		if !res.GraceUntil.IsZero() {
			result.GraceUntilUnix = res.GraceUntil.Unix()
		}

		data, jsonErr := json.Marshal(result)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_credential_rotate", Err: jsonErr}
		}
		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolCredentialSetRotationPolicy defines the qrdb_credential_set_rotation_policy tool.
func toolCredentialSetRotationPolicy() mcp.Tool {
	return mcp.NewTool("qrdb_credential_set_rotation_policy",
		mcp.WithDescription("Set a credential's scheduled rotation policy. A disabled policy stops scheduled rotation."),
		mcp.WithString("name",
			mcp.Required(),
			mcp.Description("The credential name."),
		),
		mcp.WithBoolean("enabled",
			mcp.Description("Whether scheduled rotation is on."),
		),
		mcp.WithString("interval",
			mcp.Description("The time between rotations as a Go duration string, such as 720h. Required when enabled."),
		),
		mcp.WithNumber("retain_versions",
			mcp.Description("How many versions, the current one included, survive a scheduled rotation. At least 1 when enabled."),
		),
	)
}

// handleCredentialSetRotationPolicy returns the handler for the
// qrdb_credential_set_rotation_policy tool.
func handleCredentialSetRotationPolicy(svc service.CredentialServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name := mcp.ParseString(req, "name", "")
		if name == "" {
			return mcp.NewToolResultError("name is required"), nil
		}

		policy, err := service.CredentialRotationPolicyFromWire(
			mcp.ParseBoolean(req, "enabled", false),
			mcp.ParseString(req, "interval", ""),
			mcp.ParseInt(req, "retain_versions", 0),
		)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_credential_set_rotation_policy", Err: err}
		}
		if err := svc.CredentialSetRotationPolicy(ctx, name, policy); err != nil {
			return nil, &ToolError{Tool: "qrdb_credential_set_rotation_policy", Err: err}
		}

		return mcp.NewToolResultText("rotation policy set"), nil
	}
}

// toolCredentialRotationPolicy defines the qrdb_credential_rotation_policy tool.
func toolCredentialRotationPolicy() mcp.Tool {
	return mcp.NewTool("qrdb_credential_rotation_policy",
		mcp.WithDescription("Return a credential's scheduled rotation policy and the RFC 3339 time of its next scheduled rotation."),
		mcp.WithString("name",
			mcp.Required(),
			mcp.Description("The credential name."),
		),
	)
}

// handleCredentialRotationPolicy returns the handler for the
// qrdb_credential_rotation_policy tool.
func handleCredentialRotationPolicy(svc service.CredentialServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name := mcp.ParseString(req, "name", "")
		if name == "" {
			return mcp.NewToolResultError("name is required"), nil
		}

		status, err := svc.CredentialRotationPolicy(ctx, name)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_credential_rotation_policy", Err: err}
		}

		data, jsonErr := json.Marshal(struct {
			Name           string `json:"name"`
			Enabled        bool   `json:"enabled"`
			Interval       string `json:"interval"`
			RetainVersions int    `json:"retain_versions"`
			NextRotation   string `json:"next_rotation,omitempty"`
		}{
			Name:           name,
			Enabled:        status.Policy.Enabled,
			Interval:       status.WireInterval(),
			RetainVersions: status.Policy.RetainVersions,
			NextRotation:   status.WireNextRotation(),
		})
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_credential_rotation_policy", Err: jsonErr}
		}
		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolCredentialMigrationConflicts defines the
// qrdb_credential_migration_conflicts tool.
func toolCredentialMigrationConflicts() mcp.Tool {
	return mcp.NewTool("qrdb_credential_migration_conflicts",
		mcp.WithDescription("List the credentials whose node-local copy in the old credentials/ layout differs from the replicated record, and which parts differ. Values are never returned."),
	)
}

// migrationConflictJSON is one entry of the
// qrdb_credential_migration_conflicts result.
type migrationConflictJSON struct {
	Name     string `json:"name"`
	Value    bool   `json:"value"`
	Versions bool   `json:"versions"`
	Policy   bool   `json:"policy"`
}

// handleCredentialMigrationConflicts lists the caller's store's migration
// conflicts.
func handleCredentialMigrationConflicts(svc service.CredentialServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		conflicts, err := svc.CredentialMigrationConflicts(ctx)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_credential_migration_conflicts", Err: err}
		}

		result := struct {
			Conflicts []migrationConflictJSON `json:"conflicts"`
		}{
			Conflicts: make([]migrationConflictJSON, 0, len(conflicts)),
		}
		for _, c := range conflicts {
			result.Conflicts = append(result.Conflicts, migrationConflictJSON{
				Name: c.Name, Value: c.Value, Versions: c.Versions, Policy: c.Policy,
			})
		}

		data, jsonErr := json.Marshal(result)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_credential_migration_conflicts", Err: jsonErr}
		}
		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolCredentialResolveMigrationConflict defines the
// qrdb_credential_resolve_migration_conflict tool.
func toolCredentialResolveMigrationConflict() mcp.Tool {
	return mcp.NewTool("qrdb_credential_resolve_migration_conflict",
		mcp.WithDescription("Settle one credential migration conflict: import makes the node-local current value a new version of the replicated record, discard keeps the replicated record. Either way the node-local records are deleted."),
		mcp.WithString("name",
			mcp.Required(),
			mcp.Description("The credential name."),
		),
		mcp.WithString("resolution",
			mcp.Required(),
			mcp.Enum(string(credentials.MigrationImport), string(credentials.MigrationDiscard)),
			mcp.Description("import or discard."),
		),
	)
}

// handleCredentialResolveMigrationConflict settles one migration conflict.
func handleCredentialResolveMigrationConflict(svc service.CredentialServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name := mcp.ParseString(req, "name", "")
		if name == "" {
			return mcp.NewToolResultError("name is required"), nil
		}

		how := credentials.MigrationResolution(mcp.ParseString(req, "resolution", ""))
		if err := svc.CredentialResolveMigrationConflict(ctx, name, how); err != nil {
			return nil, &ToolError{Tool: "qrdb_credential_resolve_migration_conflict", Err: err}
		}

		return mcp.NewToolResultText("migration conflict resolved"), nil
	}
}

// toolCredentialRetrieveVersion defines the qrdb_credential_retrieve_version
// tool schema.
func toolCredentialRetrieveVersion() mcp.Tool {
	return mcp.NewTool("qrdb_credential_retrieve_version",
		mcp.WithDescription("Return the stored credential value for a specific version. Pass version=0 for the current version. The value field in the JSON response is base64-encoded (RFC 4648) to preserve arbitrary binary content byte-for-byte."),
		mcp.WithString("name",
			mcp.Required(),
			mcp.Description("The credential name to retrieve."),
		),
		mcp.WithNumber("version",
			mcp.Description("The version number to retrieve. Use 0 for the current version."),
		),
	)
}

// handleCredentialRetrieveVersion returns a tool handler that delegates to
// svc.CredentialRetrieveVersion. The response is encoded as
// {"value": "..."} matching transport.CredentialRetrieveVersionResponse.
func handleCredentialRetrieveVersion(svc service.CredentialServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name := mcp.ParseString(req, "name", "")
		if name == "" {
			return mcp.NewToolResultError("name is required"), nil
		}

		version := mcp.ParseUInt64(req, "version", 0)

		buf, err := svc.CredentialRetrieveVersionBuffer(ctx, name, version)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_credential_retrieve_version", Err: err}
		}
		// The JSON tool result is the wire DTO: the one boundary where the value
		// leaves protected memory. The plain slice is wiped as soon as it has
		// been encoded.
		value, err := service.CredentialWireValue(buf)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_credential_retrieve_version", Err: err}
		}
		defer secure.Wipe(value)

		// encoding/json renders a []byte field as base64 (RFC 4648), so binary
		// credential content round-trips byte-for-byte into the client's
		// transport.CredentialRetrieveVersionResponse (also a []byte field).
		result := struct {
			Value []byte `json:"value"`
		}{
			Value: value,
		}

		data, jsonErr := json.Marshal(result)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_credential_retrieve_version", Err: jsonErr}
		}
		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolCredentialListVersions defines the qrdb_credential_list_versions tool
// schema.
func toolCredentialListVersions() mcp.Tool {
	return mcp.NewTool("qrdb_credential_list_versions",
		mcp.WithDescription("Return the version history (current plus non-expired grace) for a credential. Values are never returned."),
		mcp.WithString("name",
			mcp.Required(),
			mcp.Description("The credential name whose version history is being requested."),
		),
	)
}

// handleCredentialListVersions returns a tool handler that delegates to
// svc.CredentialListVersions. Each entry's Value field is intentionally
// dropped — callers must use qrdb_credential_retrieve_version to read the
// bytes for a specific version. The response is encoded as
// {"name", "versions": [{"version", "is_current", "created_at_unix",
// "grace_until_unix"}, ...]} matching
// transport.CredentialListVersionsResponse.
func handleCredentialListVersions(svc service.CredentialServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name := mcp.ParseString(req, "name", "")
		if name == "" {
			return mcp.NewToolResultError("name is required"), nil
		}

		versions, err := svc.CredentialListVersions(ctx, name)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_credential_list_versions", Err: err}
		}

		type versionEntry struct {
			Version        uint64 `json:"version"`
			IsCurrent      bool   `json:"is_current"`
			CreatedAtUnix  int64  `json:"created_at_unix,omitempty"`
			GraceUntilUnix int64  `json:"grace_until_unix,omitempty"`
		}

		out := make([]versionEntry, 0, len(versions))
		for _, v := range versions {
			entry := versionEntry{
				Version:   v.Version,
				IsCurrent: v.IsCurrent,
			}
			if !v.CreatedAt.IsZero() {
				entry.CreatedAtUnix = v.CreatedAt.Unix()
			}
			if !v.GraceUntil.IsZero() {
				entry.GraceUntilUnix = v.GraceUntil.Unix()
			}
			out = append(out, entry)
		}

		result := struct {
			Name     string         `json:"name"`
			Versions []versionEntry `json:"versions"`
		}{
			Name:     name,
			Versions: out,
		}

		data, jsonErr := json.Marshal(result)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_credential_list_versions", Err: jsonErr}
		}
		return mcp.NewToolResultText(string(data)), nil
	}
}

// toolCredentialGCExpired defines the qrdb_credential_gc_expired tool
// schema.
func toolCredentialGCExpired() mcp.Tool {
	return mcp.NewTool("qrdb_credential_gc_expired",
		mcp.WithDescription("Reap historical versions of a credential whose grace period has elapsed. Returns the count reaped."),
		mcp.WithString("name",
			mcp.Required(),
			mcp.Description("The credential name whose expired grace versions should be reaped."),
		),
	)
}

// handleCredentialGCExpired returns a tool handler that delegates to
// svc.CredentialGCExpired. The response is encoded as {"reaped": N}
// matching transport.CredentialGCResponse.
func handleCredentialGCExpired(svc service.CredentialServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name := mcp.ParseString(req, "name", "")
		if name == "" {
			return mcp.NewToolResultError("name is required"), nil
		}

		reaped, err := svc.CredentialGCExpired(ctx, name)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_credential_gc_expired", Err: err}
		}

		result := struct {
			Reaped int `json:"reaped"`
		}{
			Reaped: reaped,
		}

		data, jsonErr := json.Marshal(result)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_credential_gc_expired", Err: jsonErr}
		}
		return mcp.NewToolResultText(string(data)), nil
	}
}
