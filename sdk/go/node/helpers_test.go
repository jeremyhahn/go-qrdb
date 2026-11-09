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

package node

import (
	"context"
	"errors"
	"testing"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/keydelivery"
	"github.com/jeremyhahn/go-qrdb/pkg/volume"
)

// newBareStandaloneNode starts an in-memory standalone node with every
// network protocol off.
func newBareStandaloneNode(t *testing.T) *Node {
	t.Helper()
	cfg := qrdbconfig.DefaultConfig()
	cfg.Raft.DataDir = t.TempDir()
	cfg.Databases = []qrdbconfig.DatabaseConfig{{Name: "default", Backend: string(database.BackendMemory)}}
	cfg.Protocols.REST, cfg.Protocols.GRPC, cfg.Protocols.QUIC = false, false, false
	cfg.Protocols.Unix, cfg.Protocols.MCP = false, false
	n, err := NewStandaloneNode(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("NewStandaloneNode: %v", err)
	}
	t.Cleanup(func() { _ = n.Close() })
	return n
}

// refusingVolumeKeyProvider is a volume.KeyProvider that never hands out a key.
type refusingVolumeKeyProvider struct{}

var errVolumeKeyRefused = errors.New("volume key refused")

func (refusingVolumeKeyProvider) VolumeKey(context.Context, string, string, string) (volume.VolumeKey, error) {
	return volume.VolumeKey{}, errVolumeKeyRefused
}

var _ volume.KeyProvider = refusingVolumeKeyProvider{}

// stubVolume is a volume.Volume whose Usage and Close fail on demand.
type stubVolume struct {
	root     string
	usageErr error
	closeErr error
	health   volume.Health
	closed   bool
}

func (v *stubVolume) Root() string { return v.root }

func (v *stubVolume) Usage(context.Context) (volume.Usage, error) {
	return volume.Usage{}, v.usageErr
}

func (v *stubVolume) Health(context.Context) volume.Health { return v.health }

func (v *stubVolume) Close(context.Context) error {
	if v.closed {
		return nil
	}
	v.closed = true
	return v.closeErr
}

var _ volume.Volume = (*stubVolume)(nil)

// stubDecrypter is a keydelivery.Decrypter that is never asked to decrypt.
type stubDecrypter struct{ keydelivery.Decrypter }
