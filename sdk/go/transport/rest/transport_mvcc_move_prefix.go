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
	"bytes"
	"context"
	"encoding/json"
	"net/http"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// movePrefixWireOp is one op of POST /kv/move-prefix. Every byte field is
// []byte, base64 in JSON, so a SQLite page round-trips byte-for-byte.
type movePrefixWireOp struct {
	Type     string `json:"type"`
	Key      []byte `json:"key"`
	Value    []byte `json:"value,omitempty"`
	RangeEnd []byte `json:"range_end,omitempty"`
	LeaseID  int64  `json:"lease_id,omitempty"`
}

// movePrefixWireCompare is one compare of POST /kv/move-prefix.
type movePrefixWireCompare struct {
	Key            []byte `json:"key"`
	Target         string `json:"target"`
	Result         string `json:"result"`
	Value          []byte `json:"value,omitempty"`
	Version        int64  `json:"version,omitempty"`
	CreateRevision int64  `json:"create_revision,omitempty"`
	ModRevision    int64  `json:"mod_revision,omitempty"`
}

type movePrefixWireRequest struct {
	Src          []byte                  `json:"src"`
	Dst          []byte                  `json:"dst"`
	ExpectedKeys int64                   `json:"expected_keys"`
	Ops          []movePrefixWireOp      `json:"ops,omitempty"`
	Compares     []movePrefixWireCompare `json:"compares,omitempty"`
}

type movePrefixWireResponse struct {
	Revision int64 `json:"revision"`
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

	payload := movePrefixWireRequest{Src: req.Src, Dst: req.Dst, ExpectedKeys: req.ExpectedKeys}
	for _, op := range req.Ops {
		payload.Ops = append(payload.Ops, movePrefixWireOp{
			Type: opTypeStringMap[op.Type], Key: op.Key, Value: op.Value, RangeEnd: op.RangeEnd, LeaseID: op.LeaseID,
		})
	}
	for _, c := range req.Compares {
		payload.Compares = append(payload.Compares, movePrefixWireCompare{
			Key: c.Key, Target: compareTargetStringMap[c.Target], Result: compareResultStringMap[c.Result], Value: c.Value,
			Version: c.Version, CreateRevision: c.CreateRevision, ModRevision: c.ModRevision,
		})
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return 0, &transport.RequestError{Method: "MovePrefix", Err: &jsonEncodeError{Err: err}}
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, t.baseURL+"/kv/move-prefix", bytes.NewReader(data))
	if err != nil {
		return 0, &transport.RequestError{Method: "MovePrefix", Err: err}
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := t.client.Do(httpReq)
	if err != nil {
		return 0, &transport.RequestError{Method: "MovePrefix", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return 0, t.decodeErrorResponse("MovePrefix", resp)
	}
	var wireResp movePrefixWireResponse
	if err := json.NewDecoder(resp.Body).Decode(&wireResp); err != nil {
		return 0, &transport.RequestError{Method: "MovePrefix", Err: &jsonDecodeError{Err: err}}
	}
	return wireResp.Revision, nil
}
