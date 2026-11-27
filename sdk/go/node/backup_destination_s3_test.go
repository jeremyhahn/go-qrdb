//go:build awss3

package node

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// s3Stub is an in-memory path-style S3 endpoint holding one bucket.
type s3Stub struct {
	t        *testing.T
	bucket   string
	mu       sync.Mutex
	objects  map[string][]byte
	modified map[string]time.Time
}

func newS3Stub(t *testing.T, bucket string) (*s3Stub, *httptest.Server) {
	t.Helper()
	stub := &s3Stub{
		t:        t,
		bucket:   bucket,
		objects:  map[string][]byte{},
		modified: map[string]time.Time{},
	}
	srv := httptest.NewServer(stub)
	t.Cleanup(srv.Close)
	return stub, srv
}

func (s *s3Stub) names() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.objects))
	for name := range s.objects {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func (s *s3Stub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256") {
		s.t.Errorf("s3 stub: %s %s carries no SigV4 Authorization header", r.Method, r.URL.Path)
		http.Error(w, "unsigned request", http.StatusForbidden)
		return
	}
	bucketPath := "/" + s.bucket
	switch {
	case r.URL.Path == bucketPath || r.URL.Path == bucketPath+"/":
		if r.Method == http.MethodGet && r.URL.Query().Get("list-type") == "2" {
			s.list(w, r)
			return
		}
	case strings.HasPrefix(r.URL.Path, bucketPath+"/"):
		key := strings.TrimPrefix(r.URL.Path, bucketPath+"/")
		switch r.Method {
		case http.MethodPut:
			s.put(w, r, key)
			return
		case http.MethodGet, http.MethodHead:
			s.get(w, r, key)
			return
		case http.MethodDelete:
			s.mu.Lock()
			delete(s.objects, key)
			delete(s.modified, key)
			s.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}
	s.t.Errorf("s3 stub: unsupported request %s %s", r.Method, r.URL.String())
	w.WriteHeader(http.StatusNotImplemented)
}

func (s *s3Stub) put(w http.ResponseWriter, r *http.Request, key string) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.objects[key] = body
	s.modified[key] = time.Now().UTC()
	s.mu.Unlock()
	w.Header().Set("ETag", s3ETag(body))
	w.WriteHeader(http.StatusOK)
}

