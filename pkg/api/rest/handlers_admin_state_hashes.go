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
	"net/http"
	"strconv"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// handleGetStateHashes handles GET /admin/state-hashes: the applied index and
// state digest of every replica this node runs, or of those whose shard is
// named by a repeated shard query parameter (?shard=1&shard=7). It is
// answered by this node, never forwarded. The route requires
// admin:state_hash, which is ScopeCross: system tenant only.
func (s *Server) handleGetStateHashes(w http.ResponseWriter, r *http.Request) {
	hasher, ok := s.adminSvc.(service.ReplicaStateHasher)
	if !ok {
		writeErrorJSON(w, http.StatusNotImplemented, &service.ReplicaStateHashesUnsupportedError{})
		return
	}
	shardIDs, perr := parseShardParams(r.URL.Query()["shard"])
	if perr != nil {
		writeErrorJSON(w, mapHTTPError(perr), perr)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), apiRequestTimeout)
	defer cancel()

	resp, err := hasher.StateHashes(ctx, shardIDs)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}
	writeJSON(w, resp)
}

// parseShardParams parses repeated shard query parameter values as shard IDs.
func parseShardParams(values []string) ([]uint64, *InvalidParameterError) {
	if len(values) == 0 {
		return nil, nil
	}
	ids := make([]uint64, 0, len(values))
	for _, v := range values {
		id, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			return nil, &InvalidParameterError{Param: "shard", Err: err}
		}
		ids = append(ids, id)
	}
	return ids, nil
}
