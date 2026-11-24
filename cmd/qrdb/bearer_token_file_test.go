//go:build !integration

package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

const testBearerToken = "s3cr3t-bearer-value"

// writeTokenFile writes content to a fresh token file and returns its path.
func writeTokenFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}
	return path
}

// unusedLoopbackAddr returns a loopback host:port nothing listens on.
func unusedLoopbackAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	return addr
}

func TestTokenFileSource_ReadsTrimmedTokenEveryCall(t *testing.T) {
	path := writeTokenFile(t, "first-token\n")
	src := tokenFileSource{path: path}

	got, err := src.Token(context.Background())
	if err != nil {
		t.Fatalf("first Token: %v", err)
	}
	if got != "first-token" {
		t.Fatalf("first Token = %q, want %q", got, "first-token")
	}

	if err := os.WriteFile(path, []byte("  second-token \r\n"), 0o600); err != nil {
		t.Fatalf("rewrite token file: %v", err)
	}
	got, err = src.Token(context.Background())
	if err != nil {
		t.Fatalf("second Token: %v", err)
	}
	if got != "second-token" {
		t.Fatalf("second Token = %q, want %q (file must be re-read per call)", got, "second-token")
	}
}

func TestTokenFileSource_MissingFileErrorOmitsContent(t *testing.T) {
	path := writeTokenFile(t, testBearerToken)
	src := tokenFileSource{path: path}
	if _, err := src.Token(context.Background()); err != nil {
		t.Fatalf("Token before removal: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove: %v", err)
	}

	_, err := src.Token(context.Background())
	if err == nil {
		t.Fatal("Token on a missing file returned no error")
	}
	if !strings.Contains(err.Error(), path) {
		t.Fatalf("error %q does not name the path", err)
	}
	if strings.Contains(err.Error(), testBearerToken) {
		t.Fatalf("error %q leaks the token", err)
	}
}

func TestTokenFileSource_EmptyFileRefused(t *testing.T) {
	for name, content := range map[string]string{"empty": "", "whitespace": " \n\t\n"} {
		t.Run(name, func(t *testing.T) {
			src := tokenFileSource{path: writeTokenFile(t, content)}
			got, err := src.Token(context.Background())
			if err == nil {
				t.Fatalf("Token = %q with no error; an empty token file must be refused", got)
			}
		})
	}
}

func TestBuildClient_BearerTokenFile_Unreadable(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent")
	client, err := buildClient(context.Background(), transportREST, "https://127.0.0.1:1",
		"", "", "", missing, time.Second)
	if client != nil {
		t.Fatal("buildClient returned a client for an unreadable token file")
	}
	var argErr *InvalidArgError
	if !errors.As(err, &argErr) {
		t.Fatalf("err = %T %v, want *InvalidArgError", err, err)
	}
	if argErr.Arg != "bearer-token-file" {
		t.Fatalf("InvalidArgError.Arg = %q, want bearer-token-file", argErr.Arg)
	}
}

func TestBuildClient_BearerTokenFile_QUICRefused(t *testing.T) {
	path := writeTokenFile(t, testBearerToken)
	client, err := buildClient(context.Background(), transportQUIC, unusedLoopbackAddr(t),
		"", "", "", path, time.Second)
	if client != nil {
		t.Fatal("buildClient returned a QUIC client carrying a token source")
	}
	var tErr *TransportError
	if !errors.As(err, &tErr) {
		t.Fatalf("err = %T %v, want *TransportError", err, err)
	}
	var cfgErr *transport.ConfigError
	if !errors.As(err, &cfgErr) {
		t.Fatalf("err = %v, want a *transport.ConfigError inside", err)
	}
	if cfgErr.Field != "TokenSource" {
		t.Fatalf("ConfigError.Field = %q, want TokenSource", cfgErr.Field)
	}
	if strings.Contains(err.Error(), testBearerToken) {
		t.Fatalf("error %q leaks the token", err)
	}
}

func TestBuildClient_BearerTokenFile_PlaintextRESTRefused(t *testing.T) {
	path := writeTokenFile(t, testBearerToken)
	transportType, address := resolveTransport(fmt.Sprintf("http://%s", unusedLoopbackAddr(t)), "")
	if transportType != transportREST {
		t.Fatalf("resolveTransport type = %q, want %q", transportType, transportREST)
	}

	client, err := buildClient(context.Background(), transportType, address,
		"", "", "", path, time.Second)
	if client != nil {
		t.Fatal("buildClient returned a client sending a bearer over plaintext")
	}
	var connErr *ConnectError
	if !errors.As(err, &connErr) {
		t.Fatalf("err = %T %v, want *ConnectError", err, err)
	}
	var tlsErr *transport.BearerRequiresTLSError
	if !errors.As(err, &tlsErr) {
		t.Fatalf("err = %v, want a *transport.BearerRequiresTLSError inside", err)
	}
	if strings.Contains(err.Error(), testBearerToken) {
		t.Fatalf("error %q leaks the token", err)
	}
}

func TestRootCmd_BearerTokenFileFlagRegistered(t *testing.T) {
	f := newRootCmd().PersistentFlags().Lookup("bearer-token-file")
	if f == nil {
		t.Fatal("root command has no persistent --bearer-token-file flag")
	}
	if f.DefValue != "" {
		t.Fatalf("--bearer-token-file default = %q, want empty", f.DefValue)
	}
}
