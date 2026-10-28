// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
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
	"strconv"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// userAuthFactorTools returns the MCP tool registrations for the M14
// user-side auth factor surface: add, list, get, remove, rotate, relabel,
// login. The userSvc is optional and enriches the login result with the
// user's username, tenant, and roles; a nil userSvc leaves those empty.
func userAuthFactorTools(svc service.UserAuthFactorServicer, userSvc user.UserServicer) []serverTool {
	return []serverTool{
		{Tool: toolUserAuthFactorAdd(), Perm: permission.UserAuthFactorsAdd, Handler: handleUserAuthFactorAddTool(svc)},
		{Tool: toolUserAuthFactorList(), Perm: permission.UserAuthFactorsList, Handler: handleUserAuthFactorListTool(svc)},
		{Tool: toolUserAuthFactorGet(), Perm: permission.UserAuthFactorsGet, Handler: handleUserAuthFactorGetTool(svc)},
		{Tool: toolUserAuthFactorRemove(), Perm: permission.UserAuthFactorsRemove, Handler: handleUserAuthFactorRemoveTool(svc)},
		{Tool: toolUserAuthFactorDelete(), Perm: permission.UserAuthFactorsAdd, Handler: handleUserAuthFactorDeleteTool(svc)},
		{Tool: toolUserAuthFactorRotate(), Perm: permission.UserAuthFactorsRotate, Handler: handleUserAuthFactorRotateTool(svc)},
		{Tool: toolUserAuthFactorRelabel(), Perm: permission.UserAuthFactorsRelabel, Handler: handleUserAuthFactorRelabelTool(svc)},
		{Tool: toolUserAuthFactorLogin(), Perm: permission.UserAuthFactorsLogin, Handler: handleUserAuthFactorLoginTool(svc, userSvc)},
	}
}

// ---------------------------------------------------------------------------
// Tool schemas
// ---------------------------------------------------------------------------

func toolUserAuthFactorAdd() mcp.Tool {
	return mcp.NewTool("qrdb_user_authfactor_add",
		mcp.WithDescription("Register a user authentication factor (mtls, fido2, or piv)."),
		mcp.WithString("user_id", mcp.Required(), mcp.Description("Numeric user ID (uint64).")),
		mcp.WithString("type", mcp.Required(), mcp.Description("Factor type: mtls, fido2, or piv.")),
		mcp.WithString("label", mcp.Description("Human-readable display label.")),
		mcp.WithString("credential_b64", mcp.Required(), mcp.Description("Base64-encoded canonical lookup-key bytes.")),
		mcp.WithString("spki_credential_b64", mcp.Description("Base64-encoded SHA-256 of the certificate SubjectPublicKeyInfo (renewal-stable secondary lookup key, mtls/piv only).")),
		mcp.WithString("metadata_json", mcp.Description("JSON-encoded type-specific metadata.")),
		mcp.WithString("created_by", mcp.Description("Provenance: self, admin, or ceremony.")),
		mcp.WithBoolean("if_no_active_type", mcp.Description("Add only if the user has no active factor of this type; otherwise refuse with FACTOR_CONDITION_FAILED.")),
		mcp.WithString("expected_factor_set_version", mcp.Description("Add only if the user's factor set is at this version (uint64, as qrdb_user_authfactor_list reports it; 0 until the set first changes); otherwise refuse with FACTOR_CONDITION_FAILED.")),
	)
}

func toolUserAuthFactorList() mcp.Tool {
	return mcp.NewTool("qrdb_user_authfactor_list",
		mcp.WithDescription("List all factors (active and revoked) belonging to a user."),
		mcp.WithString("user_id", mcp.Required(), mcp.Description("Numeric user ID (uint64).")),
	)
}

func toolUserAuthFactorGet() mcp.Tool {
	return mcp.NewTool("qrdb_user_authfactor_get",
		mcp.WithDescription("Retrieve a single factor by ID."),
		mcp.WithString("factor_id", mcp.Required(), mcp.Description("Stable factor identifier.")),
	)
}

func toolUserAuthFactorRemove() mcp.Tool {
	return mcp.NewTool("qrdb_user_authfactor_remove",
		mcp.WithDescription("Soft-delete a factor by setting its RevokedAt. Use force=true to remove the user's last factor (admin override)."),
		mcp.WithString("factor_id", mcp.Required(), mcp.Description("Factor ID.")),
		mcp.WithBoolean("force", mcp.Description("Bypass the last-active-factor guard.")),
	)
}

