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

package sdk

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// scoperTransport is a minimal transport that records the database it was
// scoped to and implements transport.DatabaseScoper.
type scoperTransport struct {
	database string
	refuse   error
}

func (s *scoperTransport) Connect(context.Context) error { return nil }
func (s *scoperTransport) Close() error                  { return nil }
func (s *scoperTransport) Healthy(context.Context) bool  { return true }
func (s *scoperTransport) Request(context.Context, string, any, any) error {
	return nil
}

func (s *scoperTransport) WithDatabase(database string) (transport.Transport, error) {
	if s.refuse != nil {
		return nil, s.refuse
	}
	return &scoperTransport{database: transport.NormalizeDatabaseScope(database)}, nil
}

// plainTransport does NOT implement DatabaseScoper.
type plainTransport struct{}

func (plainTransport) Connect(context.Context) error                   { return nil }
func (plainTransport) Close() error                                    { return nil }
func (plainTransport) Healthy(context.Context) bool                    { return true }
func (plainTransport) Request(context.Context, string, any, any) error { return nil }

func TestDatabaseScoped_SystemIsTheReceiver(t *testing.T) {
	for _, tr := range []transport.Transport{&scoperTransport{}, plainTransport{}} {
		c, _ := NewClient(tr)
		for _, name := range []string{"", "system"} {
			scoped, err := c.DatabaseScoped(name)
			if err != nil || scoped != c {
				t.Fatalf("DatabaseScoped(%q) over %T = %v, %v; want the receiver", name, tr, scoped, err)
			}
		}
	}
}

// A transport that cannot scope is refused, never answered with the default
// database under the requested name.
func TestDatabaseScoped_UnsupportedTransportRefuses(t *testing.T) {
	c, _ := NewClient(plainTransport{})
	scoped, err := c.DatabaseScoped("db2")
	var unsupported *transport.DatabaseScopeUnsupportedError
	if !errors.As(err, &unsupported) || scoped != nil {
		t.Fatalf("DatabaseScoped over a non-scoper = %v, %v; want *transport.DatabaseScopeUnsupportedError", scoped, err)
	}
}

func TestDatabaseScoped_ClosedClient(t *testing.T) {
	c, _ := NewClient(&scoperTransport{})
	_ = c.Close()
	var closed *ClientClosedError
	if _, err := c.DatabaseScoped("db2"); !errors.As(err, &closed) {
		t.Fatalf("err = %v, want *ClientClosedError", err)
	}
}
