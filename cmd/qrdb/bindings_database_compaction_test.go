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
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func TestDatabaseBinding_UpdateCompaction_Success(t *testing.T) {
	conn := newDatabaseTestConn(t, &bindingDatabaseService{})
	b := NewDatabaseBinding(testLogger(t), conn)

	info, err := b.UpdateCompaction("events", &transport.UpdateDatabaseCompactionRequest{
		Compaction: &transport.DatabaseCompactionConfig{Mode: "revision", RetainRevisions: 5},
	})
	if err != nil {
		t.Fatalf("UpdateCompaction: %v", err)
	}
	if info.Name != "events" || info.Compaction == nil || info.Compaction.RetainRevisions != 5 {
		t.Fatalf("info = %+v", info)
	}
}

func TestDatabaseBinding_UpdateCompaction_NotConnected(t *testing.T) {
	b := NewDatabaseBinding(testLogger(t), NewConnectionBinding(testLogger(t)))
	var nce *NotConnectedError
	if _, err := b.UpdateCompaction("events", &transport.UpdateDatabaseCompactionRequest{}); !errors.As(err, &nce) {
		t.Fatalf("not connected = %v", err)
	}
}
