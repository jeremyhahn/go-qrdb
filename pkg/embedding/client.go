//go:build qrdb_vector

// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-qrdb.
//
// go-qrdb is dual-licensed:
//
// 1. GNU Affero General Public License v3.0 (AGPL-3.0)
//    See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
// 2. Commercial License
//    Contact licensing@automatethethings.com for commercial licensing options.

package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
)

// retryBaseDelay is the initial backoff duration before the first retry.
const retryBaseDelay = 500 * time.Millisecond

// retryMaxDelay caps the exponential backoff to avoid unbounded waits.
const retryMaxDelay = 10 * time.Second

// Compile-time assertion that Client satisfies Provider.
var _ Provider = (*Client)(nil)

// Client is an OpenAI-compatible HTTP client for generating vector embeddings.
// It supports any provider (Ollama, OpenAI, vLLM, etc.) that implements the
// /v1/embeddings and /v1/models endpoints.
type Client struct {
	config     *ProviderConfig
	httpClient *http.Client
}

// NewClient creates a new Client from the given ProviderConfig.
// It returns an error if the config is nil or BaseURL is empty.
func NewClient(config *ProviderConfig) (*Client, error) {
	if config == nil {
		config = DefaultProviderConfig()
	}
	if config.BaseURL == "" {
		return nil, &InvalidInputError{}
	}
	// Normalise: strip trailing slash so URL joins are consistent.
	config.BaseURL = strings.TrimRight(config.BaseURL, "/")

	timeout := config.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	return &Client{
		config: config,
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}, nil
}

// Embed generates embeddings for the given texts using the specified model.
// If model is empty, the client falls back to the model set in ProviderConfig.
// It retries on 5xx status codes and network errors with exponential backoff.
func (c *Client) Embed(ctx context.Context, texts []string, model string) (*EmbeddingResult, error) {
	if len(texts) == 0 {
		return nil, &InvalidInputError{}
	}
	if model == "" {
		model = c.config.Model
	}

	body, err := json.Marshal(embeddingRequest{
		Input:          texts,
		Model:          model,
		EncodingFormat: "float",
	})
	if err != nil {
		return nil, &MarshalError{Cause: err}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.config.BaseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, &ProviderUnavailableError{BaseURL: c.config.BaseURL, Cause: err}
	}
	req.Header.Set("Content-Type", "application/json")
	if c.config.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.config.APIKey)
	}

	resp, err := c.doWithRetry(ctx, req, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &UnmarshalError{Cause: err}
	}

	var embResp embeddingResponse
	if err := json.Unmarshal(raw, &embResp); err != nil {
		return nil, &UnmarshalError{Cause: err}
	}

	if len(embResp.Data) == 0 {
		return nil, &EmptyResponseError{}
	}

	// Sort by index to guarantee input order is preserved.
	embeddings := make([][]float32, len(embResp.Data))
	for _, d := range embResp.Data {
		if d.Index >= len(embeddings) {
			continue
		}
		embeddings[d.Index] = d.Embedding
	}

	return &EmbeddingResult{
		Embeddings: embeddings,
		Model:      embResp.Model,
		Usage: &UsageInfo{
			PromptTokens: embResp.Usage.PromptTokens,
			TotalTokens:  embResp.Usage.TotalTokens,
		},
	}, nil
}

// Models returns the list of embedding models available from the provider by
// calling GET /v1/models.
func (c *Client) Models(ctx context.Context) ([]ModelInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.config.BaseURL+"/models", nil)
	if err != nil {
		return nil, &ProviderUnavailableError{BaseURL: c.config.BaseURL, Cause: err}
	}
	if c.config.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.config.APIKey)
	}

	resp, err := c.doWithRetry(ctx, req, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &UnmarshalError{Cause: err}
	}

	var listResp modelListResponse
	if err := json.Unmarshal(raw, &listResp); err != nil {
		return nil, &UnmarshalError{Cause: err}
	}

	models := make([]ModelInfo, len(listResp.Data))
	for i, d := range listResp.Data {
		models[i] = ModelInfo{Name: d.ID}
	}
	return models, nil
}

// Close releases idle HTTP connections held by the underlying transport.
// Close is idempotent.
func (c *Client) Close() error {
	if t, ok := c.httpClient.Transport.(*http.Transport); ok {
		t.CloseIdleConnections()
	} else {
		// Default transport is a *http.Transport accessed via the unexported field;
		// calling CloseIdleConnections on the Client itself covers the default case.
		http.DefaultTransport.(*http.Transport).CloseIdleConnections() //nolint:forcetypeassert
	}
	return nil
}

// doWithRetry executes req, retrying on 5xx responses and network errors.
// For POST requests, body contains the raw bytes so the body can be replayed
// on retry (http.Request.Body is consumed after the first read).
func (c *Client) doWithRetry(ctx context.Context, req *http.Request, body []byte) (*http.Response, error) {
	maxAttempts := c.config.MaxRetries + 1
	if maxAttempts < 1 {
		maxAttempts = 1
	}

	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			delay := backoffDelay(attempt)
			select {
			case <-ctx.Done():
				return nil, &ProviderUnavailableError{BaseURL: c.config.BaseURL, Cause: ctx.Err()}
			case <-time.After(delay):
			}
			// Rebuild the request body for the retry since it was consumed.
			if body != nil {
				req.Body = io.NopCloser(bytes.NewReader(body))
				req.ContentLength = int64(len(body))
			}
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = &ProviderUnavailableError{BaseURL: c.config.BaseURL, Cause: err}
			continue
		}

		// 2xx — success.
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return resp, nil
		}

		// 4xx (except 429) — client errors, do not retry.
		if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests {
			raw, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			return nil, &HTTPError{StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(raw))}
		}

		// 5xx or 429 — server/rate-limit errors, retry.
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		lastErr = &HTTPError{StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(raw))}
	}

	return nil, &RetryExhaustedError{Attempts: maxAttempts, Cause: lastErr}
}

// backoffDelay returns the exponential backoff duration for the given attempt
// number (1-based), capped at retryMaxDelay.
func backoffDelay(attempt int) time.Duration {
	d := time.Duration(float64(retryBaseDelay) * math.Pow(2, float64(attempt-1)))
	if d > retryMaxDelay {
		return retryMaxDelay
	}
	return d
}
