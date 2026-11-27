//go:build qrdb_embedded && qrdb_vlog

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

package main

import (
	"slices"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/vlogwire"
)

// Every VlogBinding call names its database: a log made in orders is not
// listed, rotated or expired through the default database, and the other
// way round.
//
// Standalone only: the desktop's raft state machine serves no verifiable
// log yet (CreateLog answers "statemachine: vlog not configured"), so a
// raft run has no log to scope; recorded in the #1606 PR notes.
func TestVlogBinding_DatabaseScoped(t *testing.T) {
	embeddedModes(t, func(t *testing.T, conn *ConnectionBinding) {
		if t.Name() == "TestVlogBinding_DatabaseScoped/raft" {
			t.Skip("the desktop raft state machine serves no verifiable log")
		}
		b := NewVlogBinding(testLogger(t), conn)
		for db, log := range map[string]string{"orders": "olog", "system": "slog"} {
			if _, err := b.Create(db, &vlogwire.CreateRequest{
				Log: log, Origin: "example.com/" + log, AllowExpireBodies: true,
				SigningKeyType: vlog.SignatureEd25519,
			}); err != nil {
				t.Fatalf("Create %s in %s: %v", log, db, err)
			}
			if _, err := b.Append(db, &vlogwire.AppendRequest{Log: log, Entries: [][]byte{[]byte("a"), []byte("b")}}); err != nil {
				t.Fatalf("Append %s in %s: %v", log, db, err)
			}
		}
		for db, own := range map[string]string{"orders": "olog", "system": "slog", "": "slog"} {
			other := "olog"
			if own == "olog" {
				other = "slog"
			}
			list, err := b.List(db)
			if err != nil {
				t.Fatalf("List(%q): %v", db, err)
			}
			if !slices.Equal(list.Logs, []string{own}) {
				t.Fatalf("List(%q) = %v, want [%s]", db, list.Logs, own)
			}
			if _, err := b.RotateKey(db, &vlogwire.RotateKeyRequest{Log: other, SigningKeyType: vlog.SignatureEd25519}); err == nil {
				t.Fatalf("RotateKey(%q, %s) reached another database's log", db, other)
			}
			if _, err := b.ExpireBodies(db, &vlogwire.ExpireRequest{Log: other, Cutoff: 1}); err == nil {
				t.Fatalf("ExpireBodies(%q, %s) reached another database's log", db, other)
			}
		}
		for db, own := range map[string]string{"orders": "olog", "system": "slog"} {
			if _, err := b.RotateKey(db, &vlogwire.RotateKeyRequest{Log: own, SigningKeyType: vlog.SignatureEd25519}); err != nil {
				t.Fatalf("RotateKey(%q, %s): %v", db, own, err)
			}
			if _, err := b.ExpireBodies(db, &vlogwire.ExpireRequest{Log: own, Cutoff: 1}); err != nil {
				t.Fatalf("ExpireBodies(%q, %s): %v", db, own, err)
			}
		}
	})
}
