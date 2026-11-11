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

package quic

import (
	"context"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
)

func TestE2E_NodeCompaction_Failures(t *testing.T) {
	srv, tr := startE2EServer(t)
	srv.SetResponder("admin.GetNodeCompaction", func(_ []byte) (uint16, []byte) {
		return wire.StatusPermissionDenied, []byte("permission denied")
	})
	srv.SetResponder("admin.SetNodeCompaction", func(_ []byte) (uint16, []byte) {
		return wire.StatusOK, []byte{0xff, 0xff}
	})
	ctx := context.Background()
	if _, err := tr.NodeCompaction(ctx); err == nil {
		t.Fatal("a refused NodeCompaction succeeded")
	}
	if _, err := tr.SetNodeCompaction(ctx, nil); err == nil {
		t.Fatal("an undecodable SetNodeCompaction answer was accepted")
	}
	_ = tr.Close()
	if _, err := tr.NodeCompaction(ctx); err == nil {
		t.Fatal("NodeCompaction on a closed transport succeeded")
	}
	if _, err := tr.SetNodeCompaction(ctx, nil); err == nil {
		t.Fatal("SetNodeCompaction on a closed transport succeeded")
	}
}
