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

	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/vlogwire"
)

const (
	// vlogLogArg describes the log name every per-log tool takes.
	vlogLogArg = "The log's name within the session's database."
)

// vlogTools returns the MCP tool registrations for the verifiable log. Every
// result is the JSON body of sdk/go/transport/vlogwire, byte fields base64-encoded,
// and every log is named within the session's database.
func vlogTools(svc service.LogService) []serverTool {
	return []serverTool{
		{Tool: toolVlogCreate(), Perm: permission.VlogCreate, Handler: handleVlogCreate(svc)},
		{Tool: toolVlogList(), Perm: permission.VlogList, Handler: handleVlogList(svc)},
		{Tool: toolVlogAppend(), Perm: permission.VlogAppend, Handler: handleVlogAppend(svc)},
		{Tool: toolVlogGet(), Perm: permission.VlogGet, Handler: handleVlogGet(svc)},
		{Tool: toolVlogProveInclusion(), Perm: permission.VlogProof, Handler: handleVlogProveInclusion(svc)},
		{Tool: toolVlogProveConsistency(), Perm: permission.VlogProof, Handler: handleVlogProveConsistency(svc)},
		{Tool: toolVlogHead(), Perm: permission.VlogHead, Handler: handleVlogHead(svc)},
		{Tool: toolVlogSignHead(), Perm: permission.VlogSignHead, Handler: handleVlogSignHead(svc)},
		{Tool: toolVlogRotateKey(), Perm: permission.VlogSignHead, Handler: handleVlogRotateKey(svc)},
		{Tool: toolVlogExpireBodies(), Perm: permission.VlogExpire, Handler: handleVlogExpireBodies(svc)},
	}
}

// vlogTool builds a log tool's schema: the tenant and log arguments every
// per-log tool takes, then opts.
func vlogTool(name, description string, opts ...mcp.ToolOption) mcp.Tool {
	all := append([]mcp.ToolOption{
		mcp.WithDescription(description),
		mcp.WithString("log", mcp.Required(), mcp.Description(vlogLogArg)),
	}, opts...)
	return mcp.NewTool(name, all...)
}

// vlogRef reads the log a request names. A non-nil result is the tool error
// to return.
func vlogRef(ctx context.Context, req mcp.CallToolRequest) (service.LogRef, *mcp.CallToolResult) {
	log, present := req.GetArguments()["log"].(string)
	if !present {
		return service.LogRef{}, mcp.NewToolResultError((&RequiredParameterError{Parameter: "log"}).Error())
	}
	return service.LogRef{
		Database: tenant.DatabaseFromContext(ctx),
		Log:      log,
	}, nil
}

// vlogCall reads the log a request names, calls the service and renders the
// result. It is the one body every per-log tool shares.
func vlogCall[Res any](ctx context.Context, req mcp.CallToolRequest, tool string, call func(service.LogRef) (Res, error)) (*mcp.CallToolResult, error) {
	ref, failure := vlogRef(ctx, req)
	if failure != nil {
		return failure, nil
	}
	res, err := call(ref)
	if err != nil {
		return nil, &ToolError{Tool: tool, Err: err}
	}
	return toolJSONResult(tool, res)
}

func toolVlogCreate() mcp.Tool {
	return vlogTool("qrdb_vlog_create",
		"Create a verifiable log: an append-only RFC 9162 Merkle tree whose heads are signed as C2SP checkpoints.",
		mcp.WithString("origin", mcp.Required(), mcp.Description("The log's identity, the first line of every checkpoint: non-empty UTF-8 with no control characters.")),
		mcp.WithArray("verifier_keys", mcp.WithStringItems(), mcp.Description("Optional keys a checkpoint's signature is checked against, opaque to the server.")),
		mcp.WithBoolean("allow_expire_bodies", mcp.Description("Permit qrdb_vlog_expire_bodies on the log. Fixed at creation.")),
		mcp.WithString("signing_key_type", mcp.Description("Optional first head-signing key type: ed25519 or ecdsa-p256-sha256. Without one the log has no key and qrdb_vlog_sign_head refuses it.")),
	)
}

func handleVlogCreate(svc service.LogService) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		verifierKeys, err := stringListArg(req, "verifier_keys")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return vlogCall(ctx, req, "qrdb_vlog_create", func(ref service.LogRef) (*vlogwire.CreateResult, error) {
			resp, err := svc.CreateLog(ctx, &service.CreateLogRequest{
				LogRef:            ref,
				Origin:            mcp.ParseString(req, "origin", ""),
				VerifierKeys:      verifierKeys,
				AllowExpireBodies: mcp.ParseBoolean(req, "allow_expire_bodies", false),
				SigningKeyType:    vlog.SignatureType(mcp.ParseString(req, "signing_key_type", "")),
			})
			if err != nil {
				return nil, err
			}
			return vlogwire.Create(resp.ID, resp.Key), nil
		})
	}
}

