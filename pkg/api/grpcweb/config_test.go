package grpcweb

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestConfig_ZeroValueIsOffAndValid(t *testing.T) {
	var c Config
	if c.Enabled {
		t.Error("zero Config is enabled")
	}
	if err := c.Validate(); err != nil {
		t.Errorf("zero Config Validate = %v, want nil", err)
	}
}

func TestConfig_Validate(t *testing.T) {
	for _, tc := range []struct {
		name      string
		cfg       Config
		wantErr   any
		wantMatch string
	}{
		{name: "wildcard", cfg: Config{Enabled: true, AllowedOrigins: []string{"*"}}},
		{name: "listed with credentials", cfg: Config{AllowedOrigins: []string{"https://app.example.com", "http://localhost:8080"}, AllowCredentials: true}},
		{name: "wildcard with credentials", cfg: Config{Enabled: true, AllowedOrigins: []string{"https://app.example.com", "*"}, AllowCredentials: true}, wantErr: &WildcardCredentialsError{}},
		{name: "disabled wildcard with credentials", cfg: Config{AllowedOrigins: []string{"*"}, AllowCredentials: true}, wantErr: &WildcardCredentialsError{}},
		{name: "path", cfg: Config{AllowedOrigins: []string{"https://app.example.com/"}}, wantErr: &OriginError{}, wantMatch: "https://app.example.com/"},
		{name: "uppercase", cfg: Config{AllowedOrigins: []string{"https://App.example.com"}}, wantErr: &OriginError{}},
		{name: "scheme", cfg: Config{AllowedOrigins: []string{"ftp://app.example.com"}}, wantErr: &OriginError{}},
		{name: "no host", cfg: Config{AllowedOrigins: []string{"app.example.com"}}, wantErr: &OriginError{}},
		{name: "empty", cfg: Config{AllowedOrigins: []string{""}}, wantErr: &OriginError{}},
		{name: "unparsable", cfg: Config{AllowedOrigins: []string{"https://%zz"}}, wantErr: &OriginError{}},
		{name: "userinfo", cfg: Config{AllowedOrigins: []string{"https://u@app.example.com"}}, wantErr: &OriginError{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			switch want := tc.wantErr.(type) {
			case nil:
				if err != nil {
					t.Fatalf("Validate = %v, want nil", err)
				}
			case *WildcardCredentialsError:
				if !errors.As(err, &want) {
					t.Fatalf("Validate = %v, want *WildcardCredentialsError", err)
				}
			case *OriginError:
				if !errors.As(err, &want) {
					t.Fatalf("Validate = %v, want *OriginError", err)
				}
				if !strings.Contains(err.Error(), tc.wantMatch) {
					t.Errorf("error %q does not name %q", err, tc.wantMatch)
				}
			}
		})
	}
}

func TestErrors_Messages(t *testing.T) {
	if msg := (&WildcardCredentialsError{}).Error(); !strings.HasPrefix(msg, "grpcweb: ") || !strings.Contains(msg, "allow_credentials") {
		t.Errorf("WildcardCredentialsError = %q", msg)
	}
	if msg := (&OriginError{Origin: "x"}).Error(); msg != `grpcweb: allowed origin "x" is not a serialized origin (scheme://host[:port])` {
		t.Errorf("OriginError = %q", msg)
	}
}

func corsRequest(method, origin string) *http.Request {
	req := httptest.NewRequest(method, PathPrefix+"a.B/C", nil)
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	return req
}

func TestCORS_OffByDefault(t *testing.T) {
	called := false
	h := mustHandler(t, Config{Enabled: true}, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))

	pre := corsRequest(http.MethodOptions, "https://app.example.com")
	pre.Header.Set("Access-Control-Request-Method", http.MethodPost)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, pre)
	if rec.Code != http.StatusForbidden || rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("preflight = %d ACAO %q, want 403 and none", rec.Code, rec.Header().Get("Access-Control-Allow-Origin"))
	}

	req := corsRequest(http.MethodPost, "https://app.example.com")
	req.Header.Set("Content-Type", "application/grpc-web")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("Access-Control-Allow-Origin") != "" || rec.Header().Get("Vary") != "Origin" {
		t.Errorf("headers = %v, want no ACAO and Vary Origin", rec.Header())
	}
	if !called {
		t.Error("same-origin-policy enforcement is the browser's; the call must still reach the backend")
	}
}

