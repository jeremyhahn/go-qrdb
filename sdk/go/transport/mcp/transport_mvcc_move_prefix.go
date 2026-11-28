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

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// movePrefixOpWire is one op of qrdb_mvcc_move_prefix. Key and Value are
// []byte, base64 in JSON, so a SQLite page round-trips byte-for-byte.
type movePrefixOpWire struct {
	Type     string `json:"type"`
	Key      []byte `json:"key"`
	Value    []byte `json:"value,omitempty"`
	RangeEnd []byte `json:"range_end,omitempty"`
	LeaseID  int64  `json:"lease_id,omitempty"`
}

// movePrefixCompareWire is one compare of qrdb_mvcc_move_prefix.
type movePrefixCompareWire struct {
	Key            []byte `json:"key"`
	Target         string `json:"target"`
	Result         string `json:"result"`
	Value          []byte `json:"value,omitempty"`
	Version        int64  `json:"version,omitempty"`
	CreateRevision int64  `json:"create_revision,omitempty"`
	ModRevision    int64  `json:"mod_revision,omitempty"`
}

// MovePrefix moves every live key under req.Src to req.Dst in one revision
// with req.Ops, once req.Compares hold and the live count under Src equals
// req.ExpectedKeys, and returns that revision.
func (t *Transport) MovePrefix(ctx context.Context, req *transport.MovePrefixRequest) (int64, error) {
	if t.closed.Load() {
		return 0, &transport.ClosedError{}
	}
	if req == nil {
		return 0, &transport.RequestError{Method: "MovePrefix", Err: &transport.NilRequestError{}}
	}

	args := map[string]any{
		"src":           base64.StdEncoding.EncodeToString(req.Src),
		"dst":           base64.StdEncoding.EncodeToString(req.Dst),
		"expected_keys": req.ExpectedKeys,
	}
	if len(req.Ops) > 0 {
		ops := make([]movePrefixOpWire, len(req.Ops))
		for i, op := range req.Ops {
			ops[i] = movePrefixOpWire{Type: sdkOpTypeToString[op.Type], Key: op.Key, Value: op.Value, RangeEnd: op.RangeEnd, LeaseID: op.LeaseID}
		}
		data, err := json.Marshal(ops)
		if err != nil {
			return 0, &transport.RequestError{Method: "MovePrefix", Err: &jsonEncodeError{Err: err}}
		}
		args["ops"] = string(data)
	}
	if len(req.Compares) > 0 {
		compares := make([]movePrefixCompareWire, len(req.Compares))
		for i, c := range req.Compares {
			compares[i] = movePrefixCompareWire{
				Key: c.Key, Target: sdkCompareTargetToString[c.Target], Result: sdkCompareResultToString[c.Result], Value: c.Value,
				Version: c.Version, CreateRevision: c.CreateRevision, ModRevision: c.ModRevision,
			}
		}
		data, err := json.Marshal(compares)
		if err != nil {
			return 0, &transport.RequestError{Method: "MovePrefix", Err: &jsonEncodeError{Err: err}}
		}
		args["compares"] = string(data)
	}

	text, err := t.callTool(ctx, "qrdb_mvcc_move_prefix", args)
	if err != nil {
		return 0, err
	}
	var raw struct {
		Revision int64 `json:"revision"`
	}
	if jsonErr := json.Unmarshal([]byte(text), &raw); jsonErr != nil {
		return 0, &transport.RequestError{Method: "MovePrefix", Err: &jsonDecodeError{Err: jsonErr}}
	}
	return raw.Revision, nil
}
