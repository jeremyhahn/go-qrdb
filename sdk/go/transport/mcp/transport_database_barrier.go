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
	"time"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// SealDatabase seals a database's barrier via the qrdb_database_seal tool.
func (t *Transport) SealDatabase(ctx context.Context, name string) error {
	if _, err := t.callTool(ctx, "qrdb_database_seal", map[string]any{"name": name}); err != nil {
		return &transport.RequestError{Method: "SealDatabase", Err: err}
	}
	return nil
}

// UnsealDatabase unseals a database's barrier via the qrdb_database_unseal tool.
func (t *Transport) UnsealDatabase(ctx context.Context, name, secret string) error {
	ctx, err := t.guardSecret(ctx, transport.DatabaseUnsealSecret(secret))
	if err != nil {
		return err
	}
	args := map[string]any{"name": name}
	putStringSecret(args, "secret", secret)
	if _, err := t.callTool(ctx, "qrdb_database_unseal", args); err != nil {
		return &transport.RequestError{Method: "UnsealDatabase", Err: err}
	}
	return nil
}

// DatabaseBarrierStatus returns a database barrier's status via the
// qrdb_database_barrier_status tool. The tool marshals the service-layer
// seal.BarrierStatus directly (PascalCase, no json tags).
func (t *Transport) DatabaseBarrierStatus(ctx context.Context, name string) (*transport.DatabaseBarrierStatus, error) {
	text, err := t.callTool(ctx, "qrdb_database_barrier_status", map[string]any{"name": name})
	if err != nil {
		return nil, &transport.RequestError{Method: "DatabaseBarrierStatus", Err: err}
	}
	var wire struct {
		Sealed         bool
		Strategy       string
		HardwareBacked bool
		InitializedAt  time.Time
		ClusterSeal    *transport.ClusterSeal `json:"cluster_seal"`
	}
	if jsonErr := json.Unmarshal([]byte(text), &wire); jsonErr != nil {
		return nil, &transport.RequestError{Method: "DatabaseBarrierStatus", Err: &jsonDecodeError{Err: jsonErr}}
	}
	return &transport.DatabaseBarrierStatus{
		Sealed:         wire.Sealed,
		Strategy:       wire.Strategy,
		HardwareBacked: wire.HardwareBacked,
		InitializedAt:  wire.InitializedAt,
		ClusterSeal:    wire.ClusterSeal,
	}, nil
}
