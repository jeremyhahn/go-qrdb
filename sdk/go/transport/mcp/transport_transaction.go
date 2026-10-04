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

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.TransactionClient = (*Transport)(nil)

// ---------------------------------------------------------------------------
// SDK to MCP string conversion maps
// ---------------------------------------------------------------------------

// sdkCompareTargetToString maps SDK CompareTarget constants to the
// string names expected by the MCP server tool parameters.
var sdkCompareTargetToString = map[transport.CompareTarget]string{
	transport.CompareVersion:        "version",
	transport.CompareCreateRevision: "create_revision",
	transport.CompareModRevision:    "mod_revision",
	transport.CompareValue:          "value",
}

// sdkCompareResultToString maps SDK CompareResult constants to the
// string names expected by the MCP server tool parameters.
var sdkCompareResultToString = map[transport.CompareResult]string{
	transport.CompareEqual:    "equal",
	transport.CompareGreater:  "greater",
	transport.CompareLess:     "less",
	transport.CompareNotEqual: "not_equal",
}

// sdkOpTypeToString maps SDK OpType constants to the string names
// expected by the MCP server tool parameters.
var sdkOpTypeToString = map[transport.OpType]string{
	transport.OpPut:    "put",
	transport.OpGet:    "get",
	transport.OpDelete: "delete",
	transport.OpRange:  "range",
}

// ---------------------------------------------------------------------------
// JSON wire types for transaction tool arguments
// ---------------------------------------------------------------------------

// txCompareWire is the JSON representation of a compare for the MCP
// tool argument.
type txCompareWire struct {
	Key            string `json:"key"`
	Target         string `json:"target"`
	Result         string `json:"result"`
	Value          []byte `json:"value,omitempty"`
	Version        int64  `json:"version,omitempty"`
	CreateRevision int64  `json:"create_revision,omitempty"`
	ModRevision    int64  `json:"mod_revision,omitempty"`
}

// txOpWire is the JSON representation of an operation for the MCP
// tool argument.
type txOpWire struct {
	Type     string `json:"type"`
	Key      string `json:"key"`
	Value    []byte `json:"value,omitempty"`
	RangeEnd []byte `json:"range_end,omitempty"`
	LeaseID  int64  `json:"lease_id,omitempty"`
}

// ---------------------------------------------------------------------------
// Server-side JSON decode types for transaction responses
// ---------------------------------------------------------------------------

// serverTxResponse mirrors service.TransactionResponse for JSON
// decoding. The server-side struct has no JSON tags, so Go's default
// PascalCase encoding is used.
type serverTxResponse struct {
	Succeeded bool               `json:"Succeeded"`
	Revision  int64              `json:"Revision"`
	Responses []serverOpResponse `json:"Responses"`
}

// serverOpResponse mirrors service.OpResponse for JSON decoding.
type serverOpResponse struct {
	Kvs       []serverKeyValue `json:"Kvs"`
	Count     int64            `json:"Count"`
	Succeeded bool             `json:"Succeeded"`
}

// ---------------------------------------------------------------------------
// TransactionClient implementation
// ---------------------------------------------------------------------------

// ExecuteTransaction atomically evaluates compare preconditions and
// executes the corresponding operation set. When all compares succeed,
// success ops are applied; otherwise failure ops are applied. Calls
// the qrdb_tx_execute MCP tool.
func (t *Transport) ExecuteTransaction(ctx context.Context, req *transport.TransactionRequest, opts ...transport.WriteOption) (*transport.TransactionResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	// Marshal compares to JSON string.
	comparesWire := make([]txCompareWire, len(req.Compares))
	for i, c := range req.Compares {
		comparesWire[i] = txCompareWire{
			Key:            string(c.Key),
			Target:         sdkCompareTargetToString[c.Target],
			Result:         sdkCompareResultToString[c.Result],
			Value:          c.Value,
			Version:        c.Version,
			CreateRevision: c.CreateRevision,
			ModRevision:    c.ModRevision,
		}
	}

	comparesJSON, err := json.Marshal(comparesWire)
	if err != nil {
		return nil, &transport.RequestError{Method: "ExecuteTransaction", Err: &jsonDecodeError{Err: err}}
	}

	// Marshal success operations to JSON string.
	successJSON, err := marshalOpsWire(req.SuccessOps)
	if err != nil {
		return nil, &transport.RequestError{Method: "ExecuteTransaction", Err: &jsonDecodeError{Err: err}}
	}

	// Marshal failure operations to JSON string.
	failureJSON, err := marshalOpsWire(req.FailureOps)
	if err != nil {
		return nil, &transport.RequestError{Method: "ExecuteTransaction", Err: &jsonDecodeError{Err: err}}
	}

	args := map[string]any{
		"compares": string(comparesJSON),
		"success":  string(successJSON),
		"failure":  string(failureJSON),
	}

	wo := transport.ResolveWriteOptions(opts...)
	if wo.ModeSet {
		args["write_mode"] = wo.Mode.String()
	}

	text, callErr := t.callTool(ctx, "qrdb_tx_execute", args)
	if callErr != nil {
		ops := append(append([]transport.Op{}, req.SuccessOps...), req.FailureOps...)
		return nil, mapOpsNotFoundError(ops, callErr)
	}

	var raw serverTxResponse
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return nil, &transport.RequestError{Method: "ExecuteTransaction", Err: &jsonDecodeError{Err: jsonErr}}
	}

	responses := make([]transport.OpResponse, len(raw.Responses))
	for i, r := range raw.Responses {
		kvs := make([]*transport.MVCCKeyValue, len(r.Kvs))
		for j := range r.Kvs {
			kvs[j] = serverKeyValueToTransport(&r.Kvs[j])
		}
		responses[i] = transport.OpResponse{
			Kvs:       kvs,
			Count:     r.Count,
			Succeeded: r.Succeeded,
		}
	}

	return &transport.TransactionResponse{
		Succeeded: raw.Succeeded,
		Revision:  raw.Revision,
		Responses: responses,
	}, nil
}

// marshalOpsWire converts SDK operations to the wire format JSON string
// expected by the MCP transaction and batch tools.
func marshalOpsWire(ops []transport.Op) ([]byte, error) {
	wire := make([]txOpWire, len(ops))
	for i, op := range ops {
		wire[i] = txOpWire{
			Type:     sdkOpTypeToString[op.Type],
			Key:      string(op.Key),
			Value:    op.Value,
			RangeEnd: op.RangeEnd,
			LeaseID:  op.LeaseID,
		}
	}
	return json.Marshal(wire)
}