func toolUserAuthFactorDelete() mcp.Tool {
	return mcp.NewTool("qrdb_user_authfactor_delete",
		mcp.WithDescription("Hard-delete a factor, dropping its credential and SPKI index entries so the credential can be bound again (rolls back an add). Refuses a revoked factor. Use force=true to delete the user's last factor."),
		mcp.WithString("factor_id", mcp.Required(), mcp.Description("Factor ID.")),
		mcp.WithBoolean("force", mcp.Description("Bypass the last-active-factor guard.")),
	)
}

func toolUserAuthFactorRotate() mcp.Tool {
	return mcp.NewTool("qrdb_user_authfactor_rotate",
		mcp.WithDescription("Atomically replace an old factor with a new one in a single transaction."),
		mcp.WithString("old_factor_id", mcp.Required(), mcp.Description("Existing factor to revoke.")),
		mcp.WithString("type", mcp.Required(), mcp.Description("New factor type.")),
		mcp.WithString("label", mcp.Description("New factor label.")),
		mcp.WithString("credential_b64", mcp.Required(), mcp.Description("Base64-encoded new credential bytes.")),
		mcp.WithString("spki_credential_b64", mcp.Description("Base64-encoded SHA-256 of the certificate SubjectPublicKeyInfo (renewal-stable secondary lookup key, mtls/piv only).")),
		mcp.WithString("metadata_json", mcp.Description("JSON-encoded new metadata.")),
	)
}

func toolUserAuthFactorRelabel() mcp.Tool {
	return mcp.NewTool("qrdb_user_authfactor_relabel",
		mcp.WithDescription("Update the human-readable label on a factor."),
		mcp.WithString("factor_id", mcp.Required(), mcp.Description("Factor ID.")),
		mcp.WithString("label", mcp.Required(), mcp.Description("New label.")),
	)
}

func toolUserAuthFactorLogin() mcp.Tool {
	return mcp.NewTool("qrdb_user_authfactor_login",
		mcp.WithDescription("Resolve an inbound (type, lookup-key) tuple to a user and factor. Hardware verification must be performed by the caller."),
		mcp.WithString("type", mcp.Required(), mcp.Description("Factor type: mtls, fido2, or piv.")),
		mcp.WithString("lookup_key_b64", mcp.Required(), mcp.Description("Base64-encoded canonical lookup-key bytes.")),
	)
}

// ---------------------------------------------------------------------------
// Tool handlers
// ---------------------------------------------------------------------------

func handleUserAuthFactorAddTool(svc service.UserAuthFactorServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		userID, err := parseUint64(mcp.ParseString(req, "user_id", ""))
		if err != nil {
			return mcp.NewToolResultError("user_id must be a uint64: " + err.Error()), nil
		}
		typ := mcp.ParseString(req, "type", "")
		credB64, _, argErr := stringArg(req, "credential_b64")
		if argErr != nil {
			return mcp.NewToolResultError(argErr.Error()), nil
		}
		if typ == "" || credB64 == "" {
			return mcp.NewToolResultError("type and credential_b64 are required"), nil
		}
		cred, err := base64.StdEncoding.DecodeString(credB64)
		if err != nil {
			return mcp.NewToolResultError("credential_b64 must be base64: " + err.Error()), nil
		}
		var metadata map[string]string
		if mj := mcp.ParseString(req, "metadata_json", ""); mj != "" {
			if err := json.Unmarshal([]byte(mj), &metadata); err != nil {
				return mcp.NewToolResultError("metadata_json must be valid JSON: " + err.Error()), nil
			}
		}
		spkiCred, perr := parseOptionalB64(req, "spki_credential_b64")
		if perr != nil {
			return mcp.NewToolResultError(perr.Error()), nil
		}
		var expectedVersion *uint64
		if ev := mcp.ParseString(req, "expected_factor_set_version", ""); ev != "" {
			v, verr := parseUint64(ev)
			if verr != nil {
				return mcp.NewToolResultError("expected_factor_set_version must be a uint64: " + verr.Error()), nil
			}
			expectedVersion = &v
		}
		cond := auth.FactorAddConditionOf(mcp.ParseBoolean(req, "if_no_active_type", false), expectedVersion)
		factor := &auth.AuthFactor{
			UserID:         userID,
			Type:           auth.AuthFactorType(typ),
			Label:          mcp.ParseString(req, "label", ""),
			Credential:     cred,
			SPKICredential: spkiCred,
			Metadata:       metadata,
			CreatedBy:      mcp.ParseString(req, "created_by", ""),
		}
		version, err := service.AddAuthFactor(ctx, svc, userID, factor, cond)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_user_authfactor_add", Err: err}
		}
		body, _ := json.Marshal(map[string]any{"factor": factorToMCPMap(factor), "factor_set_version": version})
		return mcp.NewToolResultText(string(body)), nil
	}
}

