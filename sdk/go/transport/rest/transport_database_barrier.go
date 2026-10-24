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
	"net/url"
	"time"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// restDatabaseBarrierStatus is the on-wire JSON shape returned by the database
// barrier-status endpoint (initialized_at is an RFC3339 string).
type restDatabaseBarrierStatus struct {
	Sealed         bool                   `json:"sealed"`
	Strategy       string                 `json:"strategy"`
	HardwareBacked bool                   `json:"hardware_backed"`
	InitializedAt  string                 `json:"initialized_at"`
	ClusterSeal    *transport.ClusterSeal `json:"cluster_seal,omitempty"`
}

// SealDatabase seals a database's barrier. Maps to POST /v1/databases/{name}/seal.
func (t *Transport) SealDatabase(ctx context.Context, name string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	path := "/v1/databases/" + url.PathEscape(name) + "/seal"
	return t.postJSONNoContent(ctx, "SealDatabase", path, struct{}{})
}

// UnsealDatabase unseals a database's barrier. Maps to POST /v1/databases/{name}/unseal.
func (t *Transport) UnsealDatabase(ctx context.Context, name, secret string) error {
	if t.closed.Load() {
		return &transport.ClosedError{}
	}
	ctx, err := t.guardSecret(ctx, transport.DatabaseUnsealSecret(secret))
	if err != nil {
		return err
	}
	path := "/v1/databases/" + url.PathEscape(name) + "/unseal"
	return t.postJSONNoContent(ctx, "UnsealDatabase", path, map[string]string{"secret": secret})
}

// DatabaseBarrierStatus returns a database barrier's status. Maps to
// GET /v1/databases/{name}/barrier/status.
func (t *Transport) DatabaseBarrierStatus(ctx context.Context, name string) (*transport.DatabaseBarrierStatus, error) {
	if t.closed.Load() {
		return nil, &transport.ClosedError{}
	}
	path := "/v1/databases/" + url.PathEscape(name) + "/barrier/status"
	var wire restDatabaseBarrierStatus
	if err := t.getJSONDecode(ctx, "DatabaseBarrierStatus", path, &wire); err != nil {
		return nil, err
	}
	out := &transport.DatabaseBarrierStatus{
		Sealed:         wire.Sealed,
		Strategy:       wire.Strategy,
		HardwareBacked: wire.HardwareBacked,
		ClusterSeal:    wire.ClusterSeal,
	}
	if wire.InitializedAt != "" {
		if ts, err := time.Parse(time.RFC3339, wire.InitializedAt); err == nil {
			out.InitializedAt = ts
		}
	}
	return out, nil
}
