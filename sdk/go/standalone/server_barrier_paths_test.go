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

package standalone

import (
	"context"
	"errors"
	"testing"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// TestNewServer_UnsetStrategyIsAPassphraseBarrier proves a barrier whose
// strategy is left empty but which carries a passphrase seals with it.
func TestNewServer_UnsetStrategyIsAPassphraseBarrier(t *testing.T) {
	ctx := context.Background()
	srv, err := NewServer(ctx, &ServerConfig{
		Backend: database.BackendPebble,
		DataDir: t.TempDir(),
		Barrier: &BarrierConfig{
			AutoUnseal: true,
			Passphrase: barrierPassphrase(t, "a-passphrase-with-no-strategy"),
		},
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer func() { _ = srv.Shutdown(ctx) }()

	status, err := srv.Client().Barrier().BarrierStatus(ctx)
	if err != nil {
		t.Fatalf("BarrierStatus: %v", err)
	}
	if status.Sealed {
		t.Fatal("barrier sealed after AutoUnseal")
	}
	if status.Strategy == string(seal.StrategyNone) {
		t.Fatal("an unset strategy with a passphrase produced a NONE barrier")
	}
}

// TestRunAutoUnseal_RefusesShamir proves the auto-unseal step never opens a
// Shamir barrier, which only a share-holder quorum may unseal.
func TestRunAutoUnseal_RefusesShamir(t *testing.T) {
	err := (&Server{}).runAutoUnseal(context.Background(), nil, seal.StrategyShamir, &BarrierConfig{}, seal.Credentials{})
	var unsealErr *BarrierUnsealError
	if !errors.As(err, &unsealErr) {
		t.Fatalf("runAutoUnseal(shamir): got %v, want *BarrierUnsealError", err)
	}
	if !errors.Is(err, ErrShamirAutoUnseal) {
		t.Fatalf("runAutoUnseal(shamir): got %v, want ErrShamirAutoUnseal", err)
	}
}

func TestValidateServerConfig_ShamirRefusesAutoUnseal(t *testing.T) {
	cfg := &ServerConfig{
		DataDir: t.TempDir(),
		Barrier: &BarrierConfig{
			Strategy:   seal.StrategyShamir,
			Shamir:     &ShamirConfig{Threshold: 2, Total: 3},
			AutoUnseal: true,
		},
	}
	err := validateServerConfig(cfg, database.BackendMemory)
	var cfgErr *ServerConfigError
	if !errors.As(err, &cfgErr) || cfgErr.Field != "Barrier.AutoUnseal" {
		t.Fatalf("validateServerConfig = %v, want ServerConfigError on Barrier.AutoUnseal", err)
	}
	if want := "standalone: server config: Barrier.AutoUnseal " + cfgErr.Message; err.Error() != want {
		t.Fatalf("Error() = %q, want %q", err.Error(), want)
	}
}

func TestCreateServerStorage_UnsupportedBackendIsConfigError(t *testing.T) {
	_, _, err := createServerStorage(database.BackendType("bogus"), t.TempDir(), nil, qrdbconfig.StorageConfig{})
	var cfgErr *ServerConfigError
	if !errors.As(err, &cfgErr) || cfgErr.Field != "Backend" {
		t.Fatalf("createServerStorage(bogus) = %v (%T), want ServerConfigError on Backend", err, err)
	}
}

// getErrStorage is a real storage whose Get always fails.
type getErrStorage struct {
	storage.Storage
	err error
}

func (s *getErrStorage) Get([]byte) ([]byte, error) { return nil, s.err }

func TestCreateServerEngine_StorageReadFailureIsEngineError(t *testing.T) {
	stg, pdb, err := createServerStorage(database.BackendMemory, t.TempDir(), nil, qrdbconfig.StorageConfig{})
	if err != nil {
		t.Fatalf("createServerStorage(memory): %v", err)
	}
	defer func() { _ = stg.Close() }()

	boom := errors.New("read failed")
	_, err = createServerEngine(database.BackendMemory, &getErrStorage{Storage: stg, err: boom}, pdb, nil, nil)
	var engErr *EngineCreateError
	if !errors.As(err, &engErr) || !errors.Is(err, boom) {
		t.Fatalf("createServerEngine = %v (%T), want EngineCreateError wrapping %v", err, err, boom)
	}
}

func TestBarrierUnsealError_MessageWrapsCause(t *testing.T) {
	cause := errors.New("bad passphrase")
	err := &BarrierUnsealError{Err: cause}
	if err.Error() != "standalone: failed to unseal barrier: bad passphrase" {
		t.Fatalf("Error() = %q", err.Error())
	}
	if !errors.Is(err, cause) {
		t.Fatal("BarrierUnsealError does not unwrap to its cause")
	}
}

func TestRunAutoUnseal_AlreadyUnsealedIsNoError(t *testing.T) {
	ctx := context.Background()
	const pp = "an-already-unsealed-passphrase"
	cfg := &BarrierConfig{
		Strategy:   seal.StrategyPassphrase,
		AutoUnseal: true,
		Passphrase: barrierPassphrase(t, pp),
	}
	srv, err := NewServer(ctx, &ServerConfig{
		Backend: database.BackendPebble,
		DataDir: t.TempDir(),
		Barrier: cfg,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer func() { _ = srv.Shutdown(ctx) }()

	creds := seal.MustCredentials(ctx, []byte(pp))
	defer seal.DestroyCredentials(creds)
	if err := srv.runAutoUnseal(ctx, srv.barrier, seal.StrategyPassphrase, cfg, creds); err != nil {
		t.Fatalf("runAutoUnseal on an unsealed barrier = %v, want nil", err)
	}
}
