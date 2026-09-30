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

package database

import (
	"errors"
	"strings"
	"testing"
)

var etcdOn = &EtcdConfig{Enabled: true, ListenAddr: "127.0.0.1:2379"}

// The etcd wire protocol serves a key-value keyspace, so it is enabled only
// on an engine: kv database -- at creation and on update alike. A timeseries
// or vector database with etcd enabled was accepted and would have served its
// engine's internal keys as etcd keys.
func TestEtcd_RequiresKVEngine(t *testing.T) {
	for _, engine := range []EngineType{EngineTimeSeries, EngineVector} {
		t.Run(string(engine), func(t *testing.T) {
			m := NewManager(nil, t.TempDir())
			t.Cleanup(func() { _ = m.Close() })

			_, err := m.Create(DatabaseConfig{Name: "metrics", Engine: engine, Backend: BackendMemory,
				Protocol: ProtocolConfig{Etcd: etcdOn}})
			var unsupported *EtcdEngineUnsupportedError
			if !errors.As(err, &unsupported) || unsupported.Engine != engine {
				t.Fatalf("Create(%s, etcd) = %v, want *EtcdEngineUnsupportedError", engine, err)
			}
			if !strings.Contains(err.Error(), string(engine)) {
				t.Errorf("error %q does not name the engine", err)
			}

			if _, err := m.Create(DatabaseConfig{Name: "metrics", Engine: engine, Backend: BackendMemory}); err != nil {
				t.Fatalf("Create(%s): %v", engine, err)
			}
			if err := m.UpdateEtcd("metrics", etcdOn); !errors.As(err, &unsupported) {
				t.Fatalf("UpdateEtcd on a %s database = %v, want *EtcdEngineUnsupportedError", engine, err)
			}
			// Disabling is always allowed.
			if err := m.UpdateEtcd("metrics", &EtcdConfig{}); err != nil {
				t.Fatalf("UpdateEtcd(disabled): %v", err)
			}
		})
	}
}

// Any backend serves etcd: the listener resolves the database's namespaced
// engines through the service registry, so backend: raft is not required.
func TestEtcd_AnyBackendOnKVEngine(t *testing.T) {
	m := NewManager(nil, t.TempDir())
	t.Cleanup(func() { _ = m.Close() })
	for _, backend := range []BackendType{BackendMemory, BackendPebble, BackendRaft} {
		name := "kv" + string(backend)
		if _, err := m.Create(DatabaseConfig{Name: name, Engine: EngineKV, Backend: backend,
			Protocol: ProtocolConfig{Etcd: etcdOn}}); err != nil {
			t.Fatalf("Create(kv, %s, etcd): %v", backend, err)
		}
	}
}
