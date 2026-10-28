// Copyright (c) 2026 Jeremy Hahn
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

//go:build codec_json && raft && qrdb_ts && qrdb_vector && qrdb_mysql && qrdb_pgsql && qrdb_etcd

package mcp

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/api/mcp/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/pin/pintest"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/sealtest"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// TestHandleBarrierChangePassphrase_OverlappingChangeIsAborted drives a
// passphrase change into a barrier on which another change is in flight:
// the real refusal, not an injected one, is an ABORTED tool error carrying
// the reason that names seal.ErrRootKeyWriteInProgress, so a client can
// tell it apart and retry.
func TestHandleBarrierChangePassphrase_OverlappingChangeIsAborted(t *testing.T) {
	const tool = "qrdb_barrier_change_passphrase"
	barrier, release := sealtest.PassphraseChangeInFlight(t, "first", "second")
	handler := handleBarrierChangePassphrase(service.NewBarrierService(barrier), pintest.NewBackend(t))

	_, err := handler(context.Background(), buildCallToolRequest(t, tool, map[string]any{
		"current": "first",
		"next":    "third",
	}))
	if !errors.Is(err, seal.ErrRootKeyWriteInProgress) {
		t.Fatalf("err = %v, want ErrRootKeyWriteInProgress", err)
	}
	if code := ServiceErrorCode(err); code != wire.CodeAborted {
		t.Fatalf("code = %v, want %v", code, wire.CodeAborted)
	}
	env, ok := classifiedToolError(tool, err).StructuredContent.(*wire.ErrorEnvelope)
	if !ok || env.Error.Reason != transport.ReasonBarrierRootKeyWriteInProgress {
		t.Fatalf("envelope = %+v, want reason %q", env, transport.ReasonBarrierRootKeyWriteInProgress)
	}
	if got := transport.RefusalError(env.Error.Reason, env.Error.Metadata); !errors.Is(got, seal.ErrRootKeyWriteInProgress) {
		t.Fatalf("RefusalError(%q) = %v, want ErrRootKeyWriteInProgress", env.Error.Reason, got)
	}
	release()
}