func handleUserAuthFactorListTool(svc service.UserAuthFactorServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		userID, err := parseUint64(mcp.ParseString(req, "user_id", ""))
		if err != nil {
			return mcp.NewToolResultError("user_id must be a uint64: " + err.Error()), nil
		}
		factors, version, err := service.ListAuthFactors(ctx, svc, userID)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_user_authfactor_list", Err: err}
		}
		out := make([]map[string]any, len(factors))
		for i, f := range factors {
			out[i] = factorToMCPMap(f)
		}
		body, _ := json.Marshal(map[string]any{"factors": out, "total": len(out), "factor_set_version": version})
		return mcp.NewToolResultText(string(body)), nil
	}
}

func handleUserAuthFactorGetTool(svc service.UserAuthFactorServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		factorID := mcp.ParseString(req, "factor_id", "")
		if factorID == "" {
			return mcp.NewToolResultError("factor_id is required"), nil
		}
		factor, err := svc.Get(ctx, factorID)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_user_authfactor_get", Err: err}
		}
		return userAuthFactorJSONResult(factor)
	}
}

func handleUserAuthFactorRemoveTool(svc service.UserAuthFactorServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		factorID := mcp.ParseString(req, "factor_id", "")
		if factorID == "" {
			return mcp.NewToolResultError("factor_id is required"), nil
		}
		force := mcp.ParseBoolean(req, "force", false)
		if err := svc.Remove(ctx, factorID, force); err != nil {
			return nil, &ToolError{Tool: "qrdb_user_authfactor_remove", Err: err}
		}
		body, _ := json.Marshal(map[string]string{"status": "revoked", "factor_id": factorID})
		return mcp.NewToolResultText(string(body)), nil
	}
}

func handleUserAuthFactorDeleteTool(svc service.UserAuthFactorServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		factorID := mcp.ParseString(req, "factor_id", "")
		if factorID == "" {
			return mcp.NewToolResultError("factor_id is required"), nil
		}
		force := mcp.ParseBoolean(req, "force", false)
		if err := svc.Delete(ctx, factorID, force); err != nil {
			return nil, &ToolError{Tool: "qrdb_user_authfactor_delete", Err: err}
		}
		body, _ := json.Marshal(map[string]string{"status": "deleted", "factor_id": factorID})
		return mcp.NewToolResultText(string(body)), nil
	}
}

func handleUserAuthFactorRotateTool(svc service.UserAuthFactorServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		oldID := mcp.ParseString(req, "old_factor_id", "")
		typ := mcp.ParseString(req, "type", "")
		credB64, _, argErr := stringArg(req, "credential_b64")
		if argErr != nil {
			return mcp.NewToolResultError(argErr.Error()), nil
		}
		if oldID == "" || typ == "" || credB64 == "" {
			return mcp.NewToolResultError("old_factor_id, type, and credential_b64 are required"), nil
		}
		cred, err := base64.StdEncoding.DecodeString(credB64)
		if err != nil {
			return mcp.NewToolResultError("credential_b64 must be base64: " + err.Error()), nil
		}
		var metadata map[string]string
		if mj := mcp.ParseString(req, "metadata_json", ""); mj != "" {
			if err := json.Unmarshal([]byte(mj), &metadata); err != nil {
				return mcp.NewToolResultError("metadata_json must be valid JSON: " + err.Error()), nil
			}
		}
		spkiCred, perr := parseOptionalB64(req, "spki_credential_b64")
		if perr != nil {
			return mcp.NewToolResultError(perr.Error()), nil
		}
		newFactor := &auth.AuthFactor{
			Type:           auth.AuthFactorType(typ),
			Label:          mcp.ParseString(req, "label", ""),
			Credential:     cred,
			SPKICredential: spkiCred,
			Metadata:       metadata,
		}
		if err := svc.Rotate(ctx, oldID, newFactor); err != nil {
			return nil, &ToolError{Tool: "qrdb_user_authfactor_rotate", Err: err}
		}
		return userAuthFactorJSONResult(newFactor)
	}
}

