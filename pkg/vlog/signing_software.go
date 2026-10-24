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

//go:build qrdb_vlog

package vlog

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/sha256"
)

// SoftwareHeadSignerProvider is the HeadSignerProvider go-qrdb ships: it signs
// with the log's own k/<version> key record, which never leaves the server.
type SoftwareHeadSignerProvider struct {
	keys SigningKeyReader
}

var _ HeadSignerProvider = (*SoftwareHeadSignerProvider)(nil)

// NewSoftwareHeadSignerProvider returns the provider reading keys from keys.
func NewSoftwareHeadSignerProvider(keys SigningKeyReader) *SoftwareHeadSignerProvider {
	return &SoftwareHeadSignerProvider{keys: keys}
}

// HeadSigner reads the key req.KeyRef names and returns a signer over it.
func (p *SoftwareHeadSignerProvider) HeadSigner(ctx context.Context, req HeadSignerRequest) (HeadSigner, error) {
	version, err := ParseKeyRef(req.KeyRef)
	if err != nil {
		return nil, err
	}
	id := LogID{TenantID: req.TenantID, Database: req.Database, Log: req.Log}
	k, err := p.keys.ReadSigningKey(ctx, id, version)
	if err != nil {
		return nil, err
	}
	return NewSoftwareHeadSigner(id.String(), *k)
}

// softwareSigner is a HeadSigner over an in-memory key.
type softwareSigner struct {
	name    string
	version uint32
	typ     SignatureType
	key     crypto.Signer
	hash    uint32
}

var _ HeadSigner = (*softwareSigner)(nil)

// NewSoftwareHeadSigner returns a HeadSigner named name over k.
func NewSoftwareHeadSigner(name string, k SigningKey) (HeadSigner, error) {
	key, t, err := k.Signer()
	if err != nil {
		return nil, err
	}
	hash, err := KeyHash(name, key.Public())
	if err != nil {
		return nil, err
	}
	return &softwareSigner{name: name, version: k.Version, typ: t, key: key, hash: hash}, nil
}

func (s *softwareSigner) Name() string                 { return s.name }
func (s *softwareSigner) KeyHash() uint32              { return s.hash }
func (s *softwareSigner) KeyVersion() uint32           { return s.version }
func (s *softwareSigner) SignatureType() SignatureType { return s.typ }
func (s *softwareSigner) Public() crypto.PublicKey     { return s.key.Public() }

// Sign signs msg: Ed25519 over the message itself, ECDSA over its SHA-256.
func (s *softwareSigner) Sign(ctx context.Context, msg []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.typ == SignatureEd25519 {
		return s.key.Sign(nil, msg, crypto.Hash(0))
	}
	digest := sha256.Sum256(msg)
	return s.key.Sign(rand.Reader, digest[:], crypto.SHA256)
}
