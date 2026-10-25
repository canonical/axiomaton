// Copyright (c) 2026 Canonical Ltd
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License version 3 as
// published by the Free Software Foundation.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with this program.  If not, see <http://www.gnu.org/licenses/>.

// Package openrouter implements the model roles against the OpenRouter API
// (and, for embeddings and chat, any OpenAI compatible endpoint).
package openrouter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/canonical/axiomaton/internal/config"
	"github.com/canonical/axiomaton/internal/models"
)

// Client talks to one endpoint.
type Client struct {
	Endpoint config.Endpoint
	HTTP     *http.Client
	// Retry controls backoff after rate limits and transient failures.
	Retry config.Retry
	// Log, if set, receives a line per retry.
	Log func(format string, args ...any)
	// sleep is replaced in tests.
	sleep func(ctx context.Context, d time.Duration) error
}

// New creates a client for the resolved endpoint with the default retry
// policy.
func New(ep config.Endpoint) *Client {
	return &Client{Endpoint: ep, HTTP: &http.Client{Timeout: 120 * time.Second}, Retry: config.DefaultUser().Retry}
}

// WithRetry sets the retry policy and returns the client for chaining.
func (c *Client) WithRetry(r config.Retry, log func(string, ...any)) *Client {
	if r.MaxAttempts > 0 {
		c.Retry = r
	}
	c.Log = log
	return c
}

// Name returns the model identifier.
func (c *Client) Name() string { return c.Endpoint.Model }

// v1 joins a path onto the /v1 API root. Base URLs that already end in /v1
// are respected so OpenAI compatible servers work unchanged.
func (c *Client) v1(path string) string {
	base := c.Endpoint.BaseURL
	if !strings.HasSuffix(base, "/v1") {
		base += "/v1"
	}
	return base + path
}

// alpha joins a path onto the OpenRouter alpha API root.
func (c *Client) alpha(path string) string {
	base := strings.TrimSuffix(c.Endpoint.BaseURL, "/v1")
	return base + "/alpha" + path
}

// post sends req as JSON and decodes the response into resp.
func (c *Client) post(ctx context.Context, url string, req, resp any) error {
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	return c.request(ctx, http.MethodPost, url, body, resp)
}

// get fetches url and decodes the response into resp.
func (c *Client) get(ctx context.Context, url string, resp any) error {
	return c.request(ctx, http.MethodGet, url, nil, resp)
}

// request performs one API call, retrying rate limited and transient
// failures with exponential backoff and full jitter.
func (c *Client) request(ctx context.Context, method, url string, body []byte, resp any) error {
	attempts := max(c.Retry.MaxAttempts, 1)
	var last error
	for attempt := 1; ; attempt++ {
		data, retryAfter, err := c.do(ctx, method, url, body)
		if err == nil {
			if err := json.Unmarshal(data, resp); err != nil {
				return fmt.Errorf("%s: decode response: %w: %s", url, err, strings.TrimSpace(string(data)))
			}
			return nil
		}
		last = err
		var te *transientError
		if !errors.As(err, &te) || attempt >= attempts || ctx.Err() != nil {
			break
		}
		delay := c.backoff(attempt, retryAfter)
		if c.Log != nil {
			c.Log("%s: %s; retrying in %s (attempt %d of %d)", c.Endpoint.Model, te.reason, delay.Round(time.Millisecond), attempt+1, attempts)
		}
		if err := c.pause(ctx, delay); err != nil {
			return fmt.Errorf("%w (while waiting to retry: %v)", last, err)
		}
	}
	if attempts > 1 {
		if _, ok := errors.AsType[*transientError](last); ok {
			return fmt.Errorf("%w (gave up after %d attempts)", last, attempts)
		}
	}
	return last
}

// transientError marks a failure that is worth retrying.
type transientError struct {
	reason string
	err    error
}

func (e *transientError) Error() string { return e.err.Error() }
func (e *transientError) Unwrap() error { return e.err }

