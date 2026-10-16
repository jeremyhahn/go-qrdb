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
	"encoding/json"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/replicaforward"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// conditionFactorServer wires the frame handlers to the real service over a
// store with the guarded writes a conditional add commits through.
func conditionFactorServer(t *testing.T) *Server {
	t.Helper()
	svc, err := service.NewUserAuthFactorService(service.UserAuthFactorServiceConfig{KV: kvstore.NewMemoryKVStore()})
	if err != nil {
		t.Fatalf("NewUserAuthFactorService: %v", err)
	}
	return &Server{userAuthFactorSvc: svc}
}

func addFrame(t *testing.T, s *Server, req quicUserFactorAddRequest) (factorID string, version uint64, err error) {
	t.Helper()
	payload, _ := json.Marshal(req)
	resp, err := s.handleUserAuthFactorAdd(context.Background(), payload)
	if err != nil {
		return "", 0, err
	}
	var out struct {
		Factor           quicUserFactorRecord `json:"factor"`
		FactorSetVersion uint64               `json:"factor_set_version"`
	}
	if uerr := json.Unmarshal(resp, &out); uerr != nil {
		t.Fatalf("decode Add response: %v", uerr)
	}
	return out.Factor.ID, out.FactorSetVersion, nil
}

func TestQUIC_AddConditional_errorFrameCarriesReason(t *testing.T) {
	s := conditionFactorServer(t)

	id, version, err := addFrame(t, s, quicUserFactorAddRequest{UserID: 42, Type: "mtls", Credential: []byte("cred-1"), IfNoActiveType: true})
	if err != nil || version != 1 {
		t.Fatalf("conditional Add = %d, %v; want version 1", version, err)
	}
	_, _, err = addFrame(t, s, quicUserFactorAddRequest{UserID: 42, Type: "mtls", Credential: []byte("cred-2"), IfNoActiveType: true})
	if !errors.Is(err, auth.ErrFactorConditionFailed) {
		t.Fatalf("err = %v, want ErrFactorConditionFailed", err)
	}
	info := errorPayloadOf(err).Info
	if info == nil || info.Domain != replicaforward.ErrorDomain || info.Reason != auth.ReasonFactorConditionFailed {
		t.Fatalf("error frame info = %+v, want the %s refusal", info, auth.ReasonFactorConditionFailed)
	}
	if got := auth.FactorConditionFailedFromMetadata(info.Metadata); got.ExistingFactorID != id || got.CurrentVersion != 1 {
		t.Fatalf("refusal = %+v, want it to name %s at version 1", got, id)
	}
}

func TestQUIC_AddConditional_expectedVersionAndList(t *testing.T) {
	s := conditionFactorServer(t)
	zero := uint64(0)

	if _, _, err := addFrame(t, s, quicUserFactorAddRequest{UserID: 42, Type: "mtls", Credential: []byte("cred-1"), ExpectedFactorSetVersion: &zero}); err != nil {
		t.Fatalf("Add at version 0: %v", err)
	}
	if _, _, err := addFrame(t, s, quicUserFactorAddRequest{UserID: 42, Type: "fido2", Credential: []byte("cred-2"), ExpectedFactorSetVersion: &zero}); !errors.Is(err, auth.ErrFactorConditionFailed) {
		t.Fatalf("stale Add err = %v, want ErrFactorConditionFailed", err)
	}
	if _, version, err := addFrame(t, s, quicUserFactorAddRequest{UserID: 42, Type: "fido2", Credential: []byte("cred-3")}); err != nil || version != 0 {
		t.Fatalf("unconditional Add = %d, %v; want 0, nil", version, err)
	}

	payload, _ := json.Marshal(quicUserFactorListRequest{UserID: 42})
	resp, err := s.handleUserAuthFactorList(context.Background(), payload)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var list struct {
		Total            int    `json:"total"`
		FactorSetVersion uint64 `json:"factor_set_version"`
	}
	if err := json.Unmarshal(resp, &list); err != nil || list.Total != 2 || list.FactorSetVersion != 2 {
		t.Fatalf("List = %+v, %v; want 2 factors at version 2", list, err)
	}
}

func TestQUIC_RotateRefusesAddCondition(t *testing.T) {
	s := conditionFactorServer(t)
	payload, _ := json.Marshal(quicUserFactorRotateRequest{
		OldFactorID: "f-1",
		NewFactor:   quicUserFactorAddRequest{Type: "mtls", Credential: []byte("cred"), IfNoActiveType: true},
	})
	if _, err := s.handleUserAuthFactorRotate(context.Background(), payload); !errors.Is(err, auth.ErrInvalidFactor) {
		t.Fatalf("Rotate with a condition err = %v, want ErrInvalidFactor", err)
	}
}