func (s *s3Stub) get(w http.ResponseWriter, r *http.Request, key string) {
	s.mu.Lock()
	body, ok := s.objects[key]
	modified := s.modified[key]
	s.mu.Unlock()
	if !ok {
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>NoSuchKey</Code><Message>The specified key does not exist.</Message></Error>`)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Header().Set("ETag", s3ETag(body))
	w.Header().Set("Last-Modified", modified.Format(http.TimeFormat))
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodGet {
		_, _ = w.Write(body)
	}
}

type s3ListContents struct {
	Key          string `xml:"Key"`
	LastModified string `xml:"LastModified"`
	ETag         string `xml:"ETag"`
	Size         int    `xml:"Size"`
	StorageClass string `xml:"StorageClass"`
}

type s3ListBucketResult struct {
	XMLName               xml.Name         `xml:"http://s3.amazonaws.com/doc/2006-03-01/ ListBucketResult"`
	Name                  string           `xml:"Name"`
	Prefix                string           `xml:"Prefix"`
	KeyCount              int              `xml:"KeyCount"`
	MaxKeys               int              `xml:"MaxKeys"`
	IsTruncated           bool             `xml:"IsTruncated"`
	ContinuationToken     string           `xml:"ContinuationToken,omitempty"`
	NextContinuationToken string           `xml:"NextContinuationToken,omitempty"`
	Contents              []s3ListContents `xml:"Contents"`
}

func (s *s3Stub) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	prefix := q.Get("prefix")
	maxKeys := 1000
	if v := q.Get("max-keys"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			http.Error(w, "bad max-keys", http.StatusBadRequest)
			return
		}
		maxKeys = n
	}
	token := q.Get("continuation-token")

	s.mu.Lock()
	keys := make([]string, 0, len(s.objects))
	for key := range s.objects {
		if strings.HasPrefix(key, prefix) && key > token {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	result := s3ListBucketResult{
		Name:              s.bucket,
		Prefix:            prefix,
		MaxKeys:           maxKeys,
		ContinuationToken: token,
	}
	if len(keys) > maxKeys {
		keys = keys[:maxKeys]
		result.IsTruncated = true
		result.NextContinuationToken = keys[len(keys)-1]
	}
	for _, key := range keys {
		body := s.objects[key]
		result.Contents = append(result.Contents, s3ListContents{
			Key:          key,
			LastModified: s.modified[key].Format("2006-01-02T15:04:05.000Z"),
			ETag:         s3ETag(body),
			Size:         len(body),
			StorageClass: "STANDARD",
		})
	}
	s.mu.Unlock()
	result.KeyCount = len(result.Contents)

	out, err := xml.Marshal(result)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, xml.Header)
	_, _ = w.Write(out)
}

func s3ETag(body []byte) string {
	sum := md5.Sum(body)
	return `"` + hex.EncodeToString(sum[:]) + `"`
}

// s3CredentialFiles writes a random key id and secret, owner-only, and turns off the instance metadata lookup.
func s3CredentialFiles(t *testing.T) (keyFile, secretFile string) {
	t.Helper()
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	dir := t.TempDir()
	keyFile = filepath.Join(dir, "key")
	secretFile = filepath.Join(dir, "secret")
	require.NoError(t, os.WriteFile(keyFile, []byte(randomHex(t, 10)), 0o600))
	require.NoError(t, os.WriteFile(secretFile, []byte(randomHex(t, 20)), 0o600))
	return keyFile, secretFile
}

func randomHex(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	_, err := rand.Read(b)
	require.NoError(t, err)
	return hex.EncodeToString(b)
}

func s3Destination(endpoint, keyFile, secretFile string) *qrdbconfig.ObjstoreConfig {
	return &qrdbconfig.ObjstoreConfig{
		Provider: "s3",
		Settings: map[string]string{
			"bucket":         "qrdb-backups",
			"region":         "us-east-1",
			"endpoint":       endpoint,
			"forcePathStyle": "true",
		},
		Credentials: qrdbconfig.ObjstoreCredentialsConfig{KeyFile: keyFile, SecretFile: secretFile},
	}
}

func TestBackupDestination_S3_NodeWritesAndSecondNodeReads(t *testing.T) {
	ctx := context.Background()
	stub, srv := newS3Stub(t, "qrdb-backups")
	keyFile, secretFile := s3CredentialFiles(t)

	cfg := newTenantIsolationConfig(t)
	cfg.Backup.Destination = s3Destination(srv.URL, keyFile, secretFile)
	first, err := NewStandaloneNode(ctx, cfg, nil)
	require.NoError(t, err)
	info, err := first.Services().Backup.CreateBackup(ctx, service.BackupOptions{
		Destination: "backup-destination-s3",
		Compression: service.CompressionNone,
		Encryption:  service.EncryptionNone,
	})
	if err != nil {
		_ = first.Close()
		t.Fatalf("CreateBackup over an s3 backup.destination: %v", err)
	}
	require.NoError(t, first.Close())
	// A standalone node's backup holds no shard; the raft round trip proves a payload.
	require.Zero(t, info.ShardCount)
	require.NotEmpty(t, stub.names(), "the s3 bucket holds no objects after the backup")

	cfg2 := newTenantIsolationConfig(t)
	cfg2.Backup.Destination = s3Destination(srv.URL, keyFile, secretFile)
	second, err := NewStandaloneNode(ctx, cfg2, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = second.Close() })

	listed, err := second.Services().Backup.ListBackups(ctx)
	require.NoError(t, err)
	ids := make([]string, 0, len(listed))
	for _, b := range listed {
		ids = append(ids, b.ID)
	}
	require.Contains(t, ids, info.ID)

	got, err := second.Services().Backup.GetBackup(ctx, info.ID)
	require.NoError(t, err)
	require.Equal(t, info.ID, got.ID)

	verified, err := second.Services().Backup.VerifyBackup(ctx, info.ID)
	require.NoError(t, err)
	require.True(t, verified.Valid, "%v", verified.Errors)
}

func TestBackupDestination_S3_RaftNodeRoundTrip(t *testing.T) {
	stub, srv := newS3Stub(t, "qrdb-backups")
	keyFile, secretFile := s3CredentialFiles(t)
	testRaftBackupDestinationRoundTrip(t,
		func() *qrdbconfig.ObjstoreConfig { return s3Destination(srv.URL, keyFile, secretFile) },
		stub.names)
}

func TestBackupDestination_S3_PlaintextDeleteRemovesEveryObject(t *testing.T) {
	ctx := context.Background()
	stub, srv := newS3Stub(t, "qrdb-backups")
	keyFile, secretFile := s3CredentialFiles(t)

	cfg := newTenantIsolationConfig(t)
	cfg.Backup.Destination = s3Destination(srv.URL, keyFile, secretFile)
	n, err := NewStandaloneNode(ctx, cfg, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = n.Close() })

	before := stub.names()
	info, err := n.Services().Backup.CreateBackup(ctx, service.BackupOptions{
		Destination: "backup-destination-s3",
		Compression: service.CompressionNone,
		Encryption:  service.EncryptionNone,
	})
	require.NoError(t, err)
	require.NotEqual(t, before, stub.names(), "the backup wrote no objects")

	require.NoError(t, n.Services().Backup.DeleteBackup(ctx, info.ID))
	require.Equal(t, before, stub.names())

	_, err = n.Services().Backup.GetBackup(ctx, info.ID)
	var notFound *service.BackupNotFoundError
	require.ErrorAs(t, err, &notFound)
}
