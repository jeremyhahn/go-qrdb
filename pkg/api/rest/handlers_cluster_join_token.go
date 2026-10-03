// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package rest

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// mintJoinTokenRequestTimeout bounds the mint-token endpoint: a single
// replicated-store write, never a shard snapshot or a network fetch, so it
// shares the tighter control-plane budget rather than backupRequestTimeout.
const mintJoinTokenRequestTimeout = 10 * time.Second

// mintJoinTokenRequest is the JSON payload for POST /v1/cluster/join-tokens.
// TTLSeconds <= 0 uses clusterenroll.DefaultJoinTokenTTL.
type mintJoinTokenRequest struct {
	NodeID     string `json:"node_id"`
	TTLSeconds int64  `json:"ttl_seconds,omitempty"`
}

// mintJoinTokenResponse is the JSON payload returned by POST
// /v1/cluster/join-tokens. Token is returned exactly once -- there is no
// "list minted tokens" read path, by the same write-only-credential
// reasoning every other admission secret in this codebase follows -- so a
// caller that loses this response has to mint a fresh token.
type mintJoinTokenResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// handleMintJoinToken handles POST /v1/cluster/join-tokens: mints a
// single-use, identity-bound member-side CSR join token for the admin-named
// node identity. permission.ClusterJoinToken (ScopeCross, platform-only)
// gates this route -- see registerRoutes -- so only an identity of the
// system tenant reaches this handler at all.
func (s *Server) handleMintJoinToken(w http.ResponseWriter, r *http.Request) {
	minter, ok := s.adminSvc.(service.ClusterJoinTokenMinter)
	if !ok {
		writeErrorJSON(w, http.StatusNotImplemented, &service.ClusterJoinTokenNotConfiguredError{})
		return
	}

	var req mintJoinTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})
		return
	}

	ttl, err := service.ParseJoinTokenTTLSeconds(req.TTLSeconds)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), mintJoinTokenRequestTimeout)
	defer cancel()
	token, expiresAt, err := minter.MintJoinToken(ctx, req.NodeID, ttl)
	if err != nil {
		writeErrorJSON(w, mapHTTPError(err), err)
		return
	}

	// The response carries a single-use admission secret returned exactly
	// once; an intermediary or browser cache holding onto it would be a
	// second place it could leak from, so it must never be cached.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(mintJoinTokenResponse{Token: token, ExpiresAt: expiresAt})
}
