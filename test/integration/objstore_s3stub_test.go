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
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"hash/crc32"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go/aws/credentials"
	v4 "github.com/aws/aws-sdk-go/aws/signer/v4"

	"github.com/jeremyhahn/go-qrdb/pkg/storage/objstore"
)

// s3Stub is an in-memory S3 endpoint, path-style or virtual-hosted (as the
// HTTP proxy of a client whose endpoint names no real host), that admits a request only
// when its SigV4 signature recomputes with the access key and secret the
// test generated: a request signed with any other key or secret, or not
// signed at all, is answered 403 and recorded as a failure, and an
// operation the stub does not implement is answered 501 and recorded, so a
// missing verb is visible rather than silently tolerated.
type s3Stub struct {
	server *httptest.Server
	bucket string
	region string
	key    string
	secret string

	mu       sync.Mutex
	objects  map[string][]byte
	verified int
	requests []string
	failures []string
	signed   chan struct{}
	once     sync.Once
}

// newS3Stub starts the stub for bucket and closes it when the test ends.
func newS3Stub(t *testing.T, bucket, region, key, secret string) *s3Stub {
	t.Helper()
	s := &s3Stub{
		bucket:  bucket,
		region:  region,
		key:     key,
		secret:  secret,
		objects: make(map[string][]byte),
		signed:  make(chan struct{}),
	}
	s.server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.server.Close)
	return s
}

// URL is the stub's endpoint.
func (s *s3Stub) URL() string { return s.server.URL }

// awaitSignedRequest blocks until the stub has admitted one request whose
// signature verified, failing the test after timeout with the node's output.
func (s *s3Stub) awaitSignedRequest(t *testing.T, timeout time.Duration, node fmt.Stringer) {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-s.signed:
	case <-timer.C:
		t.Fatalf("no SigV4-verified request reached the S3 stub within %v; verified requests: %v; failures: %v\nnode output:\n%s", timeout, s.requestList(), s.failureList(), node)
	}
}

// verifiedCount reports how many requests verified against the generated
// credentials.
func (s *s3Stub) verifiedCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.verified
}

// requestList returns the verified requests in arrival order.
func (s *s3Stub) requestList() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.requests...)
}

// failureList returns the requests the stub refused or could not serve.
func (s *s3Stub) failureList() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.failures...)
}

// objectBodies returns a copy of every stored object, keyed by object key.
func (s *s3Stub) objectBodies() map[string][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string][]byte, len(s.objects))
	for k, v := range s.objects {
		out[k] = append([]byte(nil), v...)
	}
	return out
}

// The commit journal pkg/storage/objstore keeps beside an S3 store's data:
// journalName, its image journalMagic followed by records and a big-endian
// CRC-32C of everything before it (journal.go). The local provider keeps its
// journal in a sibling tree and clears it after every commit, so it cannot
// be copied from a local store like the rest of the layout; these copies of
// the unexported constants are checked by the node itself, which fails to
// open a store whose journal it cannot read.
const (
	objstoreJournalName  = "qrdb-objstore-journal"
	objstoreJournalMagic = "QRDBOJ1\x00"
)

