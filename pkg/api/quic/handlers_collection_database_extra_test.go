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

//go:build codec_json && raft && qrdb_ts && qrdb_vector && qrdb_mysql && qrdb_pgsql && qrdb_etcd

package quic

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
)

// ---------------------------------------------------------------------------
// handleCollectionGetInfo - missing UnmarshalError test
// ---------------------------------------------------------------------------

func TestHandleCollectionGetInfo_UnmarshalError(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithCollectionService(&mockCollectionService{}))
	_, err := srv.handleCollectionGetInfo(context.Background(), []byte{0xff, 0xfe})
	if err == nil {
		t.Fatal("expected unmarshal error")
	}
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// handleCollectionList - missing UnmarshalError test
// ---------------------------------------------------------------------------

func TestHandleCollectionList_UnmarshalError(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithCollectionService(&mockCollectionService{}))
	_, err := srv.handleCollectionList(context.Background(), []byte{0xff, 0xfe})
	if err == nil {
		t.Fatal("expected unmarshal error")
	}
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// handleCollectionDrop - missing UnmarshalError test
// ---------------------------------------------------------------------------

func TestHandleCollectionDrop_UnmarshalError(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithCollectionService(&mockCollectionService{}))
	_, err := srv.handleCollectionDrop(context.Background(), []byte{0xff, 0xfe})
	if err == nil {
		t.Fatal("expected unmarshal error")
	}
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// handleCollectionGetDocument - missing UnmarshalError test
// ---------------------------------------------------------------------------

func TestHandleCollectionGetDocument_UnmarshalError(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithCollectionService(&mockCollectionService{}))
	_, err := srv.handleCollectionGetDocument(context.Background(), []byte{0xff, 0xfe})
	if err == nil {
		t.Fatal("expected unmarshal error")
	}
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// handleCollectionDeleteDocument - missing UnmarshalError test
// ---------------------------------------------------------------------------

func TestHandleCollectionDeleteDocument_UnmarshalError(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithCollectionService(&mockCollectionService{}))
	_, err := srv.handleCollectionDeleteDocument(context.Background(), []byte{0xff, 0xfe})
	if err == nil {
		t.Fatal("expected unmarshal error")
	}
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// handleCollectionListDocuments - missing UnmarshalError test
// ---------------------------------------------------------------------------

func TestHandleCollectionListDocuments_UnmarshalError(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithCollectionService(&mockCollectionService{}))
	_, err := srv.handleCollectionListDocuments(context.Background(), []byte{0xff, 0xfe})
	if err == nil {
		t.Fatal("expected unmarshal error")
	}
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// handleDatabaseUpdateEtcd - missing ServiceError and UnmarshalError tests
// ---------------------------------------------------------------------------

func TestHandleDatabaseUpdateEtcd_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "update etcd error"}
	dbSvc := &mockDatabaseService{
		updateEtcdFn: func(_ string, _ *database.EtcdConfig) (*database.DatabaseInfo, error) { return nil, svcErr },
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithDatabaseService(dbSvc))
	payload, _ := json.Marshal(map[string]interface{}{"name": "db", "enabled": true, "listenAddr": ":2379"})
	_, err := srv.handleDatabaseUpdateEtcd(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
	var te *testError
	if !errors.As(err, &te) {
		t.Fatalf("expected *testError, got %T", err)
	}
}

func TestHandleDatabaseUpdateEtcd_UnmarshalError(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithDatabaseService(&mockDatabaseService{}))
	_, err := srv.handleDatabaseUpdateEtcd(context.Background(), []byte{0xff, 0xfe, 0x00})
	if err == nil {
		t.Fatal("expected unmarshal error")
	}
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// handleDatabaseUpdateRaft - missing ServiceError and UnmarshalError tests
// ---------------------------------------------------------------------------

func TestHandleDatabaseUpdateRaft_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "update raft error"}
	dbSvc := &mockDatabaseService{
		updateRaftFn: func(_ string, _ *database.DatabaseRaftConfig) (*database.DatabaseInfo, error) { return nil, svcErr },
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithDatabaseService(dbSvc))
	raftCfg := database.DatabaseRaftConfig{ShardID: 1}
	payload, _ := json.Marshal(map[string]interface{}{"name": "db", "raft": raftCfg})
	_, err := srv.handleDatabaseUpdateRaft(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
	var te *testError
	if !errors.As(err, &te) {
		t.Fatalf("expected *testError, got %T", err)
	}
}

func TestHandleDatabaseUpdateRaft_UnmarshalError(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithDatabaseService(&mockDatabaseService{}))
	_, err := srv.handleDatabaseUpdateRaft(context.Background(), []byte{0xff, 0xfe, 0x00})
	if err == nil {
		t.Fatal("expected unmarshal error")
	}
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// handleDatabaseUpdateBarrier - missing ServiceError and UnmarshalError tests
// ---------------------------------------------------------------------------

func TestHandleDatabaseUpdateBarrier_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "update barrier error"}
	dbSvc := &mockDatabaseService{
		updateBarrierFn: func(_ string, _ *database.DatabaseBarrierConfig) (*database.DatabaseInfo, error) { return nil, svcErr },
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithDatabaseService(dbSvc))
	barrierCfg := database.DatabaseBarrierConfig{Mode: "shamir"}
	payload, _ := json.Marshal(map[string]interface{}{"name": "db", "barrier": barrierCfg})
	_, err := srv.handleDatabaseUpdateBarrier(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
	var te *testError
	if !errors.As(err, &te) {
		t.Fatalf("expected *testError, got %T", err)
	}
}

func TestHandleDatabaseUpdateBarrier_UnmarshalError(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithDatabaseService(&mockDatabaseService{}))
	_, err := srv.handleDatabaseUpdateBarrier(context.Background(), []byte{0xff, 0xfe, 0x00})
	if err == nil {
		t.Fatal("expected unmarshal error")
	}
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// handleDatabaseUpdateConnection - missing ServiceError and UnmarshalError tests
// ---------------------------------------------------------------------------

func TestHandleDatabaseUpdateConnection_ServiceError(t *testing.T) {
	svcErr := &testError{msg: "update connection error"}
	dbSvc := &mockDatabaseService{
		updateConnectionFn: func(_ string, _ *database.DatabaseConnectionConfig) (*database.DatabaseInfo, error) {
			return nil, svcErr
		},
	}
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithDatabaseService(dbSvc))
	connCfg := database.DatabaseConnectionConfig{Transport: "grpc", Address: "h:9000"}
	payload, _ := json.Marshal(map[string]interface{}{"name": "db", "connection": connCfg})
	_, err := srv.handleDatabaseUpdateConnection(context.Background(), payload)
	if err == nil {
		t.Fatal("expected error")
	}
	var te *testError
	if !errors.As(err, &te) {
		t.Fatalf("expected *testError, got %T", err)
	}
}

func TestHandleDatabaseUpdateConnection_UnmarshalError(t *testing.T) {
	srv := newTestServer(&mockKVService{}, &mockAdminService{}, WithDatabaseService(&mockDatabaseService{}))
	_, err := srv.handleDatabaseUpdateConnection(context.Background(), []byte{0xff, 0xfe, 0x00})
	if err == nil {
		t.Fatal("expected unmarshal error")
	}
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected *UnmarshalError, got %T", err)
	}
}