func handleUserAuthFactorRelabelTool(svc service.UserAuthFactorServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		factorID := mcp.ParseString(req, "factor_id", "")
		label := mcp.ParseString(req, "label", "")
		if factorID == "" || label == "" {
			return mcp.NewToolResultError("factor_id and label are required"), nil
		}
		if err := svc.Relabel(ctx, factorID, label); err != nil {
			return nil, &ToolError{Tool: "qrdb_user_authfactor_relabel", Err: err}
		}
		f, err := svc.Get(ctx, factorID)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_user_authfactor_relabel", Err: err}
		}
		return userAuthFactorJSONResult(f)
	}
}

func handleUserAuthFactorLoginTool(svc service.UserAuthFactorServicer, userSvc user.UserServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		typ := mcp.ParseString(req, "type", "")
		keyB64, _, argErr := stringArg(req, "lookup_key_b64")
		if argErr != nil {
			return mcp.NewToolResultError(argErr.Error()), nil
		}
		if typ == "" || keyB64 == "" {
			return mcp.NewToolResultError("type and lookup_key_b64 are required"), nil
		}
		key, err := base64.StdEncoding.DecodeString(keyB64)
		if err != nil {
			return mcp.NewToolResultError("lookup_key_b64 must be base64: " + err.Error()), nil
		}
		factor, err := svc.LookupByCredentialKey(ctx, auth.AuthFactorType(typ), key)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_user_authfactor_login", Err: err}
		}
		result := map[string]any{
			"user_id": factor.UserID,
			"active":  true,
			"factor":  factorToMCPMap(factor),
		}
		// Enrich from the user service, when wired. The factor login yields
		// only the user ID; resolve the full record so username, tenant, and
		// roles reach the client. Best-effort: a lookup failure leaves the
		// base identifiers intact rather than failing an authenticated login.
		if userSvc != nil && factor.UserID != 0 {
			if u, uErr := userSvc.GetUser(ctx, factor.UserID); uErr == nil && u != nil {
				result["username"] = u.Username
				result["tenant_id"] = u.TenantID
				result["roles"] = u.RoleIDs
				result["active"] = u.CanAuthenticate()
			}
		}
		body, _ := json.Marshal(result)
		return mcp.NewToolResultText(string(body)), nil
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// factorToMCPMap projects an AuthFactor into a JSON-friendly map.
func factorToMCPMap(f *auth.AuthFactor) map[string]any {
	if f == nil {
		return nil
	}
	out := map[string]any{
		"id":            f.ID,
		"user_id":       f.UserID,
		"type":          string(f.Type),
		"label":         f.Label,
		"credential":    base64.StdEncoding.EncodeToString(f.Credential),
		"metadata":      f.Metadata,
		"registered_at": f.RegisteredAt.Format(time.RFC3339),
		"created_by":    f.CreatedBy,
	}
	if len(f.SPKICredential) > 0 {
		out["spki_credential"] = base64.StdEncoding.EncodeToString(f.SPKICredential)
	}
	if f.LastUsedAt != nil {
		out["last_used_at"] = f.LastUsedAt.Format(time.RFC3339)
	}
	if f.RevokedAt != nil {
		out["revoked_at"] = f.RevokedAt.Format(time.RFC3339)
	}
	return out
}

// userAuthFactorJSONResult marshals a single factor into a tool text result.
func userAuthFactorJSONResult(f *auth.AuthFactor) (*mcp.CallToolResult, error) {
	body, _ := json.Marshal(map[string]any{"factor": factorToMCPMap(f)})
	return mcp.NewToolResultText(string(body)), nil
}

// parseUint64 parses a decimal uint64.
func parseUint64(s string) (uint64, error) {
	return strconv.ParseUint(s, 10, 64)
}

// parseOptionalB64 decodes an optional base64-encoded string tool argument.
// Returns (nil, nil) when the argument is absent or empty.
func parseOptionalB64(req mcp.CallToolRequest, name string) ([]byte, error) {
	v, _, err := stringArg(req, name)
	if err != nil {
		return nil, err
	}
	if v == "" {
		return nil, nil
	}
	b, err := base64.StdEncoding.DecodeString(v)
	if err != nil {
		return nil, &ToolArgumentError{Argument: name, Reason: "must be base64: " + err.Error()}
	}
	return b, nil
}
