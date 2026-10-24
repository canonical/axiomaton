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

package openrouter

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/canonical/axiomaton/internal/config"
	"github.com/canonical/axiomaton/internal/models"
)

func server(t *testing.T) (*Client, *map[string]any) {
	t.Helper()
	var last map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer key" {
			http.Error(w, `{"error":{"code":401,"message":"nope"}}`, 401)
			return
		}
		json.NewDecoder(r.Body).Decode(&last)
		last["_path"] = r.URL.Path
		switch r.URL.Path {
		case "/api/v1/embeddings":
			json.NewEncoder(w).Encode(map[string]any{"data": []any{
				map[string]any{"index": 1, "embedding": []float32{0, 1}},
				map[string]any{"index": 0, "embedding": []float32{1, 0}},
			}})
		case "/api/v1/rerank":
			json.NewEncoder(w).Encode(map[string]any{"results": []any{
				map[string]any{"index": 1, "relevance_score": 0.9},
			}})
		case "/api/alpha/decisions":
			json.NewEncoder(w).Encode(map[string]any{"answers": map[string]any{"relation": map[string]any{
				"type": "choice", "choice": "contradicts", "confidence": 0.8,
				"probabilities": map[string]float64{"contradicts": 0.85, "restates": 0.1, "unrelated": 0.05},
			}}})
		case "/api/v1/chat/completions":
			json.NewEncoder(w).Encode(map[string]any{"choices": []any{
				map[string]any{"message": map[string]any{"content": " summary "}},
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return New(config.Endpoint{Model: "m", BaseURL: srv.URL + "/api", APIKey: "key"}), &last
}

func TestClient(t *testing.T) {
	c, last := server(t)
	ctx := context.Background()

	vecs, err := c.Embed(ctx, []string{"a", "b"})
	if err != nil || vecs[0][0] != 1 || vecs[1][1] != 1 {
		t.Fatalf("embed: %v %v", vecs, err)
	}
	ranked, err := c.Rerank(ctx, "q", []string{"x", "y"}, 1)
	if err != nil || len(ranked) != 1 || ranked[0].Index != 1 || (*last)["top_n"].(float64) != 1 {
		t.Fatalf("rerank: %v %v", ranked, err)
	}
	d, err := c.Decide(ctx, models.Pair{Subject: "s", Newer: "n", Older: "o"})
	if err != nil || d.Relation != models.Contradicts || d.Probabilities[models.Contradicts] != 0.85 {
		t.Fatalf("decide: %+v %v", d, err)
	}
	if (*last)["_path"] != "/api/alpha/decisions" || (*last)["model"] != "m" {
		t.Fatalf("decision request: %v", *last)
	}
	s, err := c.Summarise(ctx, "# md", 100)
	if err != nil || s != "summary" || (*last)["max_tokens"].(float64) != 100 {
		t.Fatalf("summarise: %q %v", s, err)
	}

	c.Endpoint.APIKey = "bad"
	if _, err := c.Embed(ctx, []string{"a"}); err == nil {
		t.Fatal("expected auth error")
	}
}

func TestV1Suffix(t *testing.T) {
	c := New(config.Endpoint{BaseURL: "http://h/v1"})
	if c.v1("/x") != "http://h/v1/x" || c.alpha("/d") != "http://h/alpha/d" {
		t.Fatalf("%s %s", c.v1("/x"), c.alpha("/d"))
	}
}

func retryServer(t *testing.T, script []func(w http.ResponseWriter)) (*Client, *[]time.Duration, *int) {
	t.Helper()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := calls
		calls++
		if i >= len(script) {
			i = len(script) - 1
		}
		script[i](w)
	}))
	t.Cleanup(srv.Close)
	var slept []time.Duration
	c := New(config.Endpoint{Model: "m", BaseURL: srv.URL, APIKey: "key"})
	c.Retry = config.Retry{MaxAttempts: 4, InitialBackoff: 1, MaxBackoff: 8}
	c.sleep = func(ctx context.Context, d time.Duration) error { slept = append(slept, d); return ctx.Err() }
	return c, &slept, &calls
}

func ok(w http.ResponseWriter) {
	json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": "fine"}}}})
}

func status(code int, retryAfter string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		if retryAfter != "" {
			w.Header().Set("Retry-After", retryAfter)
		}
		w.WriteHeader(code)
		fmt.Fprintf(w, `{"error":{"code":%d,"message":"HTTP %d: rate limited"}}`, code, code)
	}
}

