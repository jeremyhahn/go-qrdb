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

package quic

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
)

// deleteRecordingFactorSvc records the Delete it receives and answers with
// err.
type deleteRecordingFactorSvc struct {
	mockUserAuthFactorSvc
	factorID string
	force    bool
	err      error
}

func (m *deleteRecordingFactorSvc) Delete(_ context.Context, factorID string, force bool) error {
	m.factorID, m.force = factorID, force
	return m.err
}

func TestHandleUserAuthFactorDelete_Success(t *testing.T) {
	svc := &deleteRecordingFactorSvc{}
	srv := NewServer(nil, nil, "localhost:0", WithUserAuthFactorService(svc))
	payload, _ := json.Marshal(quicUserFactorDeleteRequest{FactorID: "f1", Force: true})
	resp, err := srv.handleUserAuthFactorDelete(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var out map[string]string
	if err := json.Unmarshal(resp, &out); err != nil || out["status"] != "deleted" {
		t.Fatalf("response = %s (%v), want status deleted", resp, err)
	}
	if svc.factorID != "f1" || !svc.force {
		t.Fatalf("service saw (%q, %v), want (f1, true)", svc.factorID, svc.force)
	}
}

func TestHandleUserAuthFactorDelete_ServiceError(t *testing.T) {
	svc := &deleteRecordingFactorSvc{err: &auth.FactorRevokedError{FactorID: "f1"}}
	srv := NewServer(nil, nil, "localhost:0", WithUserAuthFactorService(svc))
	payload, _ := json.Marshal(quicUserFactorDeleteRequest{FactorID: "f1"})
	if _, err := srv.handleUserAuthFactorDelete(context.Background(), payload); !errors.Is(err, auth.ErrFactorRevoked) {
		t.Fatalf("want ErrFactorRevoked, got %v", err)
	}
}

func TestHandleUserAuthFactorDelete_NilService(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0")
	payload, _ := json.Marshal(quicUserFactorDeleteRequest{FactorID: "f1"})
	_, err := srv.handleUserAuthFactorDelete(context.Background(), payload)
	var snc *ServiceNotConfiguredError
	if !errors.As(err, &snc) {
		t.Fatalf("expected ServiceNotConfiguredError, got %T", err)
	}
}

func TestHandleUserAuthFactorDelete_BadPayload(t *testing.T) {
	srv := NewServer(nil, nil, "localhost:0", WithUserAuthFactorService(&deleteRecordingFactorSvc{}))
	_, err := srv.handleUserAuthFactorDelete(context.Background(), []byte("{bad"))
	var ue *UnmarshalError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UnmarshalError, got %T", err)
	}
}
