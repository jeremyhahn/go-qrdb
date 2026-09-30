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

//go:build !integration

package etcd

import (
	"context"
	"errors"
	"testing"

	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jeremyhahn/go-qrdb/pkg/kv"
)

// historylessMVCC is an MVCC service over a state machine with no revision
// history, as the composer declares for the default pebble state machine.
type historylessMVCC struct {
	*mockMVCCKVServicer
	puts int
}

func (*historylessMVCC) KeepsRevisionHistory() bool { return false }

func newHistorylessMVCC() *historylessMVCC {
	h := &historylessMVCC{}
	h.mockMVCCKVServicer = &mockMVCCKVServicer{
		putWithLeaseFn: func(_ context.Context, key string, value []byte, _ int64) (*kv.KeyValue, error) {
			h.puts++
			return &kv.KeyValue{Key: []byte(key), Value: value}, nil
		},
	}
	return h
}

// An etcd put over a store with no revision history is refused before it
// is written: its answer would have to be revision 0.
func TestPut_RefusesAStoreWithoutRevisionHistory(t *testing.T) {
	mvcc := newHistorylessMVCC()
	srv := newTestKVServer(mvcc.mockMVCCKVServicer, nil)
	srv.mvccSvc = mvcc

	_, err := srv.Put(context.Background(), &pb.PutRequest{Key: []byte("k"), Value: []byte("v")})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("Put = %v, want FailedPrecondition", err)
	}
	if mvcc.puts != 0 {
		t.Fatalf("the refused put was written %d times", mvcc.puts)
	}
}

// The etcd server does not start over a store with no revision history. TLS
// and an authorizer are configured (newStartable) so the auth-required gate
// Start also applies does not mask the one this test is about.
func TestServer_StartRequiresRevisionHistory(t *testing.T) {
	srv := newStartable("db", "127.0.0.1:0", WithMVCC(newHistorylessMVCC()))
	err := srv.Start()
	var required *RevisionHistoryRequiredError
	if !errors.As(err, &required) {
		t.Fatalf("Start = %v, want *RevisionHistoryRequiredError", err)
	}
	if required.Error() == "" {
		t.Fatal("empty error message")
	}
}
