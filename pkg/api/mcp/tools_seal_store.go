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

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// MCP tool names of the node seal store.
const (
	toolNameSealStorePut    = "qrdb_seal_store_put"
	toolNameSealStoreGet    = "qrdb_seal_store_get"
	toolNameSealStoreDelete = "qrdb_seal_store_delete"
	toolNameSealStoreList   = "qrdb_seal_store_list"
	toolNameSealStoreReseal = "qrdb_seal_store_reseal"
	toolNameSealStoreStatus = "qrdb_seal_store_status"
)

// sealStoreTools returns the node seal store tools, each gated by
// seal:store_manage (held by the system Security Officer, not by a tenant
// Admin). svc may be nil when only the tool catalogue is needed.
func sealStoreTools(svc service.SealStoreServicer) []serverTool {
	return []serverTool{
		{Tool: toolSealStorePut(), Perm: permission.SealStoreManage, Handler: handleSealStorePut(svc)},
		{Tool: toolSealStoreGet(), Perm: permission.SealStoreManage, Handler: handleSealStoreGet(svc)},
		{Tool: toolSealStoreDelete(), Perm: permission.SealStoreManage, Handler: handleSealStoreDelete(svc)},
		{Tool: toolSealStoreList(), Perm: permission.SealStoreManage, Handler: handleSealStoreList(svc)},
		{Tool: toolSealStoreReseal(), Perm: permission.SealStoreManage, Handler: handleSealStoreReseal(svc)},
		{Tool: toolSealStoreStatus(), Perm: permission.SealStoreManage, Handler: handleSealStoreStatus(svc)},
	}
}

func sealStoreNameArg(desc string) mcp.ToolOption {
	return mcp.WithString("name", mcp.Required(), mcp.Description(desc))
}

func toolSealStorePut() mcp.Tool {
	return mcp.NewTool(toolNameSealStorePut,
		mcp.WithDescription("Store a secret in the node seal store under name, sealed by the barrier."),
		sealStoreNameArg("The secret name."),
		mcp.WithString("secret",
			mcp.Required(),
			mcp.Description("The secret bytes, standard base64."),
		),
	)
}

func handleSealStorePut(svc service.SealStoreServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name := mcp.ParseString(req, "name", "")
		if name == "" {
			return mcp.NewToolResultError("name is required"), nil
		}
		secret, err := base64.StdEncoding.DecodeString(mcp.ParseString(req, "secret", ""))
		if err != nil {
			return mcp.NewToolResultError("secret must be standard base64: " + err.Error()), nil
		}
		if err := svc.Put(ctx, name, secret); err != nil {
			return nil, &ToolError{Tool: toolNameSealStorePut, Err: err}
		}
		return mcp.NewToolResultText("ok"), nil
	}
}

func toolSealStoreGet() mcp.Tool {
	return mcp.NewTool(toolNameSealStoreGet,
		mcp.WithDescription("Return the secret stored under name in the node seal store, as base64."),
		sealStoreNameArg("The secret name."),
	)
}

func handleSealStoreGet(svc service.SealStoreServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name := mcp.ParseString(req, "name", "")
		if name == "" {
			return mcp.NewToolResultError("name is required"), nil
		}
		secret, err := svc.Get(ctx, name)
		if err != nil {
			return nil, &ToolError{Tool: toolNameSealStoreGet, Err: err}
		}
		return sealStoreJSON(toolNameSealStoreGet, map[string]string{"secret": base64.StdEncoding.EncodeToString(secret)})
	}
}

func toolSealStoreDelete() mcp.Tool {
	return mcp.NewTool(toolNameSealStoreDelete,
		mcp.WithDescription("Delete the secret stored under name in the node seal store."),
		sealStoreNameArg("The secret name."),
	)
}

func handleSealStoreDelete(svc service.SealStoreServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name := mcp.ParseString(req, "name", "")
		if name == "" {
			return mcp.NewToolResultError("name is required"), nil
		}
		if err := svc.Delete(ctx, name); err != nil {
			return nil, &ToolError{Tool: toolNameSealStoreDelete, Err: err}
		}
		return mcp.NewToolResultText("ok"), nil
	}
}

func toolSealStoreList() mcp.Tool {
	return mcp.NewTool(toolNameSealStoreList,
		mcp.WithDescription("List the names of the secrets in the node seal store."),
	)
}

func handleSealStoreList(svc service.SealStoreServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		names, err := svc.List(ctx)
		if err != nil {
			return nil, &ToolError{Tool: toolNameSealStoreList, Err: err}
		}
		if names == nil {
			names = []string{}
		}
		return sealStoreJSON(toolNameSealStoreList, map[string][]string{"names": names})
	}
}

func toolSealStoreReseal() mcp.Tool {
	return mcp.NewTool(toolNameSealStoreReseal,
		mcp.WithDescription("Reseal the secret stored under name under the current barrier key."),
		sealStoreNameArg("The secret name."),
	)
}

func handleSealStoreReseal(svc service.SealStoreServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name := mcp.ParseString(req, "name", "")
		if name == "" {
			return mcp.NewToolResultError("name is required"), nil
		}
		if err := svc.Reseal(ctx, name); err != nil {
			return nil, &ToolError{Tool: toolNameSealStoreReseal, Err: err}
		}
		return mcp.NewToolResultText("ok"), nil
	}
}

func toolSealStoreStatus() mcp.Tool {
	return mcp.NewTool(toolNameSealStoreStatus,
		mcp.WithDescription("Return the node seal store status: availability, sealer, secret count and names."),
	)
}

func handleSealStoreStatus(svc service.SealStoreServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		st, err := svc.Status(ctx)
		if err != nil {
			return nil, &ToolError{Tool: toolNameSealStoreStatus, Err: err}
		}
		return sealStoreJSON(toolNameSealStoreStatus, &transport.SealStoreStatusResponse{
			Available:   st.Available,
			SealerID:    string(st.Strategy),
			SecretCount: st.SecretCount,
			SecretNames: st.SecretNames,
		})
	}
}

func sealStoreJSON(tool string, v any) (*mcp.CallToolResult, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, &ToolError{Tool: tool, Err: err}
	}
	return mcp.NewToolResultText(string(data)), nil
}
