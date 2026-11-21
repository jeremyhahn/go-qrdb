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

package grpc

import (
	"context"
	"testing"

	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func TestGRPC_ListPermissionCatalog_MatchesRegistry(t *testing.T) {
	want, err := transport.PermissionCatalog()
	if err != nil {
		t.Fatalf("PermissionCatalog: %v", err)
	}
	resp, err := (&qrdbServiceServer{}).ListPermissionCatalog(context.Background(), &emptypb.Empty{})
	if err != nil {
		t.Fatalf("ListPermissionCatalog: %v", err)
	}
	got := resp.GetPermissions()
	if len(got) != len(want) {
		t.Fatalf("got %d permissions, want %d", len(got), len(want))
	}
	for i, p := range got {
		if p.GetName() != want[i].Name || p.GetScope() != string(want[i].Scope) ||
			p.GetResource() != want[i].Resource || p.GetAction() != want[i].Action {
			t.Fatalf("entry %d = %v, want %+v", i, p, want[i])
		}
	}
}
