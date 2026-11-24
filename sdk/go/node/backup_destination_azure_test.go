//go:build azureblob

package node

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
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

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/stretchr/testify/require"
)

// azureBlobStub is an in-process Azure Blob service holding one container's block blobs in memory.
type azureBlobStub struct {
	t        *testing.T
	prefix   string
	mu       sync.Mutex
	blobs    map[string][]byte
	modified map[string]time.Time
	staged   map[string]map[string][]byte
}

func newAzureBlobStub(t *testing.T, account, container string) (*azureBlobStub, *httptest.Server) {
	stub := &azureBlobStub{
		t:        t,
		prefix:   "/" + account + "/" + container,
		blobs:    map[string][]byte{},
		modified: map[string]time.Time{},
		staged:   map[string]map[string][]byte{},
	}
	srv := httptest.NewServer(stub)
	t.Cleanup(srv.Close)
	return stub, srv
}

func (s *azureBlobStub) names() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.blobs))
	for name := range s.blobs {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func (s *azureBlobStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	path := r.URL.Path
	if path == s.prefix && r.Method == http.MethodGet && q.Get("restype") == "container" && q.Get("comp") == "list" {
		s.list(w, q.Get("prefix"))
		return
	}
	if !strings.HasPrefix(path, s.prefix+"/") {
		s.unsupported(w, r)
		return
	}
	name := strings.TrimPrefix(path, s.prefix+"/")
	switch {
	case r.Method == http.MethodPut && q.Get("comp") == "block":
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		if s.staged[name] == nil {
			s.staged[name] = map[string][]byte{}
		}
		s.staged[name][q.Get("blockid")] = body
		s.mu.Unlock()
		w.Header().Set("Content-MD5", contentMD5(body))
		w.WriteHeader(http.StatusCreated)
	case r.Method == http.MethodPut && q.Get("comp") == "blocklist":
		s.commitBlockList(w, r, name)
	case r.Method == http.MethodPut && q.Get("comp") == "" && r.Header.Get("x-ms-blob-type") == "BlockBlob":
		body, _ := io.ReadAll(r.Body)
		s.store(w, name, body)
	case (r.Method == http.MethodGet || r.Method == http.MethodHead) && q.Get("comp") == "":
		s.mu.Lock()
		body, ok := s.blobs[name]
		mod := s.modified[name]
		s.mu.Unlock()
		if !ok {
			blobNotFound(w, r.Method)
			return
		}
		blobHeaders(w, name, body, mod)
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodGet {
			_, _ = w.Write(body)
		}
	case r.Method == http.MethodDelete && q.Get("comp") == "":
		s.mu.Lock()
		_, ok := s.blobs[name]
		delete(s.blobs, name)
		delete(s.modified, name)
		s.mu.Unlock()
		if !ok {
			blobNotFound(w, r.Method)
			return
		}
		w.WriteHeader(http.StatusAccepted)
	default:
		s.unsupported(w, r)
	}
}

func (s *azureBlobStub) unsupported(w http.ResponseWriter, r *http.Request) {
	s.t.Errorf("azure stub: unsupported request %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
	w.WriteHeader(http.StatusNotImplemented)
}

func (s *azureBlobStub) store(w http.ResponseWriter, name string, body []byte) {
	now := time.Now().UTC()
	s.mu.Lock()
	s.blobs[name] = body
	s.modified[name] = now
	delete(s.staged, name)
	s.mu.Unlock()
	blobHeaders(w, name, body, now)
	w.Header().Set("Content-MD5", contentMD5(body))
	w.WriteHeader(http.StatusCreated)
}

func (s *azureBlobStub) commitBlockList(w http.ResponseWriter, r *http.Request, name string) {
	var list struct {
		Items []struct {
			XMLName xml.Name
			ID      string `xml:",chardata"`
		} `xml:",any"`
	}
	if err := xml.NewDecoder(r.Body).Decode(&list); err != nil {
		s.t.Errorf("azure stub: block list for %s: %v", name, err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	blocks := s.staged[name]
	s.mu.Unlock()
	var body []byte
	for _, item := range list.Items {
		block, ok := blocks[item.ID]
		if !ok {
			s.t.Errorf("azure stub: block list for %s names unknown block %q", name, item.ID)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		body = append(body, block...)
	}
	s.store(w, name, body)
}

func (s *azureBlobStub) list(w http.ResponseWriter, prefix string) {
	type properties struct {
		LastModified  string `xml:"Last-Modified"`
		Etag          string `xml:"Etag"`
		ContentLength int    `xml:"Content-Length"`
		BlobType      string `xml:"BlobType"`
	}
	type blob struct {
		Name       string     `xml:"Name"`
		Properties properties `xml:"Properties"`
	}
	type results struct {
		XMLName    xml.Name `xml:"EnumerationResults"`
		Prefix     string   `xml:"Prefix"`
		Blobs      []blob   `xml:"Blobs>Blob"`
		NextMarker string   `xml:"NextMarker"`
	}
	out := results{Prefix: prefix}
	s.mu.Lock()
	for name, body := range s.blobs {
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		out.Blobs = append(out.Blobs, blob{Name: name, Properties: properties{
			LastModified:  s.modified[name].Format(http.TimeFormat),
			Etag:          etag(name, body),
			ContentLength: len(body),
			BlobType:      "BlockBlob",
		}})
	}
	s.mu.Unlock()
	sort.Slice(out.Blobs, func(i, j int) bool { return out.Blobs[i].Name < out.Blobs[j].Name })
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, xml.Header)
	_ = xml.NewEncoder(w).Encode(out)
}

func blobHeaders(w http.ResponseWriter, name string, body []byte, mod time.Time) {
	w.Header().Set("x-ms-blob-type", "BlockBlob")
	w.Header().Set("Last-Modified", mod.Format(http.TimeFormat))
	w.Header().Set("ETag", etag(name, body))
}

func blobNotFound(w http.ResponseWriter, method string) {
	w.Header().Set("x-ms-error-code", "BlobNotFound")
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(http.StatusNotFound)
	if method != http.MethodHead {
		_, _ = io.WriteString(w, xml.Header+"<Error><Code>BlobNotFound</Code><Message>The specified blob does not exist.</Message></Error>")
	}
}

func etag(name string, body []byte) string {
	sum := md5.Sum(append([]byte(name+"\x00"), body...))
	return `"0x` + strings.ToUpper(hex.EncodeToString(sum[:8])) + `"`
}

func contentMD5(body []byte) string {
	sum := md5.Sum(body)
	return base64.StdEncoding.EncodeToString(sum[:])
}

func azureAccountKeyFile(t *testing.T) string {
	key := make([]byte, 32)
	_, err := rand.Read(key)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "account-key")
	require.NoError(t, os.WriteFile(path, []byte(base64.StdEncoding.EncodeToString(key)), 0o600))
	return path
}

func azureDestination(endpoint, keyFile string) *qrdbconfig.ObjstoreConfig {
	return &qrdbconfig.ObjstoreConfig{
		Provider: "azure",
		Settings: map[string]string{
			"accountName":   "devstoreaccount1",
			"containerName": "qrdb-backups",
			"endpoint":      endpoint,
		},
		Credentials: qrdbconfig.ObjstoreCredentialsConfig{SecretFile: keyFile},
	}
}

func TestBackupDestination_Azure_NodeWritesAndSecondNodeReads(t *testing.T) {
	ctx := context.Background()
	stub, srv := newAzureBlobStub(t, "devstoreaccount1", "qrdb-backups")
	endpoint := srv.URL + "/devstoreaccount1"
	keyFile := azureAccountKeyFile(t)

	cfg := newTenantIsolationConfig(t)
	cfg.Backup.Destination = azureDestination(endpoint, keyFile)
	first, err := NewStandaloneNode(ctx, cfg, nil)
	require.NoError(t, err)
	info, err := first.Services().Backup.CreateBackup(ctx, service.BackupOptions{
		Destination: "backup-destination-azure",
		Compression: service.CompressionNone,
		Encryption:  service.EncryptionNone,
	})
	if err != nil {
		_ = first.Close()
		t.Fatalf("CreateBackup over an azure backup.destination: %v", err)
	}
	require.NoError(t, first.Close())
	require.NotEmpty(t, stub.names(), "the azure container holds no objects after the backup")

	cfg2 := newTenantIsolationConfig(t)
	cfg2.Backup.Destination = azureDestination(endpoint, keyFile)
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

	_, err = second.Services().Backup.VerifyBackup(ctx, info.ID)
	require.NoError(t, err)
}

func TestBackupDestination_Azure_MissingContainerSettingRefused(t *testing.T) {
	_, srv := newAzureBlobStub(t, "devstoreaccount1", "qrdb-backups")
	dest := azureDestination(srv.URL+"/devstoreaccount1", azureAccountKeyFile(t))
	delete(dest.Settings, "containerName")

	cfg := newTenantIsolationConfig(t)
	cfg.Backup.Destination = dest
	n, err := NewStandaloneNode(context.Background(), cfg, nil)
	if n != nil {
		_ = n.Close()
	}
	var destErr *lifecycle.BackupDestinationError
	require.True(t, errors.As(err, &destErr), "want *lifecycle.BackupDestinationError, got %v", err)
	require.Equal(t, "azure", destErr.Provider, fmt.Sprintf("error: %v", err))
	require.Nil(t, n)
}
