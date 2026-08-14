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

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// SealDatabase seals a database's encryption barrier via the injected service.
func (t *Transport) SealDatabase(ctx context.Context, name string) error {
	if t.dbSvc == nil {
		return &NilServiceError{Service: "DatabaseServicer"}
	}
	return t.dbSvc.SealDatabase(ctx, name)
}

// UnsealDatabase unseals a database's encryption barrier via the injected service.
func (t *Transport) UnsealDatabase(ctx context.Context, name, secret string) error {
	if t.dbSvc == nil {
		return &NilServiceError{Service: "DatabaseServicer"}
	}
	return t.dbSvc.UnsealDatabase(ctx, name, seal.Credentials{Secret: []byte(secret)})
}

// DatabaseBarrierStatus returns a database barrier's status via the injected service.
func (t *Transport) DatabaseBarrierStatus(ctx context.Context, name string) (*transport.DatabaseBarrierStatus, error) {
	if t.dbSvc == nil {
		return nil, &NilServiceError{Service: "DatabaseServicer"}
	}
	st, err := t.dbSvc.DatabaseBarrierStatus(ctx, name)
	if err != nil {
		return nil, err
	}
	return &transport.DatabaseBarrierStatus{
		Sealed:         st.Sealed,
		Strategy:       string(st.Strategy),
		HardwareBacked: st.HardwareBacked,
		InitializedAt:  st.InitializedAt,
	}, nil
}
