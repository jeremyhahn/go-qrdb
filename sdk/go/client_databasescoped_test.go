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

package sdk_test

import (
	"context"
	"testing"

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// scoperTransport is a minimal transport that records the database it was
// scoped to and implements transport.DatabaseScoper.
type scoperTransport struct {
	database string
}

func (s *scoperTransport) Connect(context.Context) error { return nil }
func (s *scoperTransport) Close() error                  { return nil }
func (s *scoperTransport) Healthy(context.Context) bool  { return true }
func (s *scoperTransport) Request(context.Context, string, any, any) error {
	return nil
}

func (s *scoperTransport) WithDatabase(database string) transport.Transport {
	return &scoperTransport{database: transport.NormalizeDatabaseScope(database)}
}

// plainTransport does NOT implement DatabaseScoper.
type plainTransport struct{}

func (plainTransport) Connect(context.Context) error                   { return nil }
func (plainTransport) Close() error                                    { return nil }
func (plainTransport) Healthy(context.Context) bool                    { return true }
func (plainTransport) Request(context.Context, string, any, any) error { return nil }

func TestDatabaseScoped_DelegatesToTransport(t *testing.T) {
	c, err := sdk.NewClient(&scoperTransport{})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	scoped := c.DatabaseScoped("db2")
	if scoped == c {
		t.Fatal("expected a distinct scoped client")
	}
	// The scoped client wraps a scoper transport carrying db2.
	st, ok := scoped.TransportForTest().(*scoperTransport)
	if !ok {
		t.Fatalf("unexpected transport type %T", scoped.TransportForTest())
	}
	if st.database != "db2" {
		t.Fatalf("expected db2, got %q", st.database)
	}
}

func TestDatabaseScoped_SystemIsUnscoped(t *testing.T) {
	c, _ := sdk.NewClient(&scoperTransport{})
	scoped := c.DatabaseScoped("system")
	st := scoped.TransportForTest().(*scoperTransport)
	if st.database != "" {
		t.Fatalf("system should normalize to unscoped, got %q", st.database)
	}
}

func TestDatabaseScoped_UnsupportedTransportReturnsSelf(t *testing.T) {
	c, _ := sdk.NewClient(plainTransport{})
	if got := c.DatabaseScoped("db2"); got != c {
		t.Fatal("expected the same client when transport is not a DatabaseScoper")
	}
}
