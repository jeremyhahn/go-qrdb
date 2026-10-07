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
	"context"
	"sync/atomic"

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
)

// ConnectionBinding manages the SDK client connection. All other
// bindings hold a reference to this and use loadClient() to obtain
// the active client. When a ConnectionManagerBinding is wired, client
// lookups are delegated to the manager's active connection. The direct
// atomic pointer is retained for backward compatibility with the CLI
// path which does not use the connection manager.
type ConnectionBinding struct {
	logger  *sdk.ConcreteLogger
	client  atomic.Pointer[sdk.Client]
	manager *ConnectionManagerBinding
	ctx     context.Context
}

// NewConnectionBinding creates a new ConnectionBinding.
func NewConnectionBinding(log *sdk.ConcreteLogger) *ConnectionBinding {
	return &ConnectionBinding{
		logger: log,
		ctx:    context.Background(),
	}
}

// startup is called by Wails on application startup. It stores the
// Wails context for use in event emission.
func (c *ConnectionBinding) startup(ctx context.Context) {
	c.logger.Debug("ConnectionBinding startup")
	c.ctx = ctx
}

// shutdown is called by Wails on application shutdown. It disconnects
// the client if connected.
func (c *ConnectionBinding) shutdown(_ context.Context) {
	c.logger.Debug("ConnectionBinding shutdown")
	if cl := c.client.Load(); cl != nil {
		_ = cl.Close()
	}
}

// SetManager wires a ConnectionManagerBinding so that loadClient
// delegates to the manager's active connection.
func (c *ConnectionBinding) SetManager(m *ConnectionManagerBinding) {
	c.logger.Debug("SetManager called", "hasManager", m != nil)
	c.manager = m
}

// SetClient atomically replaces the current SDK client. This is used
// by the embedded server binding and connection wizard to inject a
// client after transport construction.
func (c *ConnectionBinding) SetClient(cl *sdk.Client) {
	c.logger.Debug("SetClient called", "hasClient", cl != nil)
	c.client.Store(cl)
}

// loadClient returns the current SDK client or a NotConnectedError
// if no client has been set. When a ConnectionManagerBinding is wired,
// the client is resolved from the manager's active connection.
func (c *ConnectionBinding) loadClient() (*sdk.Client, error) {
	if c.manager != nil {
		return c.manager.ActiveClient()
	}
	cl := c.client.Load()
	if cl == nil {
		return nil, &NotConnectedError{}
	}
	return cl, nil
}

// IsConnected returns true when a client connection is active.
func (c *ConnectionBinding) IsConnected() bool {
	if c.manager != nil {
		cl, _ := c.manager.ActiveClient()
		connected := cl != nil
		c.logger.Debug("IsConnected called", "connected", connected, "source", "manager")
		return connected
	}
	connected := c.client.Load() != nil
	c.logger.Debug("IsConnected called", "connected", connected)
	return connected
}

// Disconnect closes the current client connection and clears the
// client reference.
func (c *ConnectionBinding) Disconnect() error {
	c.logger.Debug("Disconnect called")
	cl := c.client.Load()
	if cl == nil {
		return nil
	}
	err := cl.Close()
	c.client.Store(nil)
	if err != nil {
		c.logger.Error("Disconnect failed", "error", err)
		return err
	}
	c.logger.Debug("Disconnect completed")
	return nil
}

// GetVersion returns the build version information.
func (c *ConnectionBinding) GetVersion() map[string]string {
	c.logger.Debug("GetVersion called")
	return map[string]string{
		"version":   version,
		"gitCommit": gitCommit,
		"buildDate": buildDate,
	}
}
