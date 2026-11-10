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
	"errors"
	"testing"

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

func TestHandleDatabaseSeal_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := proto.Marshal(&pb.DatabaseSealRequest{Name: "testdb"})
	_, err := srv.handleDatabaseSeal(context.Background(), payload)
	assertNotConfigured(t, err)
}

func TestHandleDatabaseSeal_InvalidPayload(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0", WithDatabaseService(&mockDatabaseServiceQuic{}))
	_, err := srv.handleDatabaseSeal(context.Background(), []byte{0xff})
	assertUnmarshalError(t, err)
}

func TestHandleDatabaseSeal_Success(t *testing.T) {
	var gotName string
	svc := &mockDatabaseServiceQuic{
		sealFn: func(_ context.Context, name string) error {
			gotName = name
			return nil
		},
	}
	srv := NewServer(nil, nil, "localhost:0", WithDatabaseService(svc))
	payload, _ := proto.Marshal(&pb.DatabaseSealRequest{Name: "testdb"})
	if _, err := srv.handleDatabaseSeal(context.Background(), payload); err != nil {
		t.Fatalf("handleDatabaseSeal failed: %v", err)
	}
	if gotName != "testdb" {
		t.Fatalf("sealed %q, want testdb", gotName)
	}
}

func TestHandleDatabaseSeal_ServiceError(t *testing.T) {
	sentinel := errors.New("seal failed")
	svc := &mockDatabaseServiceQuic{
		sealFn: func(_ context.Context, _ string) error { return sentinel },
	}
	srv := NewServer(nil, nil, "localhost:0", WithDatabaseService(svc))
	payload, _ := proto.Marshal(&pb.DatabaseSealRequest{Name: "testdb"})
	if _, err := srv.handleDatabaseSeal(context.Background(), payload); !errors.Is(err, sentinel) {
		t.Fatalf("expected the service error, got %v", err)
	}
}

func TestHandleDatabaseUnseal_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := proto.Marshal(&pb.DatabaseUnsealRequest{Name: "testdb"})
	_, err := srv.handleDatabaseUnseal(context.Background(), payload)
	assertNotConfigured(t, err)
}

func TestHandleDatabaseUnseal_InvalidPayload(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0", WithDatabaseService(&mockDatabaseServiceQuic{}))
	_, err := srv.handleDatabaseUnseal(context.Background(), []byte{0xff})
	assertUnmarshalError(t, err)
}

func TestHandleDatabaseUnseal_Success(t *testing.T) {
	var (
		gotName   string
		gotSecret bool
	)
	svc := &mockDatabaseServiceQuic{
		unsealFn: func(_ context.Context, name string, creds seal.Credentials) error {
			gotName, gotSecret = name, creds.Secret != nil
			return nil
		},
	}
	srv := NewServer(nil, nil, "localhost:0", WithDatabaseService(svc))
	payload, _ := proto.Marshal(&pb.DatabaseUnsealRequest{Name: "testdb", Secret: "mysecret"})
	if _, err := srv.handleDatabaseUnseal(context.Background(), payload); err != nil {
		t.Fatalf("handleDatabaseUnseal failed: %v", err)
	}
	if gotName != "testdb" || !gotSecret {
		t.Fatalf("unsealed %q, secret carried = %v", gotName, gotSecret)
	}
}

func TestHandleDatabaseUnseal_ServiceError(t *testing.T) {
	sentinel := errors.New("unseal failed")
	svc := &mockDatabaseServiceQuic{
		unsealFn: func(_ context.Context, _ string, _ seal.Credentials) error { return sentinel },
	}
	srv := NewServer(nil, nil, "localhost:0", WithDatabaseService(svc))
	payload, _ := proto.Marshal(&pb.DatabaseUnsealRequest{Name: "testdb", Secret: "bad"})
	if _, err := srv.handleDatabaseUnseal(context.Background(), payload); !errors.Is(err, sentinel) {
		t.Fatalf("expected the service error, got %v", err)
	}
}

func TestHandleDatabaseBarrierStatus_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := proto.Marshal(&pb.DatabaseBarrierStatusRequest{Name: "testdb"})
	_, err := srv.handleDatabaseBarrierStatus(context.Background(), payload)
	assertNotConfigured(t, err)
}

func TestHandleDatabaseBarrierStatus_InvalidPayload(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0", WithDatabaseService(&mockDatabaseServiceQuic{}))
	_, err := srv.handleDatabaseBarrierStatus(context.Background(), []byte{0xff})
	assertUnmarshalError(t, err)
}

func TestHandleDatabaseBarrierStatus_Success(t *testing.T) {
	var gotName string
	svc := &mockDatabaseServiceQuic{
		barrierStatusFn: func(name string) (*seal.BarrierStatus, error) {
			gotName = name
			return &seal.BarrierStatus{Sealed: true, Strategy: "software", HardwareBacked: true}, nil
		},
	}
	srv := NewServer(nil, nil, "localhost:0", WithDatabaseService(svc))
	payload, _ := proto.Marshal(&pb.DatabaseBarrierStatusRequest{Name: "testdb"})
	resp, err := srv.handleDatabaseBarrierStatus(context.Background(), payload)
	if err != nil {
		t.Fatalf("handleDatabaseBarrierStatus failed: %v", err)
	}
	if gotName != "testdb" {
		t.Fatalf("status asked for %q, want testdb", gotName)
	}
	var out pb.DatabaseBarrierStatusResponse
	if err := proto.Unmarshal(resp, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !out.GetSealed() || out.GetStrategy() != "software" || !out.GetHardwareBacked() {
		t.Fatalf("status = %+v", &out)
	}
}

func TestHandleDatabaseBarrierStatus_ServiceError(t *testing.T) {
	sentinel := errors.New("status failed")
	svc := &mockDatabaseServiceQuic{
		barrierStatusFn: func(_ string) (*seal.BarrierStatus, error) { return nil, sentinel },
	}
	srv := NewServer(nil, nil, "localhost:0", WithDatabaseService(svc))
	payload, _ := proto.Marshal(&pb.DatabaseBarrierStatusRequest{Name: "testdb"})
	if _, err := srv.handleDatabaseBarrierStatus(context.Background(), payload); !errors.Is(err, sentinel) {
		t.Fatalf("expected the service error, got %v", err)
	}
}