func toolVlogList() mcp.Tool {
	return mcp.NewTool("qrdb_vlog_list",
		mcp.WithDescription("List the verifiable logs of the session's database."),
	)
}

func handleVlogList(svc service.LogService) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		resp, err := svc.ListLogs(ctx, &service.ListLogsRequest{
			Database: tenant.DatabaseFromContext(ctx),
		})
		if err != nil {
			return nil, &ToolError{Tool: "qrdb_vlog_list", Err: err}
		}
		return toolJSONResult("qrdb_vlog_list", vlogwire.List(resp.Logs))
	}
}

func toolVlogAppend() mcp.Tool {
	return vlogTool("qrdb_vlog_append",
		"Append entries to a verifiable log as one batch. Entries are never changed or removed once appended.",
		mcp.WithArray("entries", mcp.Required(), mcp.WithStringItems(), mcp.Description("The entry bodies, each base64-encoded (RFC 4648).")),
	)
}

func handleVlogAppend(svc service.LogService) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		entries, failure := vlogEntriesArg(req)
		if failure != nil {
			return failure, nil
		}
		return vlogCall(ctx, req, "qrdb_vlog_append", func(ref service.LogRef) (*vlogwire.AppendResult, error) {
			resp, err := svc.Append(ctx, &service.AppendRequest{LogRef: ref, Entries: entries})
			if err != nil {
				return nil, err
			}
			return &vlogwire.AppendResult{FirstIndex: resp.FirstIndex, Count: resp.Count}, nil
		})
	}
}

// vlogEntriesArg decodes the required "entries" argument, an array of base64
// strings. A non-nil result is the tool error to return.
func vlogEntriesArg(req mcp.CallToolRequest) ([][]byte, *mcp.CallToolResult) {
	if _, present := req.GetArguments()["entries"]; !present {
		return nil, mcp.NewToolResultError((&RequiredParameterError{Parameter: "entries"}).Error())
	}
	raw, err := stringListArg(req, "entries")
	if err != nil {
		return nil, mcp.NewToolResultError(err.Error())
	}
	entries := make([][]byte, len(raw))
	for i, s := range raw {
		if entries[i], err = base64.StdEncoding.DecodeString(s); err != nil {
			return nil, mcp.NewToolResultError((&ValueDecodeError{Field: "entries", Err: err}).Error())
		}
	}
	return entries, nil
}

func toolVlogGet() mcp.Tool {
	return vlogTool("qrdb_vlog_get",
		"Read consecutive entries of a verifiable log. An expired entry carries no body.",
		mcp.WithNumber("from", mcp.Description("The index of the first entry to read. Default 0.")),
		mcp.WithNumber("limit", mcp.Description("Optional maximum number of entries. Outside 1 to the server's page maximum reads the maximum.")),
	)
}

func handleVlogGet(svc service.LogService) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return vlogCall(ctx, req, "qrdb_vlog_get", func(ref service.LogRef) (*vlogwire.GetResult, error) {
			resp, err := svc.GetEntries(ctx, &service.GetEntriesRequest{
				LogRef: ref,
				From:   mcp.ParseUInt64(req, "from", 0),
				Limit:  mcp.ParseInt(req, "limit", 0),
			})
			if err != nil {
				return nil, err
			}
			return vlogwire.Get(resp.Entries), nil
		})
	}
}

func toolVlogProveInclusion() mcp.Tool {
	return vlogTool("qrdb_vlog_prove_inclusion",
		"Prove the entry at index is in the log's tree of size entries (RFC 9162 §2.1.3).",
		mcp.WithNumber("index", mcp.Required(), mcp.Description("The entry's index.")),
		mcp.WithNumber("size", mcp.Required(), mcp.Description("The tree size the proof is against.")),
	)
}

func handleVlogProveInclusion(svc service.LogService) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return vlogCall(ctx, req, "qrdb_vlog_prove_inclusion", func(ref service.LogRef) (*vlogwire.InclusionProof, error) {
			resp, err := svc.ProveInclusion(ctx, &service.InclusionRequest{
				LogRef: ref,
				Index:  mcp.ParseUInt64(req, "index", 0),
				Size:   mcp.ParseUInt64(req, "size", 0),
			})
			if err != nil {
				return nil, err
			}
			return &vlogwire.InclusionProof{Index: resp.Index, Size: resp.Size, Hashes: resp.Hashes}, nil
		})
	}
}

