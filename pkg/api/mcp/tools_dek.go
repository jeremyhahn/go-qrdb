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

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/jeremyhahn/go-qrdb/pkg/api/dekwire"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// dekTenantArg describes the optional tenant argument every keyring tool
// takes. The service refuses any tenant but the caller's own.
const dekTenantArg = "Optional. The caller's own tenant ID; any other tenant is refused."

// dekTools returns the MCP tool registrations for the tenant DEK keyring.
// Every result is the JSON body the REST transport returns, byte fields
// base64-encoded.
func dekTools(svc service.DEKServicer) []serverTool {
	return []serverTool{
		{Tool: toolDEKStatus(), Perm: permission.DEKStatus, Handler: handleDEKStatus(svc)},
		{Tool: toolDEKRotate(), Perm: permission.DEKRotate, Handler: handleDEKRotate(svc)},
		{Tool: toolDEKRetire(), Perm: permission.DEKRetire, Handler: handleDEKRetire(svc)},
		{Tool: toolDEKEncrypt(), Perm: permission.DEKEncrypt, Handler: handleDEKEncrypt(svc)},
		{Tool: toolDEKDecrypt(), Perm: permission.DEKDecrypt, Handler: handleDEKDecrypt(svc)},
		{Tool: toolDEKRewrap(), Perm: permission.DEKRewrap, Handler: handleDEKRewrap(svc)},
	}
}

func toolDEKStatus() mcp.Tool {
	return mcp.NewTool("qrdb_dek_status",
		mcp.WithDescription("Report the caller's tenant DEK keyring versions. Never returns key material."),
		mcp.WithString("tenant_id", mcp.Description(dekTenantArg)),
	)
}

func handleDEKStatus(svc service.DEKServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		st, err := svc.Status(ctx, mcp.ParseString(req, "tenant_id", ""))
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_dek_status", Err: err}
		}
		return toolJSONResult("qrdb_dek_status", dekwire.Status(st))
	}
}

func toolDEKRotate() mcp.Tool {
	return mcp.NewTool("qrdb_dek_rotate",
		mcp.WithDescription("Add a tenant DEK version and make it current. Earlier versions keep decrypting until retired."),
		mcp.WithString("tenant_id", mcp.Description(dekTenantArg)),
	)
}

func handleDEKRotate(svc service.DEKServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		st, err := svc.Rotate(ctx, mcp.ParseString(req, "tenant_id", ""))
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_dek_rotate", Err: err}
		}
		return toolJSONResult("qrdb_dek_rotate", dekwire.Status(st))
	}
}

func toolDEKRetire() mcp.Tool {
	return mcp.NewTool("qrdb_dek_retire",
		mcp.WithDescription("Destroy an old tenant DEK version. The current version is refused. Rewrap every ciphertext under the version first: retirement is irreversible."),
		mcp.WithString("tenant_id", mcp.Description(dekTenantArg)),
		mcp.WithNumber("version", mcp.Required(), mcp.Description("The key version to retire.")),
	)
}

func handleDEKRetire(svc service.DEKServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		st, err := svc.Retire(ctx, mcp.ParseString(req, "tenant_id", ""), mcp.ParseUInt64(req, "version", 0))
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_dek_retire", Err: err}
		}
		return toolJSONResult("qrdb_dek_retire", dekwire.Status(st))
	}
}

func toolDEKEncrypt() mcp.Tool {
	return mcp.NewTool("qrdb_dek_encrypt",
		mcp.WithDescription("Encrypt data under the caller's current tenant DEK version. The key never leaves the server."),
		mcp.WithString("tenant_id", mcp.Description(dekTenantArg)),
		mcp.WithString("plaintext", mcp.Required(), mcp.Description("The data to encrypt, base64-encoded (RFC 4648).")),
		mcp.WithString("aad", mcp.Description("Optional associated data, base64-encoded. It must be presented unchanged to decrypt and rewrap.")),
	)
}

func handleDEKEncrypt(svc service.DEKServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		plaintext, aad, failure := dekDataArgs(req, "plaintext")
		if failure != nil {
			return failure, nil
		}
		sealed, err := svc.Encrypt(ctx, mcp.ParseString(req, "tenant_id", ""), plaintext, aad)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_dek_encrypt", Err: err}
		}
		return toolJSONResult("qrdb_dek_encrypt", dekwire.Encrypt(sealed))
	}
}

func toolDEKDecrypt() mcp.Tool {
	return mcp.NewTool("qrdb_dek_decrypt",
		mcp.WithDescription("Decrypt a ciphertext produced by qrdb_dek_encrypt or qrdb_dek_rewrap for the caller's tenant."),
		mcp.WithString("tenant_id", mcp.Description(dekTenantArg)),
		mcp.WithString("ciphertext", mcp.Required(), mcp.Description("The ciphertext, base64-encoded.")),
		mcp.WithString("aad", mcp.Description("The associated data given at encryption, base64-encoded.")),
	)
}

func handleDEKDecrypt(svc service.DEKServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		ciphertext, aad, failure := dekDataArgs(req, "ciphertext")
		if failure != nil {
			return failure, nil
		}
		opened, err := svc.Decrypt(ctx, mcp.ParseString(req, "tenant_id", ""), ciphertext, aad)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_dek_decrypt", Err: err}
		}
		return toolJSONResult("qrdb_dek_decrypt", dekwire.Decrypt(opened))
	}
}

func toolDEKRewrap() mcp.Tool {
	return mcp.NewTool("qrdb_dek_rewrap",
		mcp.WithDescription("Re-encrypt a ciphertext under the caller's current tenant DEK version without returning its plaintext."),
		mcp.WithString("tenant_id", mcp.Description(dekTenantArg)),
		mcp.WithString("ciphertext", mcp.Required(), mcp.Description("The ciphertext, base64-encoded.")),
		mcp.WithString("aad", mcp.Description("The associated data given at encryption, base64-encoded.")),
	)
}

func handleDEKRewrap(svc service.DEKServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		ciphertext, aad, failure := dekDataArgs(req, "ciphertext")
		if failure != nil {
			return failure, nil
		}
		rw, err := svc.Rewrap(ctx, mcp.ParseString(req, "tenant_id", ""), ciphertext, aad)
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_dek_rewrap", Err: err}
		}
		return toolJSONResult("qrdb_dek_rewrap", dekwire.Rewrap(rw))
	}
}

// dekDataArgs decodes the required base64 data argument named field and the
// optional base64 "aad". A non-nil result is the tool error to return.
func dekDataArgs(req mcp.CallToolRequest, field string) (data, aad []byte, failure *mcp.CallToolResult) {
	// Presence, not emptiness, is what is required: "" is the base64 form
	// of an empty value, which the other transports accept too.
	raw, present := req.GetArguments()[field].(string)
	if !present {
		return nil, nil, mcp.NewToolResultError((&RequiredParameterError{Parameter: field}).Error())
	}
	data, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, nil, mcp.NewToolResultError((&ValueDecodeError{Field: field, Err: err}).Error())
	}
	if rawAAD := mcp.ParseString(req, "aad", ""); rawAAD != "" {
		aad, err = base64.StdEncoding.DecodeString(rawAAD)
		if err != nil {
			return nil, nil, mcp.NewToolResultError((&ValueDecodeError{Field: "aad", Err: err}).Error())
		}
	}
	return data, aad, nil
}

// toolJSONResult renders a response as the tool's JSON text result.
func toolJSONResult(tool string, v any) (*mcp.CallToolResult, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, &ToolError{Tool: tool, Err: err}
	}
	return mcp.NewToolResultText(string(data)), nil
}
