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
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
)

// Default constants for the Voyage AI provider.
const (
	// DefaultVoyageAPIURL is the canonical Voyage AI embeddings endpoint.
	DefaultVoyageAPIURL = "https://api.voyageai.com/v1/embeddings"

	// DefaultVoyageTimeout is the default per-request HTTP timeout.
	DefaultVoyageTimeout = 30 * time.Second

	// DefaultVoyageModel is the default code-optimised embedding model.
	DefaultVoyageModel = "voyage-code-2"
)

// voyageModelSpec holds the specifications for a known Voyage AI model.
type voyageModelSpec struct {
	dimensions int
	maxTokens  int
}

// voyageModels maps Voyage AI model names to their specifications.
var voyageModels = map[string]voyageModelSpec{
	"voyage-code-2":  {dimensions: 1536, maxTokens: 32000},
	"voyage-2":       {dimensions: 1024, maxTokens: 16000},
	"voyage-large-2": {dimensions: 1536, maxTokens: 16000},
}

// voyageRequest is the Voyage AI embeddings request body.
type voyageRequest struct {
	Input []string `json:"input"`
	Model string   `json:"model"`
}

// voyageResponse is the Voyage AI embeddings response body.
type voyageResponse struct {
	Object string             `json:"object"`
	Data   []voyageEmbedding  `json:"data"`
	Model  string             `json:"model"`
	Usage  voyageUsage        `json:"usage"`
}

// voyageEmbedding is a single embedding entry in a voyageResponse.
type voyageEmbedding struct {
	Object    string    `json:"object"`
	Embedding []float32 `json:"embedding"`
	Index     int       `json:"index"`
}

// voyageUsage reports token consumption for a Voyage AI request.
type voyageUsage struct {
	TotalTokens int `json:"total_tokens"`
}

// voyageErrorResponse is the error body returned by the Voyage AI API.
type voyageErrorResponse struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error"`
}

// VoyageProvider implements Provider for Voyage AI.
// Unlike OpenAI-compatible providers, Voyage AI uses a single absolute URL
// for all embedding requests rather than a base + path structure.
//
// Compile-time assertion that VoyageProvider satisfies Provider.
var _ Provider = (*VoyageProvider)(nil)

// VoyageProvider sends embedding requests to the Voyage AI API.
type VoyageProvider struct {
	apiKey     string
	apiURL     string
	model      string
	dimensions int
	httpClient *http.Client
	maxRetries int
	backoffMS  int
}

// NewVoyageProvider creates a new VoyageProvider from the given Config.
// APIKey must be non-empty; all other fields default if zero.
func NewVoyageProvider(config Config) (*VoyageProvider, error) {
	if config.APIKey == "" {
		return nil, ErrAPIKeyRequired
	}

	model := config.Model
	if model == "" {
		model = DefaultVoyageModel
	}

	spec, ok := voyageModels[model]
	if !ok {
		return nil, &ModelNotFoundError{Model: model}
	}

	apiURL := config.APIURL
	if apiURL == "" {
		apiURL = DefaultVoyageAPIURL
	}

	timeout := time.Duration(config.Timeout) * time.Second
	if timeout <= 0 {
		timeout = DefaultVoyageTimeout
	}

	maxRetries := config.MaxRetries
	if maxRetries < 0 {
		maxRetries = 3
	}

	backoffMS := config.RetryBackoffMS
	if backoffMS <= 0 {
		backoffMS = 1000
	}

	dimensions := spec.dimensions
	if config.Dimensions > 0 {
		dimensions = config.Dimensions
	}

	return &VoyageProvider{
		apiKey:     config.APIKey,
		apiURL:     apiURL,
		model:      model,
		dimensions: dimensions,
		httpClient: &http.Client{Timeout: timeout},
		maxRetries: maxRetries,
		backoffMS:  backoffMS,
	}, nil
}

