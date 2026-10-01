// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package middleware

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// reflectionMethods are the server-reflection RPCs of both published
// versions. A server that registers reflection answers them with its whole
// schema -- every service, method and message type.
var reflectionMethods = []string{
	"/grpc.reflection.v1.ServerReflection/ServerReflectionInfo",
	"/grpc.reflection.v1alpha.ServerReflection/ServerReflectionInfo",
}

// TestReflectionIsNotAnonymous: server reflection used to be exempt from
// authentication and authorization, so a composer that registered it
// exposed the node's whole gRPC schema to any caller with no credentials.
// qrdb registers no reflection service; one that is registered must be
// authenticated like any other method, or declared public explicitly.
func TestReflectionIsNotAnonymous(t *testing.T) {
	m := New(&mockAuthorizer{}, WithAuthenticator(failingAuthenticator{}))
	unary := m.UnaryInterceptor()
	stream := m.StreamInterceptor()

	for _, method := range reflectionMethods {
		_, err := unary(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: method},
			func(context.Context, any) (any, error) {
				t.Fatalf("anonymous unary %s reached the handler", method)
				return nil, nil
			})
		if status.Code(err) != codes.Unauthenticated {
			t.Errorf("anonymous unary %s: %v, want Unauthenticated", method, err)
		}

		err = stream(nil, &boostServerStream{ctx: context.Background()}, &grpc.StreamServerInfo{FullMethod: method},
			func(any, grpc.ServerStream) error {
				t.Fatalf("anonymous stream %s reached the handler", method)
				return nil
			})
		if status.Code(err) != codes.Unauthenticated {
			t.Errorf("anonymous stream %s: %v, want Unauthenticated", method, err)
		}
	}
}

// TestReflectionCanBeDeclaredPublic: a composer that wants reflection
// anonymous says so explicitly through RegisterPublicGRPCMethods, the same
// declaration every other public method needs.
func TestReflectionCanBeDeclaredPublic(t *testing.T) {
	m := New(&mockAuthorizer{}, WithAuthenticator(failingAuthenticator{}))
	if err := m.RegisterPublicGRPCMethods(reflectionMethods[:1]); err != nil {
		t.Fatalf("RegisterPublicGRPCMethods: %v", err)
	}
	called := false
	err := m.StreamInterceptor()(nil, &boostServerStream{ctx: context.Background()},
		&grpc.StreamServerInfo{FullMethod: reflectionMethods[0]},
		func(any, grpc.ServerStream) error { called = true; return nil })
	if err != nil || !called {
		t.Fatalf("declared-public reflection: called=%v err=%v", called, err)
	}
}
