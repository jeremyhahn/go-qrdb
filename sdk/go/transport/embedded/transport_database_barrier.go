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

package embedded

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/internal/sealconv"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// SealDatabase seals a database's encryption barrier via the injected service.
func (t *Transport) SealDatabase(ctx context.Context, name string) error {
	if err := t.gate("SealDatabase"); err != nil {
		return err
	}
	if err := t.authorize(ctx, "SealDatabase"); err != nil {
		return err
	}
	if t.dbSvc == nil {
		return &NilServiceError{Service: "DatabaseServicer"}
	}
	return t.dbSvc.SealDatabase(ctx, name)
}

// UnsealDatabase unseals a database's encryption barrier via the injected service.
func (t *Transport) UnsealDatabase(ctx context.Context, name, secret string) error {
	if err := t.gate("UnsealDatabase"); err != nil {
		return err
	}
	if err := t.authorize(ctx, "UnsealDatabase"); err != nil {
		return err
	}
	if t.dbSvc == nil {
		return &NilServiceError{Service: "DatabaseServicer"}
	}
	creds, err := seal.CredentialsFor(ctx, []byte(secret))
	if err != nil {
		return delayed("UnsealDatabase", err)
	}
	defer creds.Secret.Destroy()
	return t.dbSvc.UnsealDatabase(ctx, name, creds)
}

// DatabaseBarrierStatus returns a database barrier's status via the injected service.
func (t *Transport) DatabaseBarrierStatus(ctx context.Context, name string) (*transport.DatabaseBarrierStatus, error) {
	if err := t.gate("DatabaseBarrierStatus"); err != nil {
		return nil, err
	}
	if err := t.authorize(ctx, "DatabaseBarrierStatus"); err != nil {
		return nil, err
	}
	if t.dbSvc == nil {
		return nil, &NilServiceError{Service: "DatabaseServicer"}
	}
	st, err := t.dbSvc.DatabaseBarrierStatus(ctx, name)
	if err != nil {
		return nil, delayed("DatabaseBarrierStatus", err)
	}
	return &transport.DatabaseBarrierStatus{
		Sealed:         st.Sealed,
		Strategy:       string(st.Strategy),
		HardwareBacked: st.HardwareBacked,
		InitializedAt:  st.InitializedAt,
		ClusterSeal:    sealconv.FromReport(st.ClusterSeal),
	}, nil
}
