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

package rest

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// Compile-time interface assertion.
var _ transport.TransactionClient = (*Transport)(nil)

// ---------------------------------------------------------------------------
// Wire types for transaction REST communication
// ---------------------------------------------------------------------------

// txWireCompare matches the JSON compare shape expected by the REST server.
type txWireCompare struct {
	Key            string `json:"key"`
	Target         string `json:"target"`
	Result         string `json:"result"`
	Value          string `json:"value,omitempty"`
	Version        int64  `json:"version,omitempty"`
	CreateRevision int64  `json:"create_revision,omitempty"`
	ModRevision    int64  `json:"mod_revision,omitempty"`
}

// txWireOp matches the JSON operation shape expected by the REST server.
type txWireOp struct {
	Type     string `json:"type"`
	Key      string `json:"key"`
	Value    string `json:"value,omitempty"`
	RangeEnd string `json:"range_end,omitempty"`
	LeaseID  int64  `json:"lease_id,omitempty"`
}

// txWireRequest matches the JSON payload for POST /tx.
type txWireRequest struct {
	Compares   []txWireCompare `json:"compares"`
	SuccessOps []txWireOp      `json:"success_ops"`
	FailureOps []txWireOp      `json:"failure_ops"`
	WriteMode  string          `json:"write_mode,omitempty"`
}

// txWireOpResponse matches the JSON operation result from the REST server.
type txWireOpResponse struct {
	Kvs       []*mvccKVWireResponse `json:"kvs,omitempty"`
	Count     int64                 `json:"count,omitempty"`
	Succeeded bool                  `json:"succeeded"`
}

// txWireResponse matches the JSON payload returned by POST /tx.
type txWireResponse struct {
	Succeeded bool               `json:"succeeded"`
	Revision  int64              `json:"revision"`
	Responses []txWireOpResponse `json:"responses"`
}

// ---------------------------------------------------------------------------
// Enum string conversion maps
// ---------------------------------------------------------------------------

// compareTargetStringMap converts transport CompareTarget to the string
// expected by the REST server.
var compareTargetStringMap = map[transport.CompareTarget]string{
	transport.CompareVersion:        "version",
	transport.CompareCreateRevision: "create_revision",
	transport.CompareModRevision:    "mod_revision",
	transport.CompareValue:          "value",
}

// compareResultStringMap converts transport CompareResult to the string
// expected by the REST server.
var compareResultStringMap = map[transport.CompareResult]string{
	transport.CompareEqual:    "equal",
	transport.CompareGreater:  "greater",
	transport.CompareLess:     "less",
	transport.CompareNotEqual: "not_equal",
}

// opTypeStringMap converts transport OpType to the string expected by
// the REST server.
var opTypeStringMap = map[transport.OpType]string{
	transport.OpPut:    "put",
	transport.OpGet:    "get",
	transport.OpDelete: "delete",
	transport.OpRange:  "range",
}

// ---------------------------------------------------------------------------
// TransactionClient implementation
// ---------------------------------------------------------------------------

// ExecuteTransaction atomically evaluates compares and executes the
// corresponding operation set. Maps to POST /tx with a JSON body
// containing compares, success_ops, and failure_ops. When all compares
// succeed, success ops are applied; otherwise failure ops are applied.
func (t *Transport) ExecuteTransaction(ctx context.Context, req *transport.TransactionRequest, opts ...transport.WriteOption) (*transport.TransactionResponse, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}

	wireReq := txWireRequest{
		Compares:   comparesToWire(req.Compares),
		SuccessOps: opsToWire(req.SuccessOps),
		FailureOps: opsToWire(req.FailureOps),
	}

	wo := transport.ResolveWriteOptions(opts...)
	if wo.ModeSet {
		wireReq.WriteMode = wo.Mode.String()
	}

	var wireResp txWireResponse
	if err := t.postJSONDecode(ctx, "ExecuteTransaction", "/tx", wireReq, &wireResp); err != nil {
		return nil, err
	}

	responses := make([]transport.OpResponse, len(wireResp.Responses))
	for i, wr := range wireResp.Responses {
		kvs := make([]*transport.MVCCKeyValue, len(wr.Kvs))
		for j, wkv := range wr.Kvs {
			kvs[j] = wireKVToTransport(wkv)
		}
		responses[i] = transport.OpResponse{
			Kvs:       kvs,
			Count:     wr.Count,
			Succeeded: wr.Succeeded,
		}
	}

	return &transport.TransactionResponse{
		Succeeded: wireResp.Succeeded,
		Revision:  wireResp.Revision,
		Responses: responses,
	}, nil
}

// ---------------------------------------------------------------------------
// Wire conversion helpers
// ---------------------------------------------------------------------------

// comparesToWire converts a slice of transport Compares to wire format.
func comparesToWire(compares []transport.Compare) []txWireCompare {
	wire := make([]txWireCompare, len(compares))
	for i, c := range compares {
		wire[i] = txWireCompare{
			Key:            string(c.Key),
			Target:         compareTargetStringMap[c.Target],
			Result:         compareResultStringMap[c.Result],
			Value:          string(c.Value),
			Version:        c.Version,
			CreateRevision: c.CreateRevision,
			ModRevision:    c.ModRevision,
		}
	}
	return wire
}

// opsToWire converts a slice of transport Ops to wire format.
func opsToWire(ops []transport.Op) []txWireOp {
	wire := make([]txWireOp, len(ops))
	for i, op := range ops {
		wire[i] = txWireOp{
			Type:     opTypeStringMap[op.Type],
			Key:      string(op.Key),
			Value:    string(op.Value),
			RangeEnd: string(op.RangeEnd),
			LeaseID:  op.LeaseID,
		}
	}
	return wire
}
