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

//go:build qrdb_vlog

package lifecycle_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/vlog"
)

// errDeviceRefused is what the recording provider answers every request with.
var errDeviceRefused = errors.New("device refused")

// recordingHeadSigners is a HeadSignerProvider standing in for a composer's
// hardware one: it records what it is asked and refuses it.
type recordingHeadSigners struct {
	created []vlog.CreateKeyRequest
}

var _ vlog.HeadSignerProvider = (*recordingHeadSigners)(nil)

func (p *recordingHeadSigners) CreateKey(_ context.Context, req vlog.CreateKeyRequest) (vlog.CreatedKey, error) {
	p.created = append(p.created, req)
	return vlog.CreatedKey{}, errDeviceRefused
}

func (*recordingHeadSigners) HeadSigner(context.Context, vlog.HeadSignerRequest) (vlog.HeadSigner, error) {
	return nil, errDeviceRefused
}

// TestBuildServices_HeadSignerProviderCreatesTheLogsKeys: an injected
// provider is the one the log service asks for a new log's key.
func TestBuildServices_HeadSignerProviderCreatesTheLogsKeys(t *testing.T) {
	device := &recordingHeadSigners{}
	node := buildSeamNode(t, lifecycle.Infra{HeadSignerProvider: device})
	if node.Log == nil {
		t.Fatal("Log is nil in a build with the verifiable log compiled in")
	}
	ctx := tenant.WithTenantID(context.Background(), "acme")
	_, err := node.Log.CreateLog(ctx, &service.CreateLogRequest{
		LogRef: service.LogRef{Database: "ledger", Log: "events"}, Origin: "db.example.com/events", SigningKeyType: vlog.SignatureEd25519,
	})
	if !errors.Is(err, errDeviceRefused) {
		t.Fatalf("CreateLog = %v, want the device's refusal", err)
	}
	if len(device.created) != 1 || device.created[0].TenantID != "acme" || device.created[0].Version != vlog.FirstKeyVersion {
		t.Fatalf("device asked to create %+v", device.created)
	}
}

// TestBuildServices_HeadSignerProviderAbsentKeepsTheSoftwareProvider: with no
// provider the log service is still composed, over the software default.
func TestBuildServices_HeadSignerProviderAbsentKeepsTheSoftwareProvider(t *testing.T) {
	node := buildSeamNode(t, lifecycle.Infra{})
	if node.Log == nil {
		t.Fatal("Log is nil with no provider injected; want the software default")
	}
}
