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
	"net/http"
	"net/http/httptest"
	"testing"

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