// Embed generates embeddings for the given texts using the specified model.
// If model is empty it falls back to the configured default model.
func (v *VoyageProvider) Embed(ctx context.Context, texts []string, model string) (*EmbeddingResult, error) {
	if len(texts) == 0 {
		return nil, &InvalidInputError{}
	}
	if model == "" {
		model = v.model
	}

	body, err := json.Marshal(voyageRequest{Input: texts, Model: model})
	if err != nil {
		return nil, &MarshalError{Cause: err}
	}

	var lastErr error
	maxAttempts := v.maxRetries + 1
	if maxAttempts < 1 {
		maxAttempts = 1
	}

	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			delay := voyageBackoff(attempt, v.backoffMS)
			select {
			case <-ctx.Done():
				return nil, &ProviderUnavailableError{BaseURL: v.apiURL, Cause: ctx.Err()}
			case <-time.After(delay):
			}
		}

		result, err := v.doOnce(ctx, body, texts, model)
		if err == nil {
			return result, nil
		}
		lastErr = err

		// Do not retry on permanent client errors.
		if httpErr, ok := err.(*HTTPError); ok {
			code := httpErr.StatusCode
			if code >= 400 && code < 500 && code != http.StatusTooManyRequests {
				return nil, err
			}
		}
	}

	return nil, &RetryExhaustedError{Attempts: maxAttempts, Cause: lastErr}
}

// doOnce sends a single request to the Voyage AI API.
func (v *VoyageProvider) doOnce(ctx context.Context, body []byte, texts []string, model string) (*EmbeddingResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, v.apiURL, bytes.NewReader(body))
	if err != nil {
		return nil, &ProviderUnavailableError{BaseURL: v.apiURL, Cause: err}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+v.apiKey)

	resp, err := v.httpClient.Do(req)
	if err != nil {
		return nil, &ProviderUnavailableError{BaseURL: v.apiURL, Cause: err}
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &UnmarshalError{Cause: err}
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var errResp voyageErrorResponse
		msg := strings.TrimSpace(string(raw))
		if json.Unmarshal(raw, &errResp) == nil && errResp.Error.Message != "" {
			msg = errResp.Error.Message
		}
		return nil, &HTTPError{StatusCode: resp.StatusCode, Body: msg}
	}

	var voyageResp voyageResponse
	if err := json.Unmarshal(raw, &voyageResp); err != nil {
		return nil, &UnmarshalError{Cause: err}
	}

	if len(voyageResp.Data) == 0 {
		return nil, &EmptyResponseError{}
	}

	embeddings := make([][]float32, len(texts))
	for _, d := range voyageResp.Data {
		if d.Index < 0 || d.Index >= len(texts) {
			return nil, fmt.Errorf("embedding: voyage returned invalid index %d for %d texts", d.Index, len(texts))
		}
		embeddings[d.Index] = d.Embedding
	}

	for i, emb := range embeddings {
		if emb == nil {
			return nil, fmt.Errorf("embedding: voyage did not return embedding at index %d", i)
		}
	}

	return &EmbeddingResult{
		Embeddings: embeddings,
		Model:      voyageResp.Model,
		Usage: &UsageInfo{
			TotalTokens: voyageResp.Usage.TotalTokens,
		},
	}, nil
}

// Dimensions returns the embedding vector dimensionality for the configured model.
func (v *VoyageProvider) Dimensions() int {
	return v.dimensions
}

// Name returns the provider identifier.
func (v *VoyageProvider) Name() string {
	return "voyage"
}

// Models returns a static list of known Voyage AI models.
// Voyage AI does not expose a /v1/models endpoint, so this returns the
// hard-coded set of supported models.
func (v *VoyageProvider) Models(_ context.Context) ([]ModelInfo, error) {
	models := make([]ModelInfo, 0, len(voyageModels))
	for name, spec := range voyageModels {
		models = append(models, ModelInfo{
			Name:       name,
			Dimensions: spec.dimensions,
			MaxTokens:  spec.maxTokens,
		})
	}
	return models, nil
}

// Close releases idle HTTP connections.
func (v *VoyageProvider) Close() error {
	v.httpClient.CloseIdleConnections()
	return nil
}

// voyageBackoff returns the exponential backoff duration for the given attempt
// number (1-based), capped at 10 seconds.
func voyageBackoff(attempt, baseMS int) time.Duration {
	d := time.Duration(float64(time.Duration(baseMS)*time.Millisecond) * math.Pow(2, float64(attempt-1)))
	if d > 10*time.Second {
		return 10 * time.Second
	}
	return d
}
