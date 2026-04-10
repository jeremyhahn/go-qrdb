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

	"google.golang.org/protobuf/proto"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
)

// handleKVPut unmarshals a PutRequest, stores the key-value pair via the
// service layer, and returns a serialized PutResponse.
func (s *Server) handleKVPut(ctx context.Context, payload []byte) ([]byte, error) {
	var req pb.PutRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	if err := s.kvSvc.Put(ctx, req.GetKey(), req.GetValue()); err != nil {
		return nil, err
	}

	resp, err := proto.Marshal(&pb.PutResponse{})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleKVGet unmarshals a GetRequest, retrieves the value from the service
// layer, and returns a serialized GetResponse.
func (s *Server) handleKVGet(ctx context.Context, payload []byte) ([]byte, error) {
	var req pb.GetRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	data, err := s.kvSvc.Get(ctx, req.GetKey())
	if err != nil {
		return nil, err
	}

	resp, err := proto.Marshal(&pb.GetResponse{Value: data})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleKVDelete unmarshals a DeleteRequest, removes the key via the service
// layer, and returns a serialized DeleteResponse.
func (s *Server) handleKVDelete(ctx context.Context, payload []byte) ([]byte, error) {
	var req pb.DeleteRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	if err := s.kvSvc.Delete(ctx, req.GetKey()); err != nil {
		return nil, err
	}

	resp, err := proto.Marshal(&pb.DeleteResponse{})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleKVList unmarshals a ListRequest, retrieves sorted keys matching the
// prefix from the service layer, and returns a serialized ListResponse.
func (s *Server) handleKVList(ctx context.Context, payload []byte) ([]byte, error) {
	var req pb.ListRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	keys, err := s.kvSvc.List(ctx, req.GetPrefix())
	if err != nil {
		return nil, err
	}

	resp, err := proto.Marshal(&pb.ListResponse{Keys: keys})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleKVScan unmarshals a ScanRequest, retrieves key-value pairs matching
// the prefix from the service layer, and returns a serialized ScanResponse.
func (s *Server) handleKVScan(ctx context.Context, payload []byte) ([]byte, error) {
	var req pb.ScanRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	data, err := s.kvSvc.Scan(ctx, req.GetPrefix())
	if err != nil {
		return nil, err
	}

	resp, err := proto.Marshal(&pb.ScanResponse{Pairs: data})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}
