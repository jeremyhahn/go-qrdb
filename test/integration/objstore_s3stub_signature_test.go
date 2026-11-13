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

//go:build integration

package integration_test

import (
	"bytes"
	"crypto/md5" //nolint:gosec // Content-MD5 is the S3 wire header, not a security digest
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go/aws/credentials"
	v4 "github.com/aws/aws-sdk-go/aws/signer/v4"
)

// signedStubPut builds a PUT of body to the stub, signed the way the S3
// client signs one: Content-Length and Content-MD5 are headers when the
// request is signed, so both are covered by the signature. payloadDigest
// is the X-Amz-Content-Sha256 value; empty means the body's own SHA-256.
func signedStubPut(t *testing.T, stub *s3Stub, key, secret, region string, body []byte, payloadDigest string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, stub.URL()+"/"+stub.bucket+"/journal", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if payloadDigest == "" {
		sum := sha256.Sum256(body)
		payloadDigest = hex.EncodeToString(sum[:])
	}
	md5sum := md5.Sum(body) //nolint:gosec // see the import
	req.Header.Set("Content-Length", strconv.Itoa(len(body)))
	req.Header.Set("Content-Md5", base64.StdEncoding.EncodeToString(md5sum[:]))
	req.Header.Set("X-Amz-Content-Sha256", payloadDigest)
	signer := v4.NewSigner(credentials.NewStaticCredentials(key, secret, ""), func(sg *v4.Signer) {
		sg.DisableURIPathEscaping = true
	})
	if _, err := signer.Sign(req, bytes.NewReader(body), "s3", region, time.Now()); err != nil {
		t.Fatalf("sign: %v", err)
	}
	setStubBody(req, body)
	return req
}

func setStubBody(req *http.Request, body []byte) {
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
}

func doStubRequest(t *testing.T, req *http.Request) int {
	t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", req.Method, req.URL, err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode
}

// TestObjstoreS3Stub_SignatureCoversContentLengthAndMD5 proves the stub
// checks exactly what the client signed: a PUT signed over Content-Length
// and Content-MD5 is admitted, and the same PUT is refused once the length
// it carries, or its Content-MD5, differs from what was signed, or once it
// was signed with another secret.
func TestObjstoreS3Stub_SignatureCoversContentLengthAndMD5(t *testing.T) {
	const (
		bucket = "stub-bucket"
		region = "us-east-1"
		key    = "AKIASTUBSIGNATURE"
		secret = "stub-signature-secret"
	)
	body := []byte("journal entry")

	t.Run("signed PUT is admitted", func(t *testing.T) {
		stub := newS3Stub(t, bucket, region, key, secret)
		if code := doStubRequest(t, signedStubPut(t, stub, key, secret, region, body, "")); code != http.StatusOK {
			t.Fatalf("signed PUT: status %d, want 200; failures: %v", code, stub.failureList())
		}
		if got := stub.objectBodies()["journal"]; !bytes.Equal(got, body) {
			t.Fatalf("stored object = %q, want %q", got, body)
		}
	})

	refused := map[string]func(t *testing.T, stub *s3Stub) *http.Request{
		// The payload is unsigned, so only the signed Content-Length can
		// tell the longer body from the one the client signed.
		"carried length differs from the signed one": func(t *testing.T, stub *s3Stub) *http.Request {
			req := signedStubPut(t, stub, key, secret, region, body, "UNSIGNED-PAYLOAD")
			setStubBody(req, append(append([]byte(nil), body...), " and more"...))
			return req
		},
		"Content-MD5 differs from the signed one": func(t *testing.T, stub *s3Stub) *http.Request {
			req := signedStubPut(t, stub, key, secret, region, body, "")
			req.Header.Set("Content-Md5", base64.StdEncoding.EncodeToString(make([]byte, md5.Size)))
			return req
		},
		"signed with another secret": func(t *testing.T, stub *s3Stub) *http.Request {
			return signedStubPut(t, stub, key, "another-secret", region, body, "")
		},
	}
	for name, build := range refused {
		t.Run(name, func(t *testing.T) {
			stub := newS3Stub(t, bucket, region, key, secret)
			if code := doStubRequest(t, build(t, stub)); code != http.StatusForbidden {
				t.Fatalf("status %d, want 403", code)
			}
			failures := stub.failureList()
			if len(failures) != 1 || !strings.Contains(failures[0], "SignatureDoesNotMatch") {
				t.Fatalf("failures = %v, want one SignatureDoesNotMatch", failures)
			}
			if stub.verifiedCount() != 0 || len(stub.objectBodies()) != 0 {
				t.Fatalf("a refused PUT was admitted: verified %d, objects %v", stub.verifiedCount(), stub.objectBodies())
			}
		})
	}
}
