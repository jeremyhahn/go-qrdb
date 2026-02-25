// Copyright 2026 Jeremy Hahn
// Apache 2.0 License

//go:build integration

package integration

import (
	"crypto/tls"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestRESTHealthEndpoint tests the REST /health endpoint directly.
func TestRESTHealthEndpoint(t *testing.T) {
	cfg := LoadTestConfig()
	if !isServerAvailable(t, cfg) {
		t.Fatal("REST server not available")
	}

	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: loadCACertPool()},
		},
	}

	resp, err := client.Get(cfg.RESTBaseURL + "/health")
	assertNoError(t, err, "Health check request failed")
	defer resp.Body.Close()

	assertEqual(t, http.StatusOK, resp.StatusCode, "Expected 200 OK")

	body, err := io.ReadAll(resp.Body)
	assertNoError(t, err, "Failed to read response body")
	assertNotEmpty(t, string(body), "Health response should not be empty")

	t.Logf("REST health response: %s", strings.TrimSpace(string(body)))
}

// TestRESTPutGetDelete tests KV operations via REST HTTP client directly.
func TestRESTPutGetDelete(t *testing.T) {
	cfg := LoadTestConfig()
	if !isServerAvailable(t, cfg) {
		t.Fatal("REST server not available")
	}

	pool := loadCACertPool()
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: pool},
		},
	}

	key := generateUniqueID("rest-test")
	value := "hello-rest-world"

	// PUT
	putReq, err := http.NewRequest(http.MethodPut, cfg.RESTBaseURL+"/kv/"+key, strings.NewReader(value))
	assertNoError(t, err, "Failed to create PUT request")
	putResp, err := client.Do(putReq)
	assertNoError(t, err, "PUT request failed")
	putResp.Body.Close()
	assertEqual(t, http.StatusOK, putResp.StatusCode, "Expected 200 on PUT")

	// GET
	getResp, err := client.Get(cfg.RESTBaseURL + "/kv/" + key)
	assertNoError(t, err, "GET request failed")
	body, err := io.ReadAll(getResp.Body)
	getResp.Body.Close()
	assertNoError(t, err, "Failed to read GET body")
	assertEqual(t, value, string(body), "GET should return stored value")

	// DELETE
	delReq, err := http.NewRequest(http.MethodDelete, cfg.RESTBaseURL+"/kv/"+key, nil)
	assertNoError(t, err, "Failed to create DELETE request")
	delResp, err := client.Do(delReq)
	assertNoError(t, err, "DELETE request failed")
	delResp.Body.Close()
	assertEqual(t, http.StatusOK, delResp.StatusCode, "Expected 200 on DELETE")

	// Verify gone
	getResp2, err := client.Get(cfg.RESTBaseURL + "/kv/" + key)
	assertNoError(t, err, "GET after DELETE request failed")
	getResp2.Body.Close()
	assertTrue(t, getResp2.StatusCode == http.StatusNotFound || getResp2.StatusCode == http.StatusInternalServerError,
		"Expected not-found status after DELETE")

	t.Logf("REST KV round-trip succeeded for key: %s", key)
}
