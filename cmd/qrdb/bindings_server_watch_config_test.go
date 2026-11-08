//go:build qrdb_embedded

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
	"context"
	"errors"
	"testing"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

func TestEmbeddedWatchConfig_NilIsTheZeroValue(t *testing.T) {
	if got := embeddedWatchConfig(nil); got != (qrdbconfig.WatchConfig{}) {
		t.Fatalf("embeddedWatchConfig(nil) = %+v, want the zero value", got)
	}
}

func TestEmbeddedWatchConfig_CarriesBothKeys(t *testing.T) {
	got := embeddedWatchConfig(&EmbeddedWatchConfig{BufferSize: 64, MaxWatchers: 3})
	if got.BufferSize != 64 || got.MaxWatchers != 3 {
		t.Fatalf("embeddedWatchConfig = %+v, want BufferSize 64, MaxWatchers 3", got)
	}
}

func TestStartEmbedded_WatchMaxWatchersRefusesTheWatchPastTheCap(t *testing.T) {
	log := testLogger(t)
	conn := NewConnectionBinding(log)
	esb := NewEmbeddedServerBinding(log, conn, NewSQLBinding(log, conn))
	cfg := &EmbeddedConfig{
		StorageBackend: "memory",
		DataDir:        t.TempDir(),
		Watch:          &EmbeddedWatchConfig{MaxWatchers: 1},
	}
	if err := esb.StartEmbedded(cfg); err != nil {
		t.Fatalf("StartEmbedded: %v", err)
	}
	defer func() { _ = esb.StopEmbedded() }()
	cl, err := conn.loadClient()
	if err != nil {
		t.Fatalf("loadClient: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := cl.Watch().WatchPrefix(ctx, "w/", transport.WatchOptions{}); err != nil {
		t.Fatalf("first watch: %v", err)
	}
	var le *transport.WatcherLimitError
	if _, err := cl.Watch().WatchPrefix(ctx, "w/", transport.WatchOptions{}); !errors.As(err, &le) {
		t.Fatalf("second watch at max_watchers 1: err = %v (%T), want *transport.WatcherLimitError", err, err)
	}
}