// do performs one HTTP attempt. Retryable failures are returned as
// *transientError together with any Retry-After hint from the server.
func (c *Client) do(ctx context.Context, method, url string, body []byte) ([]byte, time.Duration, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	hreq, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		return nil, 0, err
	}
	if c.Endpoint.APIKey != "" {
		hreq.Header.Set("Authorization", "Bearer "+c.Endpoint.APIKey)
	}
	if body != nil {
		hreq.Header.Set("Content-Type", "application/json")
	}
	hreq.Header.Set("HTTP-Referer", "https://github.com/canonical/axiomaton")
	hreq.Header.Set("X-Title", "axiomaton")
	hresp, err := c.HTTP.Do(hreq)
	if err != nil {
		if ctx.Err() != nil {
			return nil, 0, err
		}
		return nil, 0, &transientError{reason: "connection failed", err: err}
	}
	defer hresp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(hresp.Body, 32<<20))
	if err != nil {
		return nil, 0, &transientError{reason: "reading response failed", err: err}
	}
	retryAfter := parseRetryAfter(hresp.Header)
	if hresp.StatusCode/100 != 2 {
		err := fmt.Errorf("%s %s: %s: %s", c.Endpoint.Model, url, hresp.Status, errorDetail(data))
		if retryable(hresp.StatusCode) {
			return nil, retryAfter, &transientError{reason: hresp.Status, err: err}
		}
		return nil, 0, err
	}
	var envelope struct {
		Error *struct {
			Code    any    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &envelope) == nil && envelope.Error != nil && envelope.Error.Message != "" {
		err := fmt.Errorf("%s %s: error %v: %s", c.Endpoint.Model, url, envelope.Error.Code, errorDetail(data))
		if code, ok := envelope.Error.Code.(float64); ok && retryable(int(code)) {
			return nil, retryAfter, &transientError{reason: fmt.Sprintf("error %d", int(code)), err: err}
		}
		return nil, 0, err
	}
	return data, 0, nil
}

