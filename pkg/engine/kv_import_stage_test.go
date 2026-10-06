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

package engine

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/importstage"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

// The KV engine keeps no leases, so it refuses every staging command the way
// the MVCC engine and the pebble state machine refuse one when built without
// a lessor -- a capability refusal in the result, never a halted apply and
// never "unsupported operation" -- and writes nothing a lease could later
// reclaim.
func TestKVEngine_ImportStage_RefusedAsLeaseNotConfigured(t *testing.T) {
	for name, cmd := range map[string]protocol.ImportStageCommand{
		"set meta": {Kind: protocol.ImportStageSetMeta, SessionID: "sess1", LeaseID: 7,
			Body: []byte("the metadata"), TenantID: "acme", MaxSessions: 4},
		"set chunk": {Kind: protocol.ImportStageSetChunk, SessionID: "sess1", LeaseID: 7,
			Seq: 0, Body: []byte("a chunk"), MetaBody: []byte("updated metadata")},
	} {
		t.Run(name, func(t *testing.T) {
			stg := memory.NewStorage()
			defer func() { _ = stg.Close() }()
			eng := NewKVEngine(stg)

			results := make([]sm.Result, 1)
			if err := eng.Update(context.Background(), []sm.Entry{importStageEntry(1, cmd)}, results); err != nil {
				t.Fatalf("Update must not halt apply on an engine that keeps no leases: %v", err)
			}
			msg, kind, ok := protocol.DecodeAppErrorKind(results[0].Data)
			if !ok || kind != protocol.AppErrorCapability {
				t.Fatalf("result = ok:%v kind:%d %q; want an AppErrorCapability refusal", ok, kind, msg)
			}
			staged, err := stg.List([]byte(importstage.TablePrefix))
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if len(staged) != 0 {
				t.Fatalf("a refused staging command wrote %d records", len(staged))
			}
		})
	}
}

// A malformed payload is a decode error, exactly as in the MVCC engine's
// handler, not a refusal that would hide a corrupt log entry.
func TestKVEngine_ImportStage_MalformedPayloadIsFatal(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()
	eng := NewKVEngine(stg)

	// DecodeImportStage requires at least 3 bytes.
	cmd := []byte{protocol.OpImportStage, byte(protocol.ImportStageSetMeta)}
	err := eng.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: cmd}}, make([]sm.Result, 1))
	var ice *protocol.InvalidCommandError
	if !errors.As(err, &ice) {
		t.Fatalf("Update = %T (%v); want *protocol.InvalidCommandError", err, err)
	}
}