// seedFormattedStore stores the objects of a formatted objstore store that
// holds values, plus a commit journal with no records, so an opening engine
// finds its format, the values, and nothing pending to recover. The layout
// is the objstore engine's own: the values are written through a local
// engine and its tree is copied object by object. It returns the names it
// seeded.
func (s *s3Stub) seedFormattedStore(t *testing.T, values map[string][]byte) []string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "bucket")
	e, err := objstore.New("local", map[string]string{"path": root})
	if err != nil {
		t.Fatalf("seed: open a local objstore engine: %v", err)
	}
	for k, v := range values {
		if err := e.Set([]byte(k), v); err != nil {
			t.Fatalf("seed: set %q: %v", k, err)
		}
	}
	if err := e.Close(); err != nil {
		t.Fatalf("seed: close the local objstore engine: %v", err)
	}
	seeded := map[string][]byte{}
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		body, err := os.ReadFile(path)
		seeded[filepath.ToSlash(rel)] = body
		return err
	})
	if err != nil {
		t.Fatalf("seed: copy the local objstore tree: %v", err)
	}
	journal := []byte(objstoreJournalMagic)
	seeded[objstoreJournalName] = binary.BigEndian.AppendUint32(journal, crc32.Checksum(journal, crc32.MakeTable(crc32.Castagnoli)))

	s.mu.Lock()
	defer s.mu.Unlock()
	names := make([]string, 0, len(seeded))
	for name, body := range seeded {
		s.objects[name] = body
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (s *s3Stub) fail(w http.ResponseWriter, status int, code, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	s.mu.Lock()
	s.failures = append(s.failures, fmt.Sprintf("%d %s: %s", status, code, msg))
	s.mu.Unlock()
	writeS3Error(w, status, code, msg)
}

func writeS3Error(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<Error><Code>%s</Code><Message>%s</Message></Error>", code, xmlEscape(msg))
}

func xmlEscape(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

func (s *s3Stub) serve(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		s.fail(w, http.StatusBadRequest, "IncompleteBody", "%s %s: read body: %v", r.Method, r.URL.Path, err)
		return
	}
	if err := s.verifySignature(r, body); err != nil {
		s.fail(w, http.StatusForbidden, "SignatureDoesNotMatch", "%s %s: %v", r.Method, r.URL.Path, err)
		return
	}
	s.mu.Lock()
	s.verified++
	s.requests = append(s.requests, r.Method+" "+r.Host+r.URL.RequestURI())
	s.mu.Unlock()
	s.once.Do(func() { close(s.signed) })

	// A virtual-hosted request names the bucket in its host, a path-style
	// one in its first path segment.
	bucket, key := "", strings.TrimPrefix(r.URL.Path, "/")
	if host, _, _ := strings.Cut(r.Host, ":"); strings.HasPrefix(host, s.bucket+".") {
		bucket = s.bucket
	} else {
		bucket, key, _ = strings.Cut(key, "/")
	}
	if bucket != s.bucket {
		s.fail(w, http.StatusNotFound, "NoSuchBucket", "%s %s: unknown bucket %q", r.Method, r.URL.Path, bucket)
		return
	}
	if key == "" {
		s.serveBucket(w, r, body)
		return
	}
	s.serveObject(w, r, key, body)
}

// verifySignature re-signs a clone of r with the generated credentials at
// r's X-Amz-Date over exactly the headers r signed, and compares the
// signatures. It also checks the access key named in the credential scope
// and, for a signed payload, that the body hashes to the signed digest.
func (s *s3Stub) verifySignature(r *http.Request, body []byte) error {
	auth := r.Header.Get("Authorization")
	const scheme = "AWS4-HMAC-SHA256 "
	if !strings.HasPrefix(auth, scheme) {
		return fmt.Errorf("not a SigV4 Authorization header")
	}
	fields := map[string]string{}
	for _, part := range strings.Split(strings.TrimPrefix(auth, scheme), ",") {
		name, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok {
			fields[name] = value
		}
	}
	accessKey, _, _ := strings.Cut(fields["Credential"], "/")
	if accessKey != s.key {
		return fmt.Errorf("signed with another access key")
	}
	if fields["Signature"] == "" || fields["SignedHeaders"] == "" {
		return fmt.Errorf("Authorization header lacks Signature or SignedHeaders")
	}
	signTime, err := time.Parse("20060102T150405Z", r.Header.Get("X-Amz-Date"))
	if err != nil {
		return fmt.Errorf("X-Amz-Date: %v", err)
	}
	digest := r.Header.Get("X-Amz-Content-Sha256")
	if digest != "" && digest != "UNSIGNED-PAYLOAD" {
		sum := sha256.Sum256(body)
		if hex.EncodeToString(sum[:]) != digest {
			return fmt.Errorf("body does not hash to the signed payload digest")
		}
	}

	clone := &http.Request{
		Method: r.Method,
		URL: &url.URL{
			Scheme:   "http",
			Host:     r.Host,
			Path:     r.URL.Path,
			RawPath:  r.URL.RawPath,
			RawQuery: r.URL.RawQuery,
		},
		Host:   r.Host,
		Header: http.Header{},
	}
	for _, name := range strings.Split(fields["SignedHeaders"], ";") {
		switch name {
		case "host":
		case "content-length":
			clone.ContentLength = r.ContentLength
		default:
			for _, v := range r.Header.Values(name) {
				clone.Header.Add(name, v)
			}
		}
	}
	signer := v4.NewSigner(credentials.NewStaticCredentials(s.key, s.secret, ""), func(sg *v4.Signer) {
		sg.DisableURIPathEscaping = true
	})
	if _, err := signer.Sign(clone, bytes.NewReader(body), "s3", s.region, signTime); err != nil {
		return fmt.Errorf("re-sign: %v", err)
	}
	want := ""
	for _, part := range strings.Split(clone.Header.Get("Authorization"), ",") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(part), "Signature="); ok {
			want = v
		}
	}
	if want == "" || want != fields["Signature"] {
		return fmt.Errorf("signature does not recompute with the generated secret")
	}
	return nil
}

func (s *s3Stub) serveBucket(w http.ResponseWriter, r *http.Request, body []byte) {
	q := r.URL.Query()
	switch {
	case r.Method == http.MethodHead && len(q) == 0, r.Method == http.MethodPut && len(q) == 0:
		w.WriteHeader(http.StatusOK)
	case r.Method == http.MethodGet && q.Get("list-type") == "2":
		s.listObjectsV2(w, q)
	case r.Method == http.MethodPost && q.Has("delete"):
		s.deleteObjects(w, r, body)
	default:
		s.fail(w, http.StatusNotImplemented, "NotImplemented", "bucket operation %s %s", r.Method, r.URL.RequestURI())
	}
}

