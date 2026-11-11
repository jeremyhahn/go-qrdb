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

package unix

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"

	unixserver "github.com/jeremyhahn/go-qrdb/pkg/api/unix"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// startDescriptiveServer starts a unix server over the REAL seal-disabled
// tenant service holding one tenant, "acme".
func startDescriptiveServer(t *testing.T) *Transport {
	t.Helper()
	svc, err := tenant.NewKVTenantService(kvstore.NewMemoryKVStore(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateTenant(context.Background(), "acme", "Acme", tenant.IsolationPrefix); err != nil {
		t.Fatal(err)
	}
	tr, cleanup := startAuthServer(t, unixserver.WithTenantService(svc))
	t.Cleanup(cleanup)
	return tr
}

func TestUnix_UpdateTenant_OverLimitIsInvalidArgument(t *testing.T) {
	tr := startDescriptiveServer(t)
	long := strings.Repeat("d", tenant.MaxDescriptionBytes+1)
	_, err := tr.UpdateTenant(context.Background(), &transport.UpdateTenantRequest{TenantID: "acme", Description: &long})
	var re *transport.RequestError
	if !errors.As(err, &re) || re.StatusCode != int(codes.InvalidArgument) {
		t.Fatalf("over-limit description = %v; want a RequestError carrying InvalidArgument", err)
	}
}
