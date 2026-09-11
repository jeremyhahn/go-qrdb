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

// compareTargetMap maps string names to CompareTarget constants for
// O(1) dispatch in transaction compare parsing.
var compareTargetMap = map[string]service.CompareTarget{
	"version":         service.CompareVersion,
	"create_revision": service.CompareCreateRevision,
	"mod_revision":    service.CompareModRevision,
	"value":           service.CompareValue,
}

// compareResultMap maps string names to CompareResult constants for
// O(1) dispatch in transaction compare parsing.
var compareResultMap = map[string]service.CompareResult{
	"equal":     service.CompareEqual,
	"greater":   service.CompareGreater,
	"less":      service.CompareLess,
	"not_equal": service.CompareNotEqual,
}

// opTypeMap maps string names to OpType constants for O(1) dispatch
// in transaction and batch operation parsing.
var mcpOpTypeMap = map[string]service.OpType{
	"put":    service.OpPut,
	"get":    service.OpGet,
	"delete": service.OpDelete,
	"range":  service.OpRange,
}

// transactionTools returns the MCP tool registrations for transaction operations.
func transactionTools(txSvc service.TransactionServicer) []serverTool {
	return []serverTool{
		{
			Tool:    toolTxExecute(),
			Perm:    permission.TransactionExecute,
			Handler: handleTxExecute(txSvc),
		},
	}
}

// toolTxExecute defines the qrdb_tx_execute tool schema.
func toolTxExecute() mcp.Tool {
	return mcp.NewTool("qrdb_tx_execute",
		mcp.WithDescription("Execute an atomic compare-and-swap transaction. Evaluates preconditions and atomically applies success or failure operations within a single MVCC revision."),
		mcp.WithString("compares",
			mcp.Required(),
			mcp.Description(`JSON array of compare preconditions. Each element: {"key":"<key>","target":"version|create_revision|mod_revision|value","result":"equal|greater|less|not_equal","value":"<base64>","version":<int>,"create_revision":<int>,"mod_revision":<int>}.`),
		),
		mcp.WithString("success",
			mcp.Required(),
			mcp.Description(`JSON array of operations to execute when all compares pass. Each element: {"type":"put|get|delete|range","key":"<key>","value":"<base64>","range_end":"<base64>","lease_id":<int>}.`),
		),
		mcp.WithString("failure",
			mcp.Description(`JSON array of operations to execute when any compare fails. Same format as success. Defaults to empty.`),
		),
	)
}

// txCompareJSON is the JSON representation of a transaction compare.
type txCompareJSON struct {
	Key            string `json:"key"`
	Target         string `json:"target"`
	Result         string `json:"result"`
	Value          []byte `json:"value"`
	Version        int64  `json:"version"`
	CreateRevision int64  `json:"create_revision"`
	ModRevision    int64  `json:"mod_revision"`
}

// txOpJSON is the JSON representation of a transaction or batch operation.
type txOpJSON struct {
	Type     string `json:"type"`
	Key      string `json:"key"`
	Value    []byte `json:"value"`
	RangeEnd []byte `json:"range_end"`
	LeaseID  int64  `json:"lease_id"`
}

// handleTxExecute returns a tool handler that delegates to txSvc.Execute.
func handleTxExecute(txSvc service.TransactionServicer) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		txSvc, rerr := resolveTx(ctx, txSvc)
		if rerr != nil {
			return nil, rerr
		}
		comparesStr := mcp.ParseString(req, "compares", "")
		if comparesStr == "" {
			return mcp.NewToolResultError("compares is required"), nil
		}
		successStr := mcp.ParseString(req, "success", "")
		if successStr == "" {
			return mcp.NewToolResultError("success is required"), nil
		}
		failureStr := mcp.ParseString(req, "failure", "[]")

		// Parse compares.
		var comparesJSON []txCompareJSON
		if err := json.Unmarshal([]byte(comparesStr), &comparesJSON); err != nil {
			return mcp.NewToolResultError("invalid compares JSON: " + err.Error()), nil
		}

		compares := make([]service.Compare, len(comparesJSON))
		for i, c := range comparesJSON {
			target, ok := compareTargetMap[c.Target]
			if !ok {
				return mcp.NewToolResultError("invalid compare target: " + c.Target), nil
			}
			result, ok := compareResultMap[c.Result]
			if !ok {
				return mcp.NewToolResultError("invalid compare result: " + c.Result), nil
			}
			compares[i] = service.Compare{
				Key:            []byte(c.Key),
				Target:         target,
				Result:         result,
				Value:          c.Value,
				Version:        c.Version,
				CreateRevision: c.CreateRevision,
				ModRevision:    c.ModRevision,
			}
		}

		// Parse success operations.
		successOps, err := parseOps(successStr)
		if err != nil {
			return mcp.NewToolResultError("invalid success ops JSON: " + err.Error()), nil
		}

		// Parse failure operations.
		failureOps, err := parseOps(failureStr)
		if err != nil {
			return mcp.NewToolResultError("invalid failure ops JSON: " + err.Error()), nil
		}

		resp, execErr := txSvc.Execute(ctx, compares, successOps, failureOps)
		if execErr != nil {
			return nil, &ToolError{Tool: "qrdb_tx_execute", Err: execErr}
		}

		data, jsonErr := json.Marshal(resp)
		if jsonErr != nil {
			return nil, &ToolError{Tool: "qrdb_tx_execute", Err: jsonErr}
		}

		return mcp.NewToolResultText(string(data)), nil
	}
}

// parseOps parses a JSON string into a slice of service.Op.
func parseOps(opsStr string) ([]service.Op, error) {
	var opsJSON []txOpJSON
	if err := json.Unmarshal([]byte(opsStr), &opsJSON); err != nil {
		return nil, err
	}

	ops := make([]service.Op, len(opsJSON))
	for i, o := range opsJSON {
		opType, ok := mcpOpTypeMap[o.Type]
		if !ok {
			opType = service.OpPut
		}
		ops[i] = service.Op{
			Type:     opType,
			Key:      []byte(o.Key),
			Value:    o.Value,
			RangeEnd: o.RangeEnd,
			LeaseID:  o.LeaseID,
		}
	}
	return ops, nil
}
