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

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
)

func TestGRPC_DatabaseCompaction_UpdateAndClear(t *testing.T) {
	client, cleanup := startTestServerFull(t, WithDatabaseService(newRealDatabaseService(t)))
	defer cleanup()
	ctx := context.Background()

	if _, err := client.DatabaseCreate(ctx, &pb.DatabaseCreateRequest{Name: "events", Engine: "kv", Backend: "memory"}); err != nil {
		t.Fatalf("DatabaseCreate: %v", err)
	}

	updated, err := client.DatabaseUpdateCompaction(ctx, &pb.DatabaseUpdateCompactionRequest{
		Name:       "events",
		Compaction: &pb.DatabaseCompactionConfig{Mode: "revision", RetainRevisions: 9},
	})
	if err != nil {
		t.Fatalf("DatabaseUpdateCompaction: %v", err)
	}
	if c := updated.GetCompaction(); c.GetMode() != "revision" || c.GetRetainRevisions() != 9 {
		t.Fatalf("updated compaction = %v", c)
	}
	got, err := client.DatabaseGet(ctx, &pb.DatabaseGetRequest{Name: "events"})
	if err != nil || got.GetCompaction().GetMode() != "revision" {
		t.Fatalf("DatabaseGet compaction = %v, %v", got.GetCompaction(), err)
	}

	cleared, err := client.DatabaseUpdateCompaction(ctx, &pb.DatabaseUpdateCompactionRequest{Name: "events"})
	if err != nil {
		t.Fatalf("DatabaseUpdateCompaction(clear): %v", err)
	}
	if cleared.GetCompaction() != nil {
		t.Fatalf("cleared compaction = %v, want unset", cleared.GetCompaction())
	}
}

func TestGRPC_DatabaseCompaction_Refusals(t *testing.T) {
	client, cleanup := startTestServerFull(t, WithDatabaseService(newRealDatabaseService(t)))
	defer cleanup()
	ctx := context.Background()

	if _, err := client.DatabaseCreate(ctx, &pb.DatabaseCreateRequest{Name: "events", Engine: "kv", Backend: "memory"}); err != nil {
		t.Fatalf("DatabaseCreate: %v", err)
	}
	cases := []struct {
		req  *pb.DatabaseUpdateCompactionRequest
		want codes.Code
	}{
		{&pb.DatabaseUpdateCompactionRequest{Name: "events", Compaction: &pb.DatabaseCompactionConfig{Mode: "weekly"}}, codes.InvalidArgument},
		{&pb.DatabaseUpdateCompactionRequest{Name: "events", Compaction: &pb.DatabaseCompactionConfig{Mode: "period", RetainPeriod: "1ms"}}, codes.InvalidArgument},
		{&pb.DatabaseUpdateCompactionRequest{Name: "missing", Compaction: &pb.DatabaseCompactionConfig{Mode: "revision", RetainRevisions: 1}}, codes.NotFound},
	}
	for _, c := range cases {
		_, err := client.DatabaseUpdateCompaction(ctx, c.req)
		if st, _ := status.FromError(err); st.Code() != c.want {
			t.Errorf("DatabaseUpdateCompaction(%v) code = %s, want %s", c.req, st.Code(), c.want)
		}
	}
}

func TestGRPC_DatabaseCompaction_NilService(t *testing.T) {
	client, cleanup := startTestServerFull(t)
	defer cleanup()
	_, err := client.DatabaseUpdateCompaction(context.Background(), &pb.DatabaseUpdateCompactionRequest{Name: "db"})
	if st, _ := status.FromError(err); st.Code() != codes.Unimplemented {
		t.Fatalf("code = %s, want Unimplemented", st.Code())
	}
}
