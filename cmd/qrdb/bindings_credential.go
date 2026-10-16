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

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/secure"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// credentialBindingValue converts a retrieved credential to the string the
// Wails frontend contract carries and destroys buf on every path. The frontend
// bridge serialises the value to JSON for the webview, so this is the output
// boundary where the credential leaves protected memory; nothing before it
// holds the value in plain memory.
func credentialBindingValue(buf *secure.Buffer) (string, error) {
	defer buf.Destroy()
	var value string
	err := buf.WithBytes(func(b []byte) error {
		//secure:unwiped the string a bound Retrieve method returns to the Wails bridge, which JSON-encodes it for the webview after the method returns
		value = string(b)
		return nil
	})
	return value, err
}

// CredentialBinding exposes named-credential submission, storage, and
// versioned-rotation management to the Wails frontend, mirroring the SDK
// CredentialClient.
type CredentialBinding struct {
	logger *sdk.ConcreteLogger
	conn   *ConnectionBinding
}

// NewCredentialBinding creates a new CredentialBinding backed by the given
// connection.
func NewCredentialBinding(log *sdk.ConcreteLogger, conn *ConnectionBinding) *CredentialBinding {
	return &CredentialBinding{logger: log, conn: conn}
}

// Submit submits a credential value for the named credential, notifying any
// blocked retrievers for the manual strategy or delegating to storage for
// the barrier strategy.
func (b *CredentialBinding) Submit(name, value string) error {
	b.logger.Debug("Submit called", "name", name)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("Submit failed", "error", err)
		return err
	}
	if err := cl.Credentials().CredentialSubmit(context.Background(), &transport.CredentialSubmitRequest{
		Name:  name,
		Value: []byte(value),
	}); err != nil {
		b.logger.Error("Submit failed", "name", name, "error", err)
		return err
	}
	b.logger.Debug("Submit completed", "name", name)
	return nil
}

// Store persists a credential value under the given name.
func (b *CredentialBinding) Store(name, value string) error {
	b.logger.Debug("Store called", "name", name)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("Store failed", "error", err)
		return err
	}
	if err := cl.Credentials().CredentialStore(context.Background(), name, []byte(value)); err != nil {
		b.logger.Error("Store failed", "name", name, "error", err)
		return err
	}
	b.logger.Debug("Store completed", "name", name)
	return nil
}

// Retrieve returns the credential value for the given name. For the manual
// strategy this blocks until the credential is submitted.
func (b *CredentialBinding) Retrieve(name string) (string, error) {
	b.logger.Debug("Retrieve called", "name", name)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("Retrieve failed", "error", err)
		return "", err
	}
	buf, err := cl.Credentials().CredentialRetrieveBuffer(context.Background(), name)
	if err != nil {
		b.logger.Error("Retrieve failed", "name", name, "error", err)
		return "", err
	}
	value, err := credentialBindingValue(buf)
	if err != nil {
		b.logger.Error("Retrieve failed", "name", name, "error", err)
		return "", err
	}
	b.logger.Debug("Retrieve completed", "name", name)
	return value, nil
}

// List returns the sorted list of every stored credential name. Values are
// never returned.
func (b *CredentialBinding) List() ([]string, error) {
	b.logger.Debug("List called")
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("List failed", "error", err)
		return nil, err
	}
	resp, err := cl.Credentials().CredentialList(context.Background(), &transport.CredentialListRequest{})
	if err != nil {
		b.logger.Error("List failed", "error", err)
		return nil, err
	}
	b.logger.Debug("List completed", "count", len(resp.Names))
	return resp.Names, nil
}

// Strategy returns the active credential management strategy.
func (b *CredentialBinding) Strategy() (string, error) {
	b.logger.Debug("Strategy called")
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("Strategy failed", "error", err)
		return "", err
	}
	resp, err := cl.Credentials().CredentialStrategy(context.Background())
	if err != nil {
		b.logger.Error("Strategy failed", "error", err)
		return "", err
	}
	b.logger.Debug("Strategy completed", "strategy", resp.Strategy)
	return resp.Strategy, nil
}

// Delete removes a named credential and all of its non-current historical
// versions.
func (b *CredentialBinding) Delete(name string) error {
	b.logger.Debug("Delete called", "name", name)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("Delete failed", "error", err)
		return err
	}
	if err := cl.Credentials().CredentialDelete(context.Background(), name); err != nil {
		b.logger.Error("Delete failed", "name", name, "error", err)
		return err
	}
	b.logger.Debug("Delete completed", "name", name)
	return nil
}

// Rotate atomically replaces the current value of a credential, optionally
// keeping the previous version retrievable for a grace period.
func (b *CredentialBinding) Rotate(req *transport.CredentialRotateRequest) (*transport.CredentialRotateResponse, error) {
	b.logger.Debug("Rotate called", "name", req.Name)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("Rotate failed", "error", err)
		return nil, err
	}
	resp, err := cl.Credentials().CredentialRotate(context.Background(), req)
	if err != nil {
		b.logger.Error("Rotate failed", "name", req.Name, "error", err)
		return nil, err
	}
	b.logger.Debug("Rotate completed", "name", req.Name, "newVersion", resp.NewVersion)
	return resp, nil
}

// ListVersions returns the version history (current + non-expired grace)
// for a credential. Values are never returned.
func (b *CredentialBinding) ListVersions(name string) (*transport.CredentialListVersionsResponse, error) {
	b.logger.Debug("ListVersions called", "name", name)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("ListVersions failed", "error", err)
		return nil, err
	}
	resp, err := cl.Credentials().CredentialListVersions(context.Background(), &transport.CredentialListVersionsRequest{Name: name})
	if err != nil {
		b.logger.Error("ListVersions failed", "name", name, "error", err)
		return nil, err
	}
	b.logger.Debug("ListVersions completed", "name", name, "count", len(resp.Versions))
	return resp, nil
}

// RetrieveVersion returns the stored credential value for a specific
// version. Use version == 0 for the current version.
func (b *CredentialBinding) RetrieveVersion(name string, version uint64) (string, error) {
	b.logger.Debug("RetrieveVersion called", "name", name, "version", version)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("RetrieveVersion failed", "error", err)
		return "", err
	}
	buf, err := cl.Credentials().CredentialRetrieveVersionBuffer(context.Background(), &transport.CredentialRetrieveVersionRequest{
		Name:    name,
		Version: version,
	})
	if err != nil {
		b.logger.Error("RetrieveVersion failed", "name", name, "version", version, "error", err)
		return "", err
	}
	value, err := credentialBindingValue(buf)
	if err != nil {
		b.logger.Error("RetrieveVersion failed", "name", name, "version", version, "error", err)
		return "", err
	}
	b.logger.Debug("RetrieveVersion completed", "name", name, "version", version)
	return value, nil
}

// GCExpired reaps historical versions whose grace period has elapsed for
// the named credential. Returns the count reaped.
func (b *CredentialBinding) GCExpired(name string) (int, error) {
	b.logger.Debug("GCExpired called", "name", name)
	cl, err := b.conn.loadClient()
	if err != nil {
		b.logger.Error("GCExpired failed", "error", err)
		return 0, err
	}
	resp, err := cl.Credentials().CredentialGCExpired(context.Background(), &transport.CredentialGCRequest{Name: name})
	if err != nil {
		b.logger.Error("GCExpired failed", "name", name, "error", err)
		return 0, err
	}
	b.logger.Debug("GCExpired completed", "name", name, "reaped", resp.Reaped)
	return resp.Reaped, nil
}