func (s *s3Stub) serveObject(w http.ResponseWriter, r *http.Request, key string, body []byte) {
	if len(r.URL.Query()) != 0 || r.Header.Get("Range") != "" || r.Header.Get("X-Amz-Copy-Source") != "" {
		s.fail(w, http.StatusNotImplemented, "NotImplemented", "object operation %s %s", r.Method, r.URL.RequestURI())
		return
	}
	switch r.Method {
	case http.MethodPut:
		s.mu.Lock()
		s.objects[key] = body
		s.mu.Unlock()
		w.Header().Set("ETag", s3ETag(body))
		w.WriteHeader(http.StatusOK)
	case http.MethodGet, http.MethodHead:
		s.mu.Lock()
		obj, ok := s.objects[key]
		s.mu.Unlock()
		if !ok {
			if r.Method == http.MethodHead {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			writeS3Error(w, http.StatusNotFound, "NoSuchKey", "The specified key does not exist.")
			return
		}
		w.Header().Set("ETag", s3ETag(obj))
		w.Header().Set("Content-Length", fmt.Sprint(len(obj)))
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodGet {
			_, _ = w.Write(obj)
		}
	case http.MethodDelete:
		s.mu.Lock()
		delete(s.objects, key)
		s.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	default:
		s.fail(w, http.StatusNotImplemented, "NotImplemented", "object operation %s %s", r.Method, r.URL.RequestURI())
	}
}

func s3ETag(body []byte) string {
	sum := sha256.Sum256(body)
	return `"` + hex.EncodeToString(sum[:16]) + `"`
}

type s3ListResult struct {
	XMLName        xml.Name       `xml:"ListBucketResult"`
	Name           string         `xml:"Name"`
	Prefix         string         `xml:"Prefix"`
	Delimiter      string         `xml:"Delimiter,omitempty"`
	KeyCount       int            `xml:"KeyCount"`
	MaxKeys        int            `xml:"MaxKeys"`
	IsTruncated    bool           `xml:"IsTruncated"`
	Contents       []s3ListObject `xml:"Contents"`
	CommonPrefixes []s3ListPrefix `xml:"CommonPrefixes"`
}

type s3ListObject struct {
	Key          string `xml:"Key"`
	LastModified string `xml:"LastModified"`
	ETag         string `xml:"ETag"`
	Size         int    `xml:"Size"`
	StorageClass string `xml:"StorageClass"`
}

type s3ListPrefix struct {
	Prefix string `xml:"Prefix"`
}

// listObjectsV2 answers every matching key in one page.
func (s *s3Stub) listObjectsV2(w http.ResponseWriter, q url.Values) {
	prefix, delimiter := q.Get("prefix"), q.Get("delimiter")
	res := s3ListResult{Name: s.bucket, Prefix: prefix, Delimiter: delimiter, MaxKeys: 1000}
	seen := map[string]bool{}
	s.mu.Lock()
	keys := make([]string, 0, len(s.objects))
	for k := range s.objects {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		if delimiter != "" {
			if i := strings.Index(k[len(prefix):], delimiter); i >= 0 {
				p := k[:len(prefix)+i+len(delimiter)]
				if !seen[p] {
					seen[p] = true
					res.CommonPrefixes = append(res.CommonPrefixes, s3ListPrefix{Prefix: p})
				}
				continue
			}
		}
		res.Contents = append(res.Contents, s3ListObject{
			Key:          k,
			LastModified: time.Now().UTC().Format(time.RFC3339),
			ETag:         s3ETag(s.objects[k]),
			Size:         len(s.objects[k]),
			StorageClass: "STANDARD",
		})
	}
	s.mu.Unlock()
	res.KeyCount = len(res.Contents) + len(res.CommonPrefixes)
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, xml.Header)
	_ = xml.NewEncoder(w).Encode(res)
}

// deleteObjects answers a multi-object delete.
func (s *s3Stub) deleteObjects(w http.ResponseWriter, r *http.Request, body []byte) {
	var req struct {
		Objects []struct {
			Key string `xml:"Key"`
		} `xml:"Object"`
	}
	if err := xml.Unmarshal(body, &req); err != nil {
		s.fail(w, http.StatusBadRequest, "MalformedXML", "%s %s: %v", r.Method, r.URL.RequestURI(), err)
		return
	}
	var out bytes.Buffer
	out.WriteString(xml.Header + "<DeleteResult>")
	s.mu.Lock()
	for _, o := range req.Objects {
		delete(s.objects, o.Key)
		out.WriteString("<Deleted><Key>" + xmlEscape(o.Key) + "</Key></Deleted>")
	}
	s.mu.Unlock()
	out.WriteString("</DeleteResult>")
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out.Bytes())
}
