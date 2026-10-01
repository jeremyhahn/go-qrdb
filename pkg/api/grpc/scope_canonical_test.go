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

package grpc

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// The metadata's database is bound as the canonical tenant.DatabaseFromContext
// scope too, so context-scoped services (the RAG store) see the same database
// on gRPC and Unix as on QUIC and MCP.
func TestDatabaseScopeUnaryInterceptor_BindsCanonicalScope(t *testing.T) {
	var canonical string
	handler := func(ctx context.Context, _ any) (any, error) {
		canonical = tenant.DatabaseFromContext(ctx)
		return nil, nil
	}
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs(MetadataDatabase, "db2"))
	if _, err := DatabaseScopeUnaryInterceptor(ctx, nil, &grpc.UnaryServerInfo{}, handler); err != nil {
		t.Fatalf("interceptor error: %v", err)
	}
	if canonical != "db2" {
		t.Fatalf("tenant.DatabaseFromContext = %q, want db2", canonical)
	}
}
