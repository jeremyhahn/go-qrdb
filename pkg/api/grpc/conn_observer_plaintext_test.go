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

package grpc

import (
	"errors"
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/apimodule"
)

// plaintextObserver counts connection events; only its counts matter.
type plaintextObserver struct {
	apimodule.BaseModule
	opened, closed atomic.Int64
}

func (*plaintextObserver) Name() string                     { return "plaintext-observer" }
func (o *plaintextObserver) ConnOpened(apimodule.Transport) { o.opened.Add(1) }
func (o *plaintextObserver) ConnClosed(apimodule.Transport) { o.closed.Add(1) }

var _ apimodule.ConnObserver = (*plaintextObserver)(nil)

// TestConnObserver_NoPlaintextConnectionToMiss: the observer is wired through
// the server's TLS credentials, unconditionally, because there is no plaintext
// path for it to miss. With an observer registered and no TLS config, Start
// still fails closed before binding a listener, so nothing is ever accepted
// unobserved.
func TestConnObserver_NoPlaintextConnectionToMiss(t *testing.T) {
	obs := &plaintextObserver{}
	srv := NewServer(newMockKVService(), newMockAdminService(), "127.0.0.1:0",
		WithServiceModules([]apimodule.ServiceModule{obs}))

	err := srv.Start()
	var tlsErr *TLSNotConfiguredError
	if !errors.As(err, &tlsErr) {
		srv.Stop()
		t.Fatalf("plaintext Start with an observer: got %T (%v), want *TLSNotConfiguredError", err, err)
	}
	if srv.Addr() != "127.0.0.1:0" && srv.Addr() != "" {
		t.Fatalf("a plaintext listener was bound at %s despite the refusal", srv.Addr())
	}
	if o, c := obs.opened.Load(), obs.closed.Load(); o != 0 || c != 0 {
		t.Fatalf("observer saw opened=%d closed=%d on a server that never served", o, c)
	}
}
