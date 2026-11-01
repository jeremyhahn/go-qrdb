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

//go:build codec_json && raft

package mcp

import (
	"context"
	"errors"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/credentials"
	"github.com/jeremyhahn/go-qrdb/pkg/lock"
	"github.com/jeremyhahn/go-qrdb/pkg/pin"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// Mock: CeremonyServicer
// ---------------------------------------------------------------------------

type covMockCeremonySvc struct {
	initializeFn func(ctx context.Context, ca ceremony.CeremonyCA) (*service.CeremonyInitResult, error)
	statusFn     func(ctx context.Context) (*service.CeremonyStatusInfo, error)
	claimShareFn func(ctx context.Context, username string) ([]byte, error)
	signCSRFn    func(ctx context.Context, username, soPin, csrPEM, role string) (*service.CeremonySignCSRResult, error)
}

var _ service.CeremonyServicer = (*covMockCeremonySvc)(nil)

func newCovMockCeremonySvc() *covMockCeremonySvc { return &covMockCeremonySvc{} }

func (m *covMockCeremonySvc) CeremonyInitialize(ctx context.Context, ca ceremony.CeremonyCA, _ *service.CeremonyInitializeOptions) (*service.CeremonyInitResult, error) {
	if m.initializeFn != nil {
		return m.initializeFn(ctx, ca)
	}
	return &service.CeremonyInitResult{SPKIPin: "pin123"}, nil
}
func (m *covMockCeremonySvc) CeremonyStatus(ctx context.Context) (*service.CeremonyStatusInfo, error) {
	if m.statusFn != nil {
		return m.statusFn(ctx)
	}
	return &service.CeremonyStatusInfo{SPKIPin: "pin123"}, nil
}
func (m *covMockCeremonySvc) CeremonySignCSR(ctx context.Context, username, soPin, csrPEM, role string) (*service.CeremonySignCSRResult, error) {
	if m.signCSRFn != nil {
		return m.signCSRFn(ctx, username, soPin, csrPEM, role)
	}
	return &service.CeremonySignCSRResult{CertPEM: []byte("-----BEGIN CERTIFICATE-----")}, nil
}

// ---------------------------------------------------------------------------
// Mock: CredentialServicer
// ---------------------------------------------------------------------------

type covMockCredentialSvc struct {
	storeFn           func(ctx context.Context, name string, value []byte) error
	retrieveFn        func(ctx context.Context, name string) ([]byte, error)
	submitFn          func(ctx context.Context, name string, value []byte) error
	deleteFn          func(ctx context.Context, name string) error
	strategyFn        func() string
	listFn            func(ctx context.Context) ([]string, error)
	rotateFn          func(ctx context.Context, name string, opts credentials.RotateOptions) (*credentials.RotateResult, error)
	retrieveVersionFn func(ctx context.Context, name string, version uint64) ([]byte, error)
	listVersionsFn    func(ctx context.Context, name string) ([]credentials.CredentialVersion, error)
	gcExpiredFn       func(ctx context.Context, name string) (int, error)
}

var _ service.CredentialServicer = (*covMockCredentialSvc)(nil)

func newCovMockCredentialSvc() *covMockCredentialSvc { return &covMockCredentialSvc{} }

func (m *covMockCredentialSvc) CredentialStore(ctx context.Context, name string, value []byte) error {
	if m.storeFn != nil {
		return m.storeFn(ctx, name, value)
	}
	return nil
}
func (m *covMockCredentialSvc) retrieveValue(ctx context.Context, name string) ([]byte, error) {
	if m.retrieveFn != nil {
		return m.retrieveFn(ctx, name)
	}
	return []byte("secret-value"), nil
}

func (m *covMockCredentialSvc) CredentialRetrieveBuffer(ctx context.Context, name string) (*secure.Buffer, error) {
	v, err := m.retrieveValue(ctx, name)
	if err != nil {
		return nil, err
	}
	return secure.NewBufferFromBytes(append([]byte(nil), v...))
}

func (m *covMockCredentialSvc) CredentialRetrieveVersionBuffer(ctx context.Context, name string, version uint64) (*secure.Buffer, error) {
	v, err := m.retrieveVersionValue(ctx, name, version)
	if err != nil {
		return nil, err
	}
	return secure.NewBufferFromBytes(append([]byte(nil), v...))
}
func (m *covMockCredentialSvc) CredentialSubmit(ctx context.Context, name string, value []byte) error {
	if m.submitFn != nil {
		return m.submitFn(ctx, name, value)
	}
	return nil
}
func (m *covMockCredentialSvc) CredentialDelete(ctx context.Context, name string) error {
	if m.deleteFn != nil {
		return m.deleteFn(ctx, name)
	}
	return nil
}
func (m *covMockCredentialSvc) CredentialStrategy(context.Context) (string, error) {
	return m.credentialStrategy(), nil
}

func (m *covMockCredentialSvc) credentialStrategy() string {
	if m.strategyFn != nil {
		return m.strategyFn()
	}
	return "barrier"
}
func (m *covMockCredentialSvc) CredentialList(ctx context.Context) ([]string, error) {
	if m.listFn != nil {
		return m.listFn(ctx)
	}
	return []string{"cred-a", "cred-b"}, nil
}
func (m *covMockCredentialSvc) CredentialRotate(ctx context.Context, name string, opts credentials.RotateOptions) (*credentials.RotateResult, error) {
	if m.rotateFn != nil {
		return m.rotateFn(ctx, name, opts)
	}
	return &credentials.RotateResult{
		Name:            name,
		PreviousVersion: 1,
		NewVersion:      2,
	}, nil
}
func (m *covMockCredentialSvc) retrieveVersionValue(ctx context.Context, name string, version uint64) ([]byte, error) {
	if m.retrieveVersionFn != nil {
		return m.retrieveVersionFn(ctx, name, version)
	}
	return []byte("versioned-value"), nil
}
func (m *covMockCredentialSvc) CredentialListVersions(ctx context.Context, name string) ([]credentials.CredentialVersion, error) {
	if m.listVersionsFn != nil {
		return m.listVersionsFn(ctx, name)
	}
	return []credentials.CredentialVersion{
		{Version: 1, IsCurrent: false, CreatedAt: time.Now().Add(-time.Hour), GraceUntil: time.Now().Add(time.Hour)},
		{Version: 2, IsCurrent: true, CreatedAt: time.Now()},
	}, nil
}
func (m *covMockCredentialSvc) CredentialGCExpired(ctx context.Context, name string) (int, error) {
	if m.gcExpiredFn != nil {
		return m.gcExpiredFn(ctx, name)
	}
	return 3, nil
}

// ---------------------------------------------------------------------------
// Mock: LockServicer
// ---------------------------------------------------------------------------

type covMockLockSvc struct {
	lockFn    func(ctx context.Context, key, holderID string, ttl time.Duration, reentrant bool) (*lock.Lock, error)
	tryLockFn func(ctx context.Context, key, holderID string, ttl time.Duration, reentrant bool) (*lock.Lock, error)
	unlockFn  func(ctx context.Context, key string, fencingToken int64) error
	extendFn  func(ctx context.Context, key string, fencingToken int64) (*lock.Lock, error)
	statusFn  func(ctx context.Context, key string) (*lock.Lock, error)
	listFn    func(ctx context.Context, prefix string) ([]*lock.Lock, error)
}

var _ service.LockServicer = (*covMockLockSvc)(nil)

func newCovMockLockSvc() *covMockLockSvc { return &covMockLockSvc{} }

func (m *covMockLockSvc) Lock(ctx context.Context, key, holderID string, ttl time.Duration, reentrant bool) (*lock.Lock, error) {
	if m.lockFn != nil {
		return m.lockFn(ctx, key, holderID, ttl, reentrant)
	}
	return &lock.Lock{Key: key, HolderID: holderID, FencingToken: 42}, nil
}
func (m *covMockLockSvc) TryLock(ctx context.Context, key, holderID string, ttl time.Duration, reentrant bool) (*lock.Lock, error) {
	if m.tryLockFn != nil {
		return m.tryLockFn(ctx, key, holderID, ttl, reentrant)
	}
	return &lock.Lock{Key: key, HolderID: holderID, FencingToken: 99}, nil
}
func (m *covMockLockSvc) Unlock(ctx context.Context, key string, fencingToken int64) error {
	if m.unlockFn != nil {
		return m.unlockFn(ctx, key, fencingToken)
	}
	return nil
}
func (m *covMockLockSvc) Extend(ctx context.Context, key string, fencingToken int64) (*lock.Lock, error) {
	if m.extendFn != nil {
		return m.extendFn(ctx, key, fencingToken)
	}
	return &lock.Lock{Key: key, FencingToken: fencingToken}, nil
}
func (m *covMockLockSvc) Status(ctx context.Context, key string) (*lock.Lock, error) {
	if m.statusFn != nil {
		return m.statusFn(ctx, key)
	}
	return &lock.Lock{Key: key, HolderID: "holder-1", FencingToken: 7}, nil
}
func (m *covMockLockSvc) List(ctx context.Context, prefix string) ([]*lock.Lock, error) {
	if m.listFn != nil {
		return m.listFn(ctx, prefix)
	}
	return []*lock.Lock{
		{Key: "resource/a", HolderID: "proc-1", FencingToken: 1},
	}, nil
}

// ---------------------------------------------------------------------------
// Mock: PINBackend
// ---------------------------------------------------------------------------

type covMockPINBackend struct {
	soPINSet   bool
	soPIN      string
	userPIN    string
	setSOPINFn func(old, newPIN string) error
	setUserFn  func(soPin, newPIN string) error
	verifySOFn func(soPin string) error
}

var _ pin.PINBackend = (*covMockPINBackend)(nil)

func (m *covMockPINBackend) Strategy() pin.StrategyID             { return "software" }
func (m *covMockPINBackend) SOPINSet() bool                       { return m.soPINSet }
func (m *covMockPINBackend) UserPINSet() bool                     { return m.userPIN != "" }
func (m *covMockPINBackend) IsInitialized() bool                  { return m.soPINSet }
func (m *covMockPINBackend) GetLockoutStatus() *pin.LockoutStatus { return nil }
func (m *covMockPINBackend) ResetLockout(_ string) error          { return nil }
func (m *covMockPINBackend) ChangeSOPIN(_, _ string) error        { return nil }
func (m *covMockPINBackend) ChangeUserPIN(_, _ string) error      { return nil }

func (m *covMockPINBackend) SetSOPIN(old, newPIN string) error {
	if m.setSOPINFn != nil {
		return m.setSOPINFn(old, newPIN)
	}
	m.soPIN = newPIN
	m.soPINSet = true
	return nil
}
func (m *covMockPINBackend) SetUserPIN(soPin, newPIN string) error {
	if m.setUserFn != nil {
		return m.setUserFn(soPin, newPIN)
	}
	m.userPIN = newPIN
	return nil
}
func (m *covMockPINBackend) VerifySOPIN(soPin string) error {
	if m.verifySOFn != nil {
		return m.verifySOFn(soPin)
	}
	if soPin != m.soPIN {
		return errors.New("invalid SO PIN")
	}
	return nil
}
func (m *covMockPINBackend) VerifyUserPIN(userPin string) error {
	if userPin != m.userPIN {
		return errors.New("invalid user PIN")
	}
	return nil
}
