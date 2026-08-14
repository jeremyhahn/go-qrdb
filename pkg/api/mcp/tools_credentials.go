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
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// credentialTools returns the MCP tool registrations for credential
// management operations: non-versioned submission/strategy/deletion plus
// the versioned-rotation surface (list, rotate, retrieve-version,
// list-versions, gc-expired).
func credentialTools(svc service.CredentialServicer) []mcpserver.ServerTool {
	return []mcpserver.ServerTool{
		{Tool: toolCredentialSubmit(), Handler: handleCredentialSubmit(svc)},
		{Tool: toolCredentialStrategy(), Handler: handleCredentialStrategy(svc)},
		{Tool: toolCredentialDelete(), Handler: handleCredentialDelete(svc)},
		{Tool: toolCredentialList(), Handler: handleCredentialList(svc)},
		{Tool: toolCredentialRotate(), Handler: handleCredentialRotate(svc)},
		{Tool: toolCredentialRetrieveVersion(), Handler: handleCredentialRetrieveVersion(svc)},
		{Tool: toolCredentialListVersions(), Handler: handleCredentialListVersions(svc)},
		{Tool: toolCredentialGCExpired(), Handler: handleCredentialGCExpired(svc)},
		{Tool: toolCredentialStore(), Handler: handleCredentialStore(svc)},
		{Tool: toolCredentialRetrieve(), Handler: handleCredentialRetrieve(svc)},
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
		valueArg := mcp.ParseString(req, "value", "")
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
		value, err := svc.CredentialRetrieve(ctx, name)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_credential_retrieve", Err: err}
		}
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

		valueArg := mcp.ParseString(req, "value", "")
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
	return func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		strategy := svc.CredentialStrategy()

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

		newValueArg := mcp.ParseString(req, "new_value", "")
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

		value, err := svc.CredentialRetrieveVersion(ctx, name, version)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_credential_retrieve_version", Err: err}
		}

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