func TestCORS_ListedOrigin(t *testing.T) {
	h := mustHandler(t, Config{Enabled: true, AllowedOrigins: []string{"https://app.example.com"}}, http.NotFoundHandler())

	pre := corsRequest(http.MethodOptions, "https://app.example.com")
	pre.Header.Set("Access-Control-Request-Method", http.MethodPost)
	pre.Header.Set("Access-Control-Request-Headers", "authorization, content-type, x-grpc-web")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, pre)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("preflight = %d, want 204", rec.Code)
	}
	for key, want := range map[string]string{
		"Access-Control-Allow-Origin":      "https://app.example.com",
		"Access-Control-Allow-Methods":     http.MethodPost,
		"Access-Control-Allow-Headers":     "authorization, content-type, x-grpc-web",
		"Access-Control-Expose-Headers":    corsExposeHeaders,
		"Access-Control-Allow-Credentials": "",
	} {
		if got := rec.Header().Get(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}

	other := corsRequest(http.MethodOptions, "https://evil.example.com")
	other.Header.Set("Access-Control-Request-Method", http.MethodPost)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, other)
	if rec.Code != http.StatusForbidden || rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("unlisted preflight = %d ACAO %q, want 403 and none", rec.Code, rec.Header().Get("Access-Control-Allow-Origin"))
	}

	wrongMethod := corsRequest(http.MethodOptions, "https://app.example.com")
	wrongMethod.Header.Set("Access-Control-Request-Method", http.MethodDelete)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, wrongMethod)
	if rec.Code != http.StatusForbidden {
		t.Errorf("DELETE preflight = %d, want 403", rec.Code)
	}
}

func TestCORS_WildcardOrigin(t *testing.T) {
	h := mustHandler(t, Config{Enabled: true, AllowedOrigins: []string{"*"}}, http.NotFoundHandler())
	pre := corsRequest(http.MethodOptions, "https://any.example.com")
	pre.Header.Set("Access-Control-Request-Method", http.MethodPost)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, pre)
	if rec.Code != http.StatusNoContent || rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Errorf("preflight = %d ACAO %q, want 204 and *", rec.Code, rec.Header().Get("Access-Control-Allow-Origin"))
	}
	if rec.Header().Get("Access-Control-Allow-Credentials") != "" {
		t.Error("wildcard origin allowed credentials")
	}
}

func TestCORS_CredentialsEchoListedOrigin(t *testing.T) {
	h := mustHandler(t, Config{Enabled: true, AllowedOrigins: []string{"https://app.example.com"}, AllowCredentials: true}, http.NotFoundHandler())
	pre := corsRequest(http.MethodOptions, "https://app.example.com")
	pre.Header.Set("Access-Control-Request-Method", http.MethodPost)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, pre)
	if rec.Header().Get("Access-Control-Allow-Origin") != "https://app.example.com" || rec.Header().Get("Access-Control-Allow-Credentials") != "true" {
		t.Errorf("headers = %v, want the origin echoed with credentials", rec.Header())
	}
}

func TestCORS_NoOriginIsNotCORS(t *testing.T) {
	h := mustHandler(t, Config{Enabled: true, AllowedOrigins: []string{"*"}}, http.NotFoundHandler())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, corsRequest(http.MethodOptions, ""))
	if rec.Code != http.StatusForbidden || rec.Header().Get("Vary") != "" {
		t.Errorf("OPTIONS without Origin = %d Vary %q, want 403 and no Vary", rec.Code, rec.Header().Get("Vary"))
	}
}
