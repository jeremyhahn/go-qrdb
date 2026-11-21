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

package quic

import (
	"context"
	"encoding/json"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// sealStoreNameWire is the payload of sealstore.Get, Delete and Reseal.
type sealStoreNameWire struct {
	Name string `json:"name"`
}

// sealStorePutWire is the payload of sealstore.Put; Secret is base64 on the wire.
type sealStorePutWire struct {
	Name   string `json:"name"`
	Secret []byte `json:"secret"`
}

func (s *Server) sealStoreConfigured() error {
	if s.sealStoreSvc == nil {
		return &ServiceNotConfiguredError{Service: "seal store"}
	}
	return nil
}

func decodeSealStore(payload []byte, v any) error {
	if err := json.Unmarshal(payload, v); err != nil {
		return &UnmarshalError{Err: err}
	}
	return nil
}

func encodeSealStore(v any) ([]byte, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return data, nil
}

func (s *Server) handleSealStorePut(ctx context.Context, payload []byte) ([]byte, error) {
	if err := s.sealStoreConfigured(); err != nil {
		return nil, err
	}
	var req sealStorePutWire
	if err := decodeSealStore(payload, &req); err != nil {
		return nil, err
	}
	if err := s.sealStoreSvc.Put(ctx, req.Name, req.Secret); err != nil {
		return nil, err
	}
	return nil, nil
}

func (s *Server) handleSealStoreGet(ctx context.Context, payload []byte) ([]byte, error) {
	if err := s.sealStoreConfigured(); err != nil {
		return nil, err
	}
	var req sealStoreNameWire
	if err := decodeSealStore(payload, &req); err != nil {
		return nil, err
	}
	secret, err := s.sealStoreSvc.Get(ctx, req.Name)
	if err != nil {
		return nil, err
	}
	return encodeSealStore(transport.SealStoreGetResponse{Name: req.Name, Secret: secret})
}

func (s *Server) handleSealStoreDelete(ctx context.Context, payload []byte) ([]byte, error) {
	if err := s.sealStoreConfigured(); err != nil {
		return nil, err
	}
	var req sealStoreNameWire
	if err := decodeSealStore(payload, &req); err != nil {
		return nil, err
	}
	if err := s.sealStoreSvc.Delete(ctx, req.Name); err != nil {
		return nil, err
	}
	return nil, nil
}

func (s *Server) handleSealStoreList(ctx context.Context, _ []byte) ([]byte, error) {
	if err := s.sealStoreConfigured(); err != nil {
		return nil, err
	}
	names, err := s.sealStoreSvc.List(ctx)
	if err != nil {
		return nil, err
	}
	if names == nil {
		names = []string{}
	}
	return encodeSealStore(transport.SealStoreListResponse{Names: names})
}

func (s *Server) handleSealStoreReseal(ctx context.Context, payload []byte) ([]byte, error) {
	if err := s.sealStoreConfigured(); err != nil {
		return nil, err
	}
	var req sealStoreNameWire
	if err := decodeSealStore(payload, &req); err != nil {
		return nil, err
	}
	if err := s.sealStoreSvc.Reseal(ctx, req.Name); err != nil {
		return nil, err
	}
	return nil, nil
}

func (s *Server) handleSealStoreStatus(ctx context.Context, _ []byte) ([]byte, error) {
	if err := s.sealStoreConfigured(); err != nil {
		return nil, err
	}
	st, err := s.sealStoreSvc.Status(ctx)
	if err != nil {
		return nil, err
	}
	return encodeSealStore(transport.SealStoreStatusResponse{
		Available:   st.Available,
		SealerID:    string(st.Strategy),
		SecretCount: st.SecretCount,
		SecretNames: st.SecretNames,
	})
}
