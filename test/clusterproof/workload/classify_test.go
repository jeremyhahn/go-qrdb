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

//go:build clusterproof

package workload

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/test/clusterproof/history"
)

// grpcWrapped is a gRPC status error under a typed wrapper, the way a
// transport hands one up, so the classifier must unwrap to find the code.
func grpcWrapped(code codes.Code) error {
	return &DialError{Endpoint: "db.example.com:7000", Err: status.Error(code, code.String())}
}

func TestDefaultClassifier(t *testing.T) {
	var (
		ok    = history.OutcomeOK
		fail  = history.OutcomeFail
		indet = history.OutcomeIndeterminate
	)
	notFound := &transport.KeyNotFoundError{Key: "k"}
	connLost := &transport.ConnectionError{Address: "db.example.com:7000", Err: context.Canceled}
	wrapped := &DialError{Endpoint: "db.example.com:7000", Err: &transport.CapabilityUnsupportedError{Message: "cas"}}
	cases := map[string]struct {
		kind history.Kind
		err  error
		want history.Outcome
	}{
		"put ok":                     {history.KindPut, nil, ok},
		"get absent":                 {history.KindGet, notFound, ok},
		"delete absent":              {history.KindDelete, notFound, ok},
		"put not found":              {history.KindPut, notFound, indet},
		"get timeout":                {history.KindGet, context.DeadlineExceeded, fail},
		"get connection lost":        {history.KindGet, connLost, fail},
		"put timeout":                {history.KindPut, context.DeadlineExceeded, indet},
		"delete connection lost":     {history.KindDelete, connLost, indet},
		"put not leader":             {history.KindPut, &transport.NotLeaderError{ShardID: 1}, indet},
		"put closed":                 {history.KindPut, &transport.ClosedError{}, indet},
		"put request error":          {history.KindPut, &transport.RequestError{Method: "put", StatusCode: 503}, indet},
		"cas capability":             {history.KindCAS, &transport.CapabilityUnsupportedError{Message: "cas"}, fail},
		"put not implemented":        {history.KindPut, &transport.NotImplementedError{}, fail},
		"put invalid write mode":     {history.KindPut, &transport.InvalidWriteModeError{}, fail},
		"put config":                 {history.KindPut, &transport.ConfigError{Field: "Address"}, fail},
		"wrapped definite rejection": {history.KindCAS, wrapped, fail},
		// gRPC codes: the server's statement that a write had no effect is
		// fail; every code that can mean the request reached it is
		// indeterminate, Unavailable above all (it also reports a connection
		// that broke after the request was sent).
		"grpc invalid argument":    {history.KindPut, status.Error(codes.InvalidArgument, "bad key"), fail},
		"grpc unauthenticated":     {history.KindPut, status.Error(codes.Unauthenticated, "no token"), fail},
		"grpc permission denied":   {history.KindDelete, status.Error(codes.PermissionDenied, "denied"), fail},
		"grpc resource exhausted":  {history.KindCAS, status.Error(codes.ResourceExhausted, "rate"), fail},
		"grpc wrapped rejection":   {history.KindPut, grpcWrapped(codes.PermissionDenied), fail},
		"grpc unavailable put":     {history.KindPut, status.Error(codes.Unavailable, "conn reset"), indet},
		"grpc unavailable cas":     {history.KindCAS, grpcWrapped(codes.Unavailable), indet},
		"grpc deadline exceeded":   {history.KindPut, status.Error(codes.DeadlineExceeded, "slow"), indet},
		"grpc aborted":             {history.KindDelete, status.Error(codes.Aborted, "aborted"), indet},
		"grpc unknown":             {history.KindPut, status.Error(codes.Unknown, "?"), indet},
		"grpc failed precondition": {history.KindPut, status.Error(codes.FailedPrecondition, "sealed"), indet},
		"grpc internal":            {history.KindCAS, status.Error(codes.Internal, "panic"), indet},
		"grpc get unavailable":     {history.KindGet, status.Error(codes.Unavailable, "down"), fail},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := DefaultClassifier(tc.kind, tc.err); got != tc.want {
				t.Fatalf("DefaultClassifier(%s, %v) = %s, want %s", tc.kind, tc.err, got, tc.want)
			}
		})
	}
}

func TestConnectionLost(t *testing.T) {
	cases := map[string]struct {
		err  error
		want bool
	}{
		"nil":                      {nil, false},
		"timeout":                  {context.DeadlineExceeded, false},
		"connection":               {&transport.ConnectionError{Address: "a", Err: context.Canceled}, true},
		"closed":                   {&transport.ClosedError{}, true},
		"not leader":               {&transport.NotLeaderError{}, false},
		"grpc unavailable":         {status.Error(codes.Unavailable, "conn reset"), true},
		"wrapped grpc unavailable": {grpcWrapped(codes.Unavailable), true},
		"grpc deadline exceeded":   {status.Error(codes.DeadlineExceeded, "slow"), false},
		"grpc invalid argument":    {status.Error(codes.InvalidArgument, "bad"), false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := connectionLost(tc.err); got != tc.want {
				t.Fatalf("connectionLost(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
