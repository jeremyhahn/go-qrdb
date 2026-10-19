package grpc

import (
	"net/http"
	"strconv"

	"google.golang.org/grpc/codes"
)

// notServingMessage is the grpc-message of a request ServeHTTP receives
// while the server is not serving.
const notServingMessage = "grpc server is not serving"

// ServeHTTP serves one native gRPC request carried by net/http through the
// same *grpc.Server, and so the same interceptor chain -- authentication,
// tenant derivation, the sealed window and method-permission authorization
// -- that the gRPC listener serves. The peer's TLS state, when r carries
// one, is the credentials.TLSInfo the interceptors authenticate, and r's
// headers are the incoming metadata. The gRPC-Web translator
// (pkg/api/grpcweb) is the caller.
//
// Before Start, after Stop, and on a nil *Server it answers Unavailable,
// so a handler bound before the gRPC server starts is safe to serve.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s != nil {
		if gs := s.serving.Load(); gs != nil {
			gs.ServeHTTP(w, r)
			return
		}
	}
	h := w.Header()
	h.Set("Content-Type", "application/grpc")
	h.Set("Grpc-Status", strconv.Itoa(int(codes.Unavailable)))
	h.Set("Grpc-Message", notServingMessage)
	w.WriteHeader(http.StatusOK)
}
