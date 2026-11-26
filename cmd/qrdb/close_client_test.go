//go:build !integration

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

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// TestCloseCLIClient_ClosesTheConnectedClient requires the CLI to close the
// client its root command connected once the command has run, so a QUIC
// connection is closed rather than left to the server's idle timeout.
func TestCloseCLIClient_ClosesTheConnectedClient(t *testing.T) {
	origClient, origBase := client, baseClient
	t.Cleanup(func() { client, baseClient = origClient, origBase })

	tr, err := embedded.New(&noopKVService{}, newMockAdminService())
	if err != nil {
		t.Fatalf("embedded.New: %v", err)
	}
	cl, err := sdk.NewClient(tr)
	if err != nil {
		t.Fatalf("sdk.NewClient: %v", err)
	}
	client, baseClient = cl, cl

	closeCLIClient()

	if client != nil || baseClient != nil {
		t.Fatalf("closeCLIClient left client=%v baseClient=%v, want both nil", client, baseClient)
	}
	var closed *sdk.ClientClosedError
	if _, err := cl.DatabaseScoped("any"); !errors.As(err, &closed) {
		t.Fatalf("DatabaseScoped after closeCLIClient: err = %v, want ClientClosedError", err)
	}
}

// TestCloseCLIClient_NoClient requires closeCLIClient to be a no-op for a
// command that built no client (version, help, enroll).
func TestCloseCLIClient_NoClient(t *testing.T) {
	origClient, origBase := client, baseClient
	t.Cleanup(func() { client, baseClient = origClient, origBase })

	client, baseClient = nil, nil
	closeCLIClient()
}
