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

package transport

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

func TestIsDataPlanePath(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"/kv", true},
		{"/kv/foo", true},
		{"/kv?prefix=x", true},
		{"/ts/query", true},
		{"/vector/search", true},
		{"/v1/databases", false},
		{"/v1/tenants", false},
		{"/health", false},
		{"/kvother", false}, // not a segment boundary
	}
	for _, c := range cases {
		if got := IsDataPlanePath(c.path); got != c.want {
			t.Errorf("IsDataPlanePath(%q)=%v want %v", c.path, got, c.want)
		}
	}
}

func TestNormalizeDatabaseScope(t *testing.T) {
	if got := NormalizeDatabaseScope("system"); got != "" {
		t.Errorf("system should normalize to empty, got %q", got)
	}
	if got := NormalizeDatabaseScope(""); got != "" {
		t.Errorf("empty should stay empty, got %q", got)
	}
	if got := NormalizeDatabaseScope("db2"); got != "db2" {
		t.Errorf("db2 should be unchanged, got %q", got)
	}
}

func TestDatabaseUnaryClientInterceptor_AppendsMetadata(t *testing.T) {
	var got []string
	invoker := func(ctx context.Context, _ string, _, _ any, _ *grpc.ClientConn, _ ...grpc.CallOption) error {
		md, _ := metadata.FromOutgoingContext(ctx)
		got = md.Get(DatabaseMetadataKey)
		return nil
	}
	err := DatabaseUnaryClientInterceptor("db2")(
		context.Background(), "/qrdb.v1.QRDBService/Put", nil, nil, nil, invoker)
	if err != nil {
		t.Fatalf("interceptor error: %v", err)
	}
	if len(got) != 1 || got[0] != "db2" {
		t.Fatalf("expected metadata [db2], got %v", got)
	}
}

func TestDatabaseStreamClientInterceptor_AppendsMetadata(t *testing.T) {
	var got []string
	streamer := func(ctx context.Context, _ *grpc.StreamDesc, _ *grpc.ClientConn, _ string, _ ...grpc.CallOption) (grpc.ClientStream, error) {
		md, _ := metadata.FromOutgoingContext(ctx)
		got = md.Get(DatabaseMetadataKey)
		return nil, nil
	}
	_, err := DatabaseStreamClientInterceptor("db2")(
		context.Background(), &grpc.StreamDesc{}, nil, "/qrdb.v1.QRDBService/Watch", streamer)
	if err != nil {
		t.Fatalf("interceptor error: %v", err)
	}
	if len(got) != 1 || got[0] != "db2" {
		t.Fatalf("expected metadata [db2], got %v", got)
	}
}