func TestRetryRateLimit(t *testing.T) {
	ctx := context.Background()
	var logged []string
	c, slept, calls := retryServer(t, []func(http.ResponseWriter){status(429, "3"), status(503, ""), ok})
	c.Log = func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) }
	out, err := c.Chat(ctx, "s", "u", 0)
	if err != nil || out != "fine" || *calls != 3 {
		t.Fatalf("out=%q err=%v calls=%d", out, err, *calls)
	}
	if len(*slept) != 2 || (*slept)[0] < 3*time.Second || (*slept)[1] > 2*time.Second || (*slept)[1] <= 0 {
		t.Fatalf("delays: %v", *slept)
	}
	if len(logged) != 2 || !strings.Contains(logged[0], "429") || !strings.Contains(logged[0], "attempt 2 of 4") {
		t.Fatalf("log: %q", logged)
	}

	// An error envelope inside a 200 with a retryable code is retried too.
	c, _, calls = retryServer(t, []func(http.ResponseWriter){
		func(w http.ResponseWriter) { fmt.Fprint(w, `{"error":{"code":429,"message":"HTTP 429: slow down"}}`) }, ok,
	})
	if _, err := c.Chat(ctx, "s", "u", 0); err != nil || *calls != 2 {
		t.Fatalf("envelope retry: %v calls=%d", err, *calls)
	}

	// Client errors are not retried.
	c, slept, calls = retryServer(t, []func(http.ResponseWriter){status(400, ""), ok})
	if _, err := c.Chat(ctx, "s", "u", 0); err == nil || *calls != 1 || len(*slept) != 0 || strings.Contains(err.Error(), "gave up") {
		t.Fatalf("400: %v calls=%d slept=%v", err, *calls, *slept)
	}

	// Persistent throttling gives up after MaxAttempts with a clear message.
	c, slept, calls = retryServer(t, []func(http.ResponseWriter){status(429, "")})
	_, err = c.Chat(ctx, "s", "u", 0)
	if err == nil || *calls != 4 || len(*slept) != 3 || !strings.Contains(err.Error(), "gave up after 4 attempts") || !strings.Contains(err.Error(), "429") {
		t.Fatalf("give up: %v calls=%d slept=%v", err, *calls, *slept)
	}
	for i, d := range *slept {
		if ceiling := time.Second << i; d <= 0 || d > ceiling {
			t.Fatalf("delay %d = %v exceeds ceiling %v", i, d, ceiling)
		}
	}

	// Cancellation during the wait stops retrying.
	cctx, cancel := context.WithCancel(ctx)
	c, _, calls = retryServer(t, []func(http.ResponseWriter){status(429, "")})
	c.sleep = func(ctx context.Context, d time.Duration) error { cancel(); return context.Canceled }
	if _, err := c.Chat(cctx, "s", "u", 0); err == nil || *calls != 1 || !strings.Contains(err.Error(), "while waiting to retry") {
		t.Fatalf("cancel: %v calls=%d", err, *calls)
	}
}

func TestBackoffBounds(t *testing.T) {
	c := New(config.Endpoint{})
	c.Retry = config.Retry{MaxAttempts: 8, InitialBackoff: 1, MaxBackoff: 10}
	for attempt := 1; attempt <= 20; attempt++ {
		if d := c.backoff(attempt, 0); d <= 0 || d > 10*time.Second {
			t.Fatalf("attempt %d: %v", attempt, d)
		}
	}
	if d := c.backoff(1, 15*time.Second); d != 15*time.Second {
		t.Fatalf("retry-after should win: %v", d)
	}
	if d := c.backoff(1, time.Hour); d != 20*time.Second {
		t.Fatalf("absurd retry-after should be capped: %v", d)
	}
	h := http.Header{}
	h.Set("Retry-After", time.Now().Add(30*time.Second).UTC().Format(http.TimeFormat))
	if d := parseRetryAfter(h); d < 25*time.Second || d > 31*time.Second {
		t.Fatalf("http date: %v", d)
	}
}

func TestErrorDetail(t *testing.T) {
	// Verbatim shape of an OpenRouter 429 relayed from an upstream provider.
	body := `{"error":{"message":"HTTP 429: {\"error\":{\"message\":\"You have exceeded your rate limit for this API. Please try again later. For more information, see https://docs.fireworks.ai/guides/quotas_usage/rate-limits.\",\"param\":null,\"code\":\"RATE_LIMIT_EXCEEDED\",\"type\":\"error\"},\"request_id\":\"embd-b662ec60d41a4fa4a3aa0014ef34622b\"}","code":429}}`
	got := errorDetail([]byte(body))
	want := "HTTP 429: You have exceeded your rate limit for this API. Please try again later. For more information, see https://docs.fireworks.ai/guides/quotas_usage/rate-limits. [code RATE_LIMIT_EXCEEDED, type error, request embd-b662ec60d41a4fa4a3aa0014ef34622b]"
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	// Provider metadata is appended; plain messages pass through.
	got = errorDetail([]byte(`{"error":{"code":502,"message":"Provider returned error","metadata":{"provider_name":"Fireworks","raw":"upstream exploded"}}}`))
	if got != "Provider returned error (provider Fireworks; raw: upstream exploded)" {
		t.Fatalf("metadata: %s", got)
	}
	// Non JSON bodies are returned in full, however long.
	long := strings.Repeat("<html>", 500)
	if errorDetail([]byte(long+"\n")) != long {
		t.Fatal("long body was altered")
	}
	// The full detail reaches the caller.
	c, _, _ := retryServer(t, []func(http.ResponseWriter){func(w http.ResponseWriter) {
		w.WriteHeader(400)
		fmt.Fprint(w, body)
	}})
	_, err := c.Chat(context.Background(), "s", "u", 0)
	if err == nil || !strings.Contains(err.Error(), "400 Bad Request: "+want) {
		t.Fatalf("surfaced error: %v", err)
	}
}