func toolVlogProveConsistency() mcp.Tool {
	return vlogTool("qrdb_vlog_prove_consistency",
		"Prove the log's tree of size1 entries is a prefix of its tree of size2 entries (RFC 9162 §2.1.4).",
		mcp.WithNumber("size1", mcp.Required(), mcp.Description("The earlier tree size.")),
		mcp.WithNumber("size2", mcp.Required(), mcp.Description("The later tree size.")),
	)
}

func handleVlogProveConsistency(svc service.LogService) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return vlogCall(ctx, req, "qrdb_vlog_prove_consistency", func(ref service.LogRef) (*vlogwire.ConsistencyProof, error) {
			resp, err := svc.ProveConsistency(ctx, &service.ConsistencyRequest{
				LogRef: ref,
				Size1:  mcp.ParseUInt64(req, "size1", 0),
				Size2:  mcp.ParseUInt64(req, "size2", 0),
			})
			if err != nil {
				return nil, err
			}
			return &vlogwire.ConsistencyProof{Size1: resp.Size1, Size2: resp.Size2, Hashes: resp.Hashes}, nil
		})
	}
}

func toolVlogHead() mcp.Tool {
	return vlogTool("qrdb_vlog_head", "Report a verifiable log's current tree head and its latest checkpoint.")
}

func handleVlogHead(svc service.LogService) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return vlogCall(ctx, req, "qrdb_vlog_head", func(ref service.LogRef) (*vlogwire.Head, error) {
			resp, err := svc.Head(ctx, &service.HeadRequest{LogRef: ref})
			if err != nil {
				return nil, err
			}
			return vlogwire.HeadOf(resp.Head), nil
		})
	}
}

func toolVlogSignHead() mcp.Tool {
	return vlogTool("qrdb_vlog_sign_head", "Sign a verifiable log's current tree head and record it as a C2SP checkpoint.")
}

func handleVlogSignHead(svc service.LogService) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return vlogCall(ctx, req, "qrdb_vlog_sign_head", func(ref service.LogRef) (*vlogwire.SignHeadResult, error) {
			resp, err := svc.SignHead(ctx, &service.SignHeadRequest{LogRef: ref})
			if err != nil {
				return nil, err
			}
			return &vlogwire.SignHeadResult{Head: resp.Head, Checkpoint: resp.Checkpoint}, nil
		})
	}
}

func toolVlogRotateKey() mcp.Tool {
	return vlogTool("qrdb_vlog_rotate_key",
		"Rotate a verifiable log's head-signing key: append a new key to the log's verifier keys. Every head is signed by it from then on; checkpoints signed before still verify under the key they name.",
		mcp.WithString("signing_key_type", mcp.Required(), mcp.Description("The new key's type: ed25519 or ecdsa-p256-sha256.")),
	)
}

func handleVlogRotateKey(svc service.LogService) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return vlogCall(ctx, req, "qrdb_vlog_rotate_key", func(ref service.LogRef) (*vlogwire.RotateKeyResult, error) {
			resp, err := svc.RotateKey(ctx, &service.RotateKeyRequest{
				LogRef:         ref,
				SigningKeyType: vlog.SignatureType(mcp.ParseString(req, "signing_key_type", "")),
			})
			if err != nil {
				return nil, err
			}
			return &vlogwire.RotateKeyResult{Key: vlogwire.Key(resp.Key)}, nil
		})
	}
}

func toolVlogExpireBodies() mcp.Tool {
	return vlogTool("qrdb_vlog_expire_bodies",
		"Remove the bodies of a verifiable log's entries below cutoff. Their leaf hashes, and so every proof, stay. Refused unless the log was created with allow_expire_bodies.",
		mcp.WithNumber("cutoff", mcp.Required(), mcp.Description("Entries below this index lose their bodies.")),
	)
}

func handleVlogExpireBodies(svc service.LogService) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return vlogCall(ctx, req, "qrdb_vlog_expire_bodies", func(ref service.LogRef) (*vlogwire.ExpireResult, error) {
			resp, err := svc.ExpireBodies(ctx, &service.ExpireBodiesRequest{LogRef: ref, Cutoff: mcp.ParseUInt64(req, "cutoff", 0)})
			if err != nil {
				return nil, err
			}
			return &vlogwire.ExpireResult{Expired: resp.Expired}, nil
		})
	}
}
