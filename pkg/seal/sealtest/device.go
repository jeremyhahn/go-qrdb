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

package sealtest

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"
	"sync/atomic"

	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// DeviceStrategy is a fake of a hardware sealing strategy (TPM 2.0, PKCS#11, a
// cloud KMS) as go-xkms registers it. It wraps and unwraps a root key under a
// device key that is generated inside the strategy and never leaves it, takes
// no passphrase, and counts the device operations it served -- which is what a
// test asserts to show a barrier was sealed and unsealed by the device.
//
// Like a real device-backed strategy it returns no per-operation encrypter from
// BarrierEncryptor, so the barrier does its bulk encryption with a resident DEK
// and the device is touched only to wrap and unwrap the root key.
type DeviceStrategy struct {
	id      seal.StrategyID
	aead    cipher.AEAD
	wraps   atomic.Int64
	unwraps atomic.Int64
	fault   atomic.Pointer[error]
}

// NewDeviceStrategy returns a DeviceStrategy registered under id, with a fresh
// device key.
func NewDeviceStrategy(id seal.StrategyID) (*DeviceStrategy, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	// The FIPS-approved construction: it samples the nonce itself and prepends it.
	aead, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		return nil, err
	}
	return &DeviceStrategy{id: id, aead: aead}, nil
}

// Wraps is the number of root-key wraps the device served.
func (d *DeviceStrategy) Wraps() int64 { return d.wraps.Load() }

// Unwraps is the number of root-key unwraps the device served.
func (d *DeviceStrategy) Unwraps() int64 { return d.unwraps.Load() }

// ID implements seal.SealingStrategy.
func (d *DeviceStrategy) ID() seal.StrategyID { return d.id }

// Available implements seal.SealingStrategy.
func (d *DeviceStrategy) Available() bool { return true }

// HardwareBacked implements seal.SealingStrategy.
func (d *DeviceStrategy) HardwareBacked() bool { return true }

// Close implements seal.SealingStrategy.
func (d *DeviceStrategy) Close() error { return nil }

// BarrierEncryptor implements seal.SealingStrategy; see the type comment.
func (d *DeviceStrategy) BarrierEncryptor(context.Context, []byte) (seal.SymmetricEncrypter, error) {
	return nil, nil
}

// SealRootKey implements seal.SealingStrategy.
func (d *DeviceStrategy) SealRootKey(ctx context.Context, rootKey []byte, creds seal.Credentials) (*seal.SealedRootKey, error) {
	d.wraps.Add(1)
	pin, err := devicePIN(ctx, creds)
	if err != nil {
		return nil, err
	}
	// #nosec G407 -- the nonce argument MUST be nil: this AEAD is cipher.NewGCMWithRandomNonce, which samples a fresh nonce inside the FIPS module on every Seal and prepends it.
	ct := d.aead.Seal(nil, nil, rootKey, pin)
	return &seal.SealedRootKey{Strategy: d.id, Version: 1, Ciphertext: ct}, nil
}

// UnsealRootKey implements seal.SealingStrategy. As the SealingStrategy
// contract asks, an AEAD open that fails under the presented credential is a
// refused credential, reported as seal.ErrDecryptionFailed wrapped with its
// cause; an injected device fault (InjectFault) is returned raw, as a real
// device's operational failure is.
func (d *DeviceStrategy) UnsealRootKey(ctx context.Context, sealed *seal.SealedRootKey, creds seal.Credentials) ([]byte, error) {
	d.unwraps.Add(1)
	if fault := d.fault.Load(); fault != nil {
		return nil, *fault
	}
	pin, err := devicePIN(ctx, creds)
	if err != nil {
		return nil, err
	}
	rootKey, err := d.aead.Open(nil, nil, sealed.Ciphertext, pin)
	if err != nil {
		return nil, fmt.Errorf("sealtest device: unwrap root key: %w: %w", seal.ErrDecryptionFailed, err)
	}
	return rootKey, nil
}

// InjectFault makes every later UnsealRootKey fail with err, as a device that
// is unplugged or refuses service does; nil clears the fault.
func (d *DeviceStrategy) InjectFault(err error) {
	if err == nil {
		d.fault.Store(nil)
		return
	}
	d.fault.Store(&err)
}

// devicePIN returns the credential the device binds a wrap to (its PIN), used
// as the AEAD's additional data: a wrap opens only under the credential it was
// made with. No credential binds the empty PIN.
func devicePIN(ctx context.Context, creds seal.Credentials) ([]byte, error) {
	if creds.Secret == nil {
		return nil, nil
	}
	opened, err := creds.Secret.Open(ctx)
	if err != nil {
		return nil, fmt.Errorf("sealtest device: open credential: %w", err)
	}
	defer func() { _ = opened.Destroy() }()
	var pin []byte
	if err := opened.WithBytes(func(view []byte) error {
		pin = bytes.Clone(view)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("sealtest device: read credential: %w", err)
	}
	return pin, nil
}

var _ seal.SealingStrategy = (*DeviceStrategy)(nil)
