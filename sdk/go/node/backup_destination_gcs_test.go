//go:build gcpstorage

package node

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
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

// gcsObject is one object the GCS stub holds.
type gcsObject struct {
	body       []byte
	generation int64
	updated    time.Time
}

// gcsStub is an in-process GCS JSON and XML API serving one bucket from memory.
type gcsStub struct {
	t       *testing.T
	bucket  string
	mu      sync.Mutex
	objects map[string]gcsObject
	uploads map[string]string
	next    int64
}

func newGCSStub(t *testing.T, bucket string) (*gcsStub, *httptest.Server) {
	stub := &gcsStub{t: t, bucket: bucket, objects: map[string]gcsObject{}, uploads: map[string]string{}}
	srv := httptest.NewServer(stub)
	t.Cleanup(srv.Close)
	return stub, srv
}

func (s *gcsStub) names() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.objects))
	for name := range s.objects {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func (s *gcsStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.EscapedPath()
	q := r.URL.Query()
	jsonObjects := "/storage/v1/b/" + s.bucket + "/o"
	uploadObjects := "/upload/storage/v1/b/" + s.bucket + "/o"
	switch {
	case path == uploadObjects && r.Method == http.MethodPost && q.Get("uploadType") == "multipart":
		s.multipartUpload(w, r)
	case path == uploadObjects && r.Method == http.MethodPost && q.Get("uploadType") == "resumable":
		s.startResumable(w, r)
	case path == uploadObjects && r.Method == http.MethodPut && q.Get("upload_id") != "":
		s.finishResumable(w, r, q.Get("upload_id"))
	case path == jsonObjects && r.Method == http.MethodGet:
		s.list(w, q.Get("prefix"))
	case strings.HasPrefix(path, jsonObjects+"/"):
		name, ok := s.unescape(w, r, strings.TrimPrefix(path, jsonObjects+"/"))
		if !ok {
			return
		}
		switch r.Method {
		case http.MethodGet:
			obj, found := s.lookup(w, name)
			if !found {
				return
			}
			if q.Get("alt") == "media" {
				s.media(w, r, obj)
				return
			}
			s.writeResource(w, http.StatusOK, name, obj)
		case http.MethodDelete:
			s.mu.Lock()
			_, found := s.objects[name]
			delete(s.objects, name)
			s.mu.Unlock()
			if !found {
				gcsNotFound(w)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			s.unsupported(w, r)
		}
	case strings.HasPrefix(path, "/"+s.bucket+"/") && r.Method == http.MethodGet:
		name, ok := s.unescape(w, r, strings.TrimPrefix(path, "/"+s.bucket+"/"))
		if !ok {
			return
		}
		obj, found := s.lookup(w, name)
		if !found {
			return
		}
		s.media(w, r, obj)
	default:
		s.unsupported(w, r)
	}
}

func (s *gcsStub) unsupported(w http.ResponseWriter, r *http.Request) {
	s.t.Errorf("gcs stub: unsupported request %s %s?%s", r.Method, r.URL.EscapedPath(), r.URL.RawQuery)
	w.WriteHeader(http.StatusNotImplemented)
}

func (s *gcsStub) unescape(w http.ResponseWriter, r *http.Request, escaped string) (string, bool) {
	name, err := url.PathUnescape(escaped)
	if err != nil {
		s.t.Errorf("gcs stub: object name %q: %v", escaped, err)
		w.WriteHeader(http.StatusBadRequest)
		return "", false
	}
	return name, true
}

func (s *gcsStub) lookup(w http.ResponseWriter, name string) (gcsObject, bool) {
	s.mu.Lock()
	obj, ok := s.objects[name]
	s.mu.Unlock()
	if !ok {
		gcsNotFound(w)
	}
	return obj, ok
}

func (s *gcsStub) store(w http.ResponseWriter, name string, body []byte) {
	if name == "" {
		s.t.Errorf("gcs stub: upload without an object name")
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.next++
	obj := gcsObject{body: body, generation: s.next, updated: time.Now().UTC()}
	s.objects[name] = obj
	s.mu.Unlock()
	s.writeResource(w, http.StatusOK, name, obj)
}

func (s *gcsStub) multipartUpload(w http.ResponseWriter, r *http.Request) {
	_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		s.t.Errorf("gcs stub: multipart upload content type: %v", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	mr := multipart.NewReader(r.Body, params["boundary"])
	metaPart, err := mr.NextPart()
	if err != nil {
		s.t.Errorf("gcs stub: multipart upload metadata part: %v", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	var meta struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(metaPart).Decode(&meta); err != nil {
		s.t.Errorf("gcs stub: multipart upload metadata: %v", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	mediaPart, err := mr.NextPart()
	if err != nil {
		s.t.Errorf("gcs stub: multipart upload media part: %v", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	body, err := io.ReadAll(mediaPart)
	if err != nil {
		s.t.Errorf("gcs stub: multipart upload media: %v", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	name := meta.Name
	if name == "" {
		name = r.URL.Query().Get("name")
	}
	s.store(w, name, body)
}

func (s *gcsStub) startResumable(w http.ResponseWriter, r *http.Request) {
	var meta struct {
		Name string `json:"name"`
	}
	_ = json.NewDecoder(r.Body).Decode(&meta)
	name := meta.Name
	if name == "" {
		name = r.URL.Query().Get("name")
	}
	s.mu.Lock()
	s.next++
	id := strconv.FormatInt(s.next, 10)
	s.uploads[id] = name
	s.mu.Unlock()
	w.Header().Set("Location", "http://"+r.Host+"/upload/storage/v1/b/"+s.bucket+"/o?uploadType=resumable&upload_id="+id)
	w.WriteHeader(http.StatusOK)
}

func (s *gcsStub) finishResumable(w http.ResponseWriter, r *http.Request, id string) {
	s.mu.Lock()
	name, ok := s.uploads[id]
	delete(s.uploads, id)
	s.mu.Unlock()
	if !ok {
		s.unsupported(w, r)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		s.t.Errorf("gcs stub: resumable upload body: %v", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	s.store(w, name, body)
}

func (s *gcsStub) resource(name string, obj gcsObject) map[string]any {
	return map[string]any{
		"kind":           "storage#object",
		"bucket":         s.bucket,
		"name":           name,
		"size":           strconv.Itoa(len(obj.body)),
		"generation":     strconv.FormatInt(obj.generation, 10),
		"metageneration": "1",
		"contentType":    "application/octet-stream",
		"timeCreated":    obj.updated.Format(time.RFC3339Nano),
		"updated":        obj.updated.Format(time.RFC3339Nano),
	}
}

func (s *gcsStub) writeResource(w http.ResponseWriter, status int, name string, obj gcsObject) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(s.resource(name, obj))
}

func (s *gcsStub) media(w http.ResponseWriter, r *http.Request, obj gcsObject) {
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(len(obj.body)))
	w.Header().Set("Last-Modified", obj.updated.Format(http.TimeFormat))
	w.Header().Set("X-Goog-Generation", strconv.FormatInt(obj.generation, 10))
	w.Header().Set("X-Goog-Metageneration", "1")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(obj.body)
	}
}

func (s *gcsStub) list(w http.ResponseWriter, prefix string) {
	s.mu.Lock()
	names := make([]string, 0, len(s.objects))
	for name := range s.objects {
		if strings.HasPrefix(name, prefix) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	items := make([]map[string]any, 0, len(names))
	for _, name := range names {
		items = append(items, s.resource(name, s.objects[name]))
	}
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{"kind": "storage#objects", "items": items})
}

func gcsNotFound(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)
	_, _ = fmt.Fprint(w, `{"error":{"code":404,"message":"No such object.","errors":[{"domain":"global","reason":"notFound","message":"No such object."}]}}`)
}

func gcsDestination() *qrdbconfig.ObjstoreConfig {
	return &qrdbconfig.ObjstoreConfig{
		Provider: "gcs",
		Settings: map[string]string{"bucket": "qrdb-backups"},
	}
}

func TestBackupDestination_GCS_NodeWritesAndSecondNodeReads(t *testing.T) {
	ctx := context.Background()
	stub, srv := newGCSStub(t, "qrdb-backups")
	t.Setenv("STORAGE_EMULATOR_HOST", srv.URL)

	cfg := newTenantIsolationConfig(t)
	cfg.Backup.Destination = gcsDestination()
	first, err := NewStandaloneNode(ctx, cfg, nil)
	require.NoError(t, err)
	info, err := first.Services().Backup.CreateBackup(ctx, service.BackupOptions{
		Destination: "backup-destination-gcs",
		Compression: service.CompressionNone,
		Encryption:  service.EncryptionNone,
	})
	if err != nil {
		_ = first.Close()
		t.Fatalf("CreateBackup over a gcs backup.destination: %v", err)
	}
	require.NoError(t, first.Close())
	// A standalone node's backup holds no shard; the raft round trip proves a payload.
	require.Zero(t, info.ShardCount)
	require.NotEmpty(t, stub.names(), "the gcs bucket holds no objects after the backup")

	cfg2 := newTenantIsolationConfig(t)
	cfg2.Backup.Destination = gcsDestination()
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

func TestBackupDestination_GCS_RaftNodeRoundTrip(t *testing.T) {
	stub, srv := newGCSStub(t, "qrdb-backups")
	t.Setenv("STORAGE_EMULATOR_HOST", srv.URL)
	testRaftBackupDestinationRoundTrip(t, gcsDestination, stub.names)
}

func TestBackupDestination_GCS_PlaintextDeleteRemovesEveryObject(t *testing.T) {
	ctx := context.Background()
	stub, srv := newGCSStub(t, "qrdb-backups")
	t.Setenv("STORAGE_EMULATOR_HOST", srv.URL)

	cfg := newTenantIsolationConfig(t)
	cfg.Backup.Destination = gcsDestination()
	n, err := NewStandaloneNode(ctx, cfg, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = n.Close() })

	before := stub.names()
	info, err := n.Services().Backup.CreateBackup(ctx, service.BackupOptions{
		Destination: "backup-destination-gcs",
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
