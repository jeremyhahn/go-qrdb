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
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/apimodule"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
)

// gaugeModule is the module a composing host writes against the SDK alone:
// an active-connection gauge fed by the ModuleConnObserver hook.
type gaugeModule struct {
	sdk.BaseModule
	active atomic.Int64
}

func (m *gaugeModule) Name() string                   { return "gauge" }
func (m *gaugeModule) ConnOpened(sdk.ModuleTransport) { m.active.Add(1) }
func (m *gaugeModule) ConnClosed(sdk.ModuleTransport) { m.active.Add(-1) }

// Compile-time: a module written against the SDK names is the server's
// observer — the aliases are the pkg/apimodule declarations, not copies.
var (
	_ sdk.ServiceModule      = (*gaugeModule)(nil)
	_ sdk.ModuleConnObserver = (*gaugeModule)(nil)
	_ apimodule.ConnObserver = (*gaugeModule)(nil)
	_ apimodule.Transport    = sdk.ModuleTransportQUIC
)

// TestSDK_ModuleTransports_AreTheServerTransports pins each SDK transport
// name to the value the server reports.
func TestSDK_ModuleTransports_AreTheServerTransports(t *testing.T) {
	pairs := map[sdk.ModuleTransport]apimodule.Transport{
		sdk.ModuleTransportGRPC:  apimodule.TransportGRPC,
		sdk.ModuleTransportUnix:  apimodule.TransportUnix,
		sdk.ModuleTransportREST:  apimodule.TransportREST,
		sdk.ModuleTransportHTTP3: apimodule.TransportHTTP3,
		sdk.ModuleTransportMCP:   apimodule.TransportMCP,
		sdk.ModuleTransportQUIC:  apimodule.TransportQUIC,
	}
	if len(pairs) != 6 {
		t.Fatalf("%d distinct transports, want 6", len(pairs))
	}
	for sdkValue, pkgValue := range pairs {
		if sdkValue != pkgValue {
			t.Errorf("sdk %q != server %q", sdkValue, pkgValue)
		}
	}
}

// TestSDK_ModuleConnObserver_IsPickedUpByTheServer is the behaviour: the
// server's collection step (what every listener runs at Start) recognises an
// SDK-written module as an observer, and events it routes reach the gauge,
// balanced.
func TestSDK_ModuleConnObserver_IsPickedUpByTheServer(t *testing.T) {
	m := &gaugeModule{}
	observers := apimodule.ConnObserversOf([]sdk.ServiceModule{m})
	if len(observers) != 1 {
		t.Fatalf("the server found %d observers in an SDK-written module, want 1", len(observers))
	}
	closeA := observers.Open(sdk.ModuleTransportMCP)
	closeB := observers.Open(sdk.ModuleTransportQUIC)
	if got := m.active.Load(); got != 2 {
		t.Fatalf("active = %d after two opens, want 2", got)
	}
	closeA()
	closeB()
	closeB()
	if got := m.active.Load(); got != 0 {
		t.Fatalf("active = %d after closing both, want 0", got)
	}
}