// retryable reports whether an HTTP status is worth another attempt.
func retryable(status int) bool {
	switch status {
	case http.StatusRequestTimeout, http.StatusTooManyRequests,
		http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

// parseRetryAfter reads a Retry-After header given as seconds or a date.
func parseRetryAfter(h http.Header) time.Duration {
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0
	}
	if secs, err := strconv.ParseFloat(v, 64); err == nil && secs > 0 {
		return time.Duration(secs * float64(time.Second))
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

// backoff picks the delay before the next attempt: full jitter over an
// exponentially growing ceiling, never less than the server's Retry-After.
func (c *Client) backoff(attempt int, retryAfter time.Duration) time.Duration {
	initial, maxDelay := c.Retry.Initial(), c.Retry.Max()
	if initial <= 0 {
		initial = time.Second
	}
	if maxDelay < initial {
		maxDelay = initial
	}
	ceiling := initial << (attempt - 1)
	if ceiling > maxDelay || ceiling <= 0 {
		ceiling = maxDelay
	}
	delay := max(retryAfter, time.Duration(rand.Int64N(int64(ceiling))+1))
	if cap := 2 * maxDelay; retryAfter > cap {
		delay = cap
	}
	return delay
}

func (c *Client) pause(ctx context.Context, d time.Duration) error {
	if c.sleep != nil {
		return c.sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// errorDetail turns an error response body into a readable message without
// losing anything: OpenRouter error envelopes are flattened, provider errors
// nested as JSON strings are unwrapped, and anything else is returned whole.
func errorDetail(data []byte) string {
	var env struct {
		Error *struct {
			Code     any            `json:"code"`
			Message  string         `json:"message"`
			Metadata map[string]any `json:"metadata"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &env) != nil || env.Error == nil || env.Error.Message == "" {
		return strings.TrimSpace(string(data))
	}
	msg := unwrap(env.Error.Message)
	var extra []string
	if p, ok := env.Error.Metadata["provider_name"].(string); ok && p != "" {
		extra = append(extra, "provider "+p)
	}
	if raw, ok := env.Error.Metadata["raw"].(string); ok && raw != "" && !strings.Contains(env.Error.Message, raw) {
		extra = append(extra, "raw: "+unwrap(raw))
	}
	if len(extra) > 0 {
		msg += " (" + strings.Join(extra, "; ") + ")"
	}
	return msg
}

// unwrap expands messages such as `HTTP 429: {"error":{"message":...}}` where
// a provider's JSON error is embedded as a string, keeping its code, type and
// request id.
func unwrap(msg string) string {
	i := strings.Index(msg, "{")
	if i < 0 {
		return msg
	}
	var inner struct {
		Error *struct {
			Message string `json:"message"`
			Code    any    `json:"code"`
			Type    string `json:"type"`
		} `json:"error"`
		RequestID string `json:"request_id"`
	}
	if json.Unmarshal([]byte(msg[i:]), &inner) != nil || inner.Error == nil || inner.Error.Message == "" {
		return msg
	}
	out := strings.TrimSpace(strings.TrimSpace(msg[:i]) + " " + unwrap(inner.Error.Message))
	var tags []string
	if c := fmt.Sprint(inner.Error.Code); inner.Error.Code != nil && c != "" {
		tags = append(tags, "code "+c)
	}
	if inner.Error.Type != "" {
		tags = append(tags, "type "+inner.Error.Type)
	}
	if inner.RequestID != "" {
		tags = append(tags, "request "+inner.RequestID)
	}
	if len(tags) > 0 {
		out += " [" + strings.Join(tags, ", ") + "]"
	}
	return out
}

// Embed implements models.Embedder via POST /v1/embeddings.
func (c *Client) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	req := map[string]any{"model": c.Endpoint.Model, "input": texts, "encoding_format": "float"}
	var resp struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := c.post(ctx, c.v1("/embeddings"), req, &resp); err != nil {
		return nil, err
	}
	if len(resp.Data) != len(texts) {
		return nil, fmt.Errorf("embeddings: got %d vectors for %d inputs", len(resp.Data), len(texts))
	}
	out := make([][]float32, len(texts))
	for i, d := range resp.Data {
		idx := d.Index
		if idx < 0 || idx >= len(out) {
			idx = i
		}
		out[idx] = d.Embedding
	}
	return out, nil
}

// Rerank implements models.Reranker via POST /v1/rerank.
func (c *Client) Rerank(ctx context.Context, query string, docs []string, topN int) ([]models.Ranked, error) {
	if len(docs) == 0 {
		return nil, nil
	}
	req := map[string]any{"model": c.Endpoint.Model, "query": query, "documents": docs, "return_documents": false}
	if topN > 0 {
		req["top_n"] = topN
	}
	var resp struct {
		Results []struct {
			Index          int     `json:"index"`
			RelevanceScore float64 `json:"relevance_score"`
		} `json:"results"`
	}
	if err := c.post(ctx, c.v1("/rerank"), req, &resp); err != nil {
		return nil, err
	}
	out := make([]models.Ranked, 0, len(resp.Results))
	for _, r := range resp.Results {
		out = append(out, models.Ranked{Index: r.Index, Score: r.RelevanceScore})
	}
	return out, nil
}

// Decide implements models.Decider via the OpenRouter Decisions API, which
// serves typed decision models such as typesafe/jev.
func (c *Client) Decide(ctx context.Context, p models.Pair) (models.Decision, error) {
	req := map[string]any{
		"model": c.Endpoint.Model,
		"state": map[string]any{
			"subject":         p.Subject,
			"newer_statement": p.Newer,
			"newer_clock":     p.NewerClock,
			"older_statement": p.Older,
			"older_clock":     p.OlderClock,
		},
		"questions": map[string]any{
			"relation": map[string]any{
				"type": "choice",
				"instructions": "Both statements are axioms about the same subject; the newer one was " +
					"recorded at a later clock than the older one. Classify how the newer statement " +
					"relates to the older statement.",
				"criteria": map[string]string{
					string(models.Contradicts): "The newer statement conflicts with, replaces, narrows, " +
						"broadens or otherwise changes what the older statement asserts, so a reader who " +
						"believed the older statement would now be wrong or out of date.",
					string(models.Restates): "The newer statement asserts the same fact as the older one, " +
						"possibly in different words or with trivial extra detail; believing either is equivalent.",
					string(models.Unrelated): "The statements are about different facts and can both be " +
						"true at the same time without either changing the meaning of the other.",
				},
			},
		},
	}
	var resp struct {
		Answers struct {
			Relation struct {
				Choice        string             `json:"choice"`
				Confidence    float64            `json:"confidence"`
				Probabilities map[string]float64 `json:"probabilities"`
			} `json:"relation"`
		} `json:"answers"`
	}
	if err := c.post(ctx, c.alpha("/decisions"), req, &resp); err != nil {
		return models.Decision{}, err
	}
	d := models.Decision{
		Relation:      models.Relation(resp.Answers.Relation.Choice),
		Confidence:    resp.Answers.Relation.Confidence,
		Probabilities: map[models.Relation]float64{},
	}
	for k, v := range resp.Answers.Relation.Probabilities {
		d.Probabilities[models.Relation(k)] = v
	}
	switch d.Relation {
	case models.Contradicts, models.Restates, models.Unrelated:
	default:
		return d, fmt.Errorf("decision model returned unknown relation %q", d.Relation)
	}
	return d, nil
}

// Chat sends a single system+user exchange via POST /v1/chat/completions.
func (c *Client) Chat(ctx context.Context, system, user string, maxTokens int) (string, error) {
	req := map[string]any{
		"model": c.Endpoint.Model,
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": user},
		},
	}
	if maxTokens > 0 {
		req["max_tokens"] = maxTokens
	}
	var resp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := c.post(ctx, c.v1("/chat/completions"), req, &resp); err != nil {
		return "", err
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("%s: chat completion returned no choices", c.Endpoint.Model)
	}
	return strings.TrimSpace(resp.Choices[0].Message.Content), nil
}

// Summarise implements models.Summariser.
func (c *Client) Summarise(ctx context.Context, markdown string, budget int) (string, error) {
	system := "You condense change logs of foundational facts (axioms) for consumption by another " +
		"language model. Preserve every distinct fact, the subject and clock it applies to, and which " +
		"older facts it supersedes. Drop prose, repetition and sources. Output Markdown only."
	user := fmt.Sprintf("Summarise the following in at most %d tokens.\n\n%s", budget, markdown)
	return c.Chat(ctx, system, user, budget)
}

// ModelInfo is the subset of GET /v1/models that ax uses.
type ModelInfo struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	CanonicalSlug string `json:"canonical_slug"`
	// Created is the Unix time the model was listed, which stands in for
	// its release date.
	Created int64 `json:"created"`
	// KnowledgeCutoff is YYYY-MM-DD, or empty when the provider has not
	// published one.
	KnowledgeCutoff string `json:"knowledge_cutoff"`
	// AliasTarget is set for rolling aliases such as ~vendor/model-latest.
	AliasTarget *struct {
		Slug string `json:"slug"`
		Name string `json:"name"`
	} `json:"alias_target,omitempty"`
}

// ListModels fetches the model catalogue. The endpoint works without an API
// key.
func (c *Client) ListModels(ctx context.Context) ([]ModelInfo, error) {
	var resp struct {
		Data []ModelInfo `json:"data"`
	}
	if err := c.get(ctx, c.v1("/models"), &resp); err != nil {
		return nil, err
	}
	if len(resp.Data) == 0 {
		return nil, fmt.Errorf("%s/models returned no models", c.v1(""))
	}
	return resp.Data, nil
}
