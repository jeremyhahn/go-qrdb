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
// service layer, and returns a serialized PutResponse. When the request
// carries a non-UNSPECIFIED WriteMode, PutWithMode is used; otherwise the
// plain Put method is called, preserving backwards compatibility with clients
// that omit the field.
func (s *Server) handleKVPut(ctx context.Context, payload []byte) ([]byte, error) {
	var req pb.PutRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	kvSvc, svcErr := s.kvSvcFor(ctx)
	if svcErr != nil {
		return nil, svcErr
	}

	if mode, ok := writeModeFromWire(req.GetWriteMode()); ok {
		if err := kvSvc.PutWithMode(ctx, req.GetKey(), req.GetValue(), mode); err != nil {
			return nil, err
		}
	} else {
		if err := kvSvc.Put(ctx, req.GetKey(), req.GetValue()); err != nil {
			return nil, err
		}
	}

	resp, err := proto.Marshal(&pb.PutResponse{})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleKVGet unmarshals a GetRequest, retrieves the value from the service
// layer, and returns a serialized GetResponse. When the request carries a
// non-UNSPECIFIED ConsistencyLevel, GetWithConsistency is used; otherwise the
// plain Get method is called.
func (s *Server) handleKVGet(ctx context.Context, payload []byte) ([]byte, error) {
	var req pb.GetRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	kvSvc, svcErr := s.kvSvcFor(ctx)
	if svcErr != nil {
		return nil, svcErr
	}

	var (
		data []byte
		err  error
	)
	if opts, ok := readOptsFromWire(req.GetConsistency()); ok {
		data, err = kvSvc.GetWithConsistency(ctx, req.GetKey(), opts)
	} else {
		data, err = kvSvc.Get(ctx, req.GetKey())
	}
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
// layer, and returns a serialized DeleteResponse. When the request carries a
// non-UNSPECIFIED WriteMode, DeleteWithMode is used; otherwise the plain
// Delete method is called.
func (s *Server) handleKVDelete(ctx context.Context, payload []byte) ([]byte, error) {
	var req pb.DeleteRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	kvSvc, svcErr := s.kvSvcFor(ctx)
	if svcErr != nil {
		return nil, svcErr
	}

	if mode, ok := writeModeFromWire(req.GetWriteMode()); ok {
		if err := kvSvc.DeleteWithMode(ctx, req.GetKey(), mode); err != nil {
			return nil, err
		}
	} else {
		if err := kvSvc.Delete(ctx, req.GetKey()); err != nil {
			return nil, err
		}
	}

	resp, err := proto.Marshal(&pb.DeleteResponse{})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}

// handleKVList unmarshals a ListRequest, retrieves sorted keys matching the
// prefix from the service layer, and returns a serialized ListResponse. When
// the request carries a non-UNSPECIFIED ConsistencyLevel, ListWithConsistency
// is used; otherwise the plain List method is called.
func (s *Server) handleKVList(ctx context.Context, payload []byte) ([]byte, error) {
	var req pb.ListRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	kvSvc, svcErr := s.kvSvcFor(ctx)
	if svcErr != nil {
		return nil, svcErr
	}

	var (
		keys []string
		err  error
	)
	if opts, ok := readOptsFromWire(req.GetConsistency()); ok {
		keys, err = kvSvc.ListWithConsistency(ctx, req.GetPrefix(), opts)
	} else {
		keys, err = kvSvc.List(ctx, req.GetPrefix())
	}
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
// When the request carries a non-UNSPECIFIED ConsistencyLevel,
// ScanWithConsistency is used; otherwise the plain Scan method is called.
func (s *Server) handleKVScan(ctx context.Context, payload []byte) ([]byte, error) {
	var req pb.ScanRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, &UnmarshalError{Err: err}
	}

	kvSvc, svcErr := s.kvSvcFor(ctx)
	if svcErr != nil {
		return nil, svcErr
	}

	var (
		data map[string][]byte
		err  error
	)
	if opts, ok := readOptsFromWire(req.GetConsistency()); ok {
		data, err = kvSvc.ScanWithConsistency(ctx, req.GetPrefix(), opts)
	} else {
		data, err = kvSvc.Scan(ctx, req.GetPrefix())
	}
	if err != nil {
		return nil, err
	}

	resp, err := proto.Marshal(&pb.ScanResponse{Pairs: data})
	if err != nil {
		return nil, &MarshalError{Err: err}
	}
	return resp, nil
}
