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
	"encoding/json"
	"net/http"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// Transaction request/response types
// ---------------------------------------------------------------------------

// transactionRequest is the JSON payload for POST /tx.
type transactionRequest struct {
	Compares   []compareJSON `json:"compares"`
	SuccessOps []opJSON      `json:"success_ops"`
	FailureOps []opJSON      `json:"failure_ops"`
}

// compareJSON is the JSON representation of a transaction compare precondition.
type compareJSON struct {
	Key            string `json:"key"`
	Target         string `json:"target"`
	Result         string `json:"result"`
	Value          string `json:"value,omitempty"`
	Version        int64  `json:"version,omitempty"`
	CreateRevision int64  `json:"create_revision,omitempty"`
	ModRevision    int64  `json:"mod_revision,omitempty"`
}

// opJSON is the JSON representation of an operation in a transaction or batch.
type opJSON struct {
	Type     string `json:"type"`
	Key      string `json:"key"`
	Value    string `json:"value,omitempty"`
	RangeEnd string `json:"range_end,omitempty"`
	LeaseID  int64  `json:"lease_id,omitempty"`
}

// transactionResponse is the JSON payload returned by POST /tx.
type transactionResponseJSON struct {
	Succeeded bool             `json:"succeeded"`
	Revision  int64            `json:"revision"`
	Responses []opResponseJSON `json:"responses"`
}

// opResponseJSON is the JSON representation of an operation result.
type opResponseJSON struct {
	Kvs       []*kvResponse `json:"kvs,omitempty"`
	Count     int64         `json:"count,omitempty"`
	Succeeded bool          `json:"succeeded"`
}

// ---------------------------------------------------------------------------
// Transaction handler
// ---------------------------------------------------------------------------

// handleTransaction handles POST /tx -- executes an atomic compare-and-swap
// transaction. The request body contains compares (preconditions), success
// operations, and failure operations.
func (s *Server) handleTransaction(w http.ResponseWriter, r *http.Request) {
	if s.txSvc == nil {
		writeErrorJSON(w, http.StatusNotImplemented, &ServiceNotImplementedError{Service: "transaction"})
		return
	}

	var req transactionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	compares := make([]service.Compare, len(req.Compares))
	for i, c := range req.Compares {
		compares[i] = service.Compare{
			Key:            []byte(c.Key),
			Target:         parseCompareTarget(c.Target),
			Result:         parseCompareResult(c.Result),
			Value:          []byte(c.Value),
			Version:        c.Version,
			CreateRevision: c.CreateRevision,
			ModRevision:    c.ModRevision,
		}
	}

	successOps := jsonToOps(req.SuccessOps)
	failureOps := jsonToOps(req.FailureOps)

	resp, err := s.txSvc.Execute(r.Context(), compares, successOps, failureOps)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	responses := make([]opResponseJSON, len(resp.Responses))
	for i, opResp := range resp.Responses {
		responses[i] = opResponseJSON{
			Kvs:       kvsToResponse(opResp.Kvs),
			Count:     opResp.Count,
			Succeeded: opResp.Succeeded,
		}
	}

	writeJSON(w, transactionResponseJSON{
		Succeeded: resp.Succeeded,
		Revision:  resp.Revision,
		Responses: responses,
	})
}

// ---------------------------------------------------------------------------
// JSON-to-service conversion helpers
// ---------------------------------------------------------------------------

// jsonToOps converts a slice of opJSON to service.Op values.
func jsonToOps(ops []opJSON) []service.Op {
	result := make([]service.Op, len(ops))
	for i, o := range ops {
		result[i] = service.Op{
			Type:     parseOpType(o.Type),
			Key:      []byte(o.Key),
			Value:    []byte(o.Value),
			RangeEnd: []byte(o.RangeEnd),
			LeaseID:  o.LeaseID,
		}
	}
	return result
}

// compareTargetMap maps JSON string values to CompareTarget constants.
var compareTargetMap = map[string]service.CompareTarget{
	"version":         service.CompareVersion,
	"create_revision": service.CompareCreateRevision,
	"mod_revision":    service.CompareModRevision,
	"value":           service.CompareValue,
}

// compareResultMap maps JSON string values to CompareResult constants.
var compareResultMap = map[string]service.CompareResult{
	"equal":     service.CompareEqual,
	"greater":   service.CompareGreater,
	"less":      service.CompareLess,
	"not_equal": service.CompareNotEqual,
}

// opTypeMap maps JSON string values to OpType constants.
var restOpTypeMap = map[string]service.OpType{
	"put":    service.OpPut,
	"get":    service.OpGet,
	"delete": service.OpDelete,
	"range":  service.OpRange,
}

// parseCompareTarget converts a JSON string to a CompareTarget constant.
func parseCompareTarget(s string) service.CompareTarget {
	if target, ok := compareTargetMap[s]; ok {
		return target
	}
	return service.CompareVersion
}

// parseCompareResult converts a JSON string to a CompareResult constant.
func parseCompareResult(s string) service.CompareResult {
	if result, ok := compareResultMap[s]; ok {
		return result
	}
	return service.CompareEqual
}

// parseOpType converts a JSON string to an OpType constant.
func parseOpType(s string) service.OpType {
	if opType, ok := restOpTypeMap[s]; ok {
		return opType
	}
	return service.OpPut
}
