//go:build clusterproof

package container

import (
	"errors"
	"testing"
)

func TestErrorMessages(t *testing.T) {
	inner := errors.New("inner")
	for _, tc := range []struct {
		err  error
		want string
	}{
		{&ConfigError{Field: "image", Reason: "required"}, "container: inventory image: required"},
		{&UnknownProtocolError{Protocol: "ftp"}, `container: unknown protocol "ftp" (want quic, grpc or rest)`},
		{&CommandError{Node: "n1", Step: "start", Output: "conflict", Err: inner}, "container: start on n1: inner: conflict"},
		{&CommandError{Node: "n1", Step: "start", Err: inner}, "container: start on n1: inner"},
		{&IdentityError{Node: "n1", Output: "x"}, `container: node-identity on n1 printed no spki:<sha256-hex> identity: "x"`},
		{&UnsupportedTransportError{Protocol: "rest", Need: "the admin calls"}, `container: transport "rest" does not support the admin calls`},
		{&StepError{Node: "n1", Protocol: "grpc", Step: "BOOT-01", Err: inner}, "container: BOOT-01 on n1 over grpc: inner"},
		{&StepError{Node: "n1", Step: "first boot", Err: inner}, "container: first boot on n1: inner"},
		{&CheckError{Check: "peers", Reason: "no system shard"}, "container: peers: no system shard"},
	} {
		if got := tc.err.Error(); got != tc.want {
			t.Errorf("got %q, want %q", got, tc.want)
		}
	}
}

func TestErrorsUnwrap(t *testing.T) {
	inner := errors.New("inner")
	for _, err := range []error{
		&CommandError{Node: "n1", Step: "start", Err: inner},
		&StepError{Node: "n1", Step: "first boot", Err: inner},
	} {
		if !errors.Is(err, inner) {
			t.Errorf("%T does not unwrap to its cause", err)
		}
	}
}
