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

package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/canonical/axiomaton/internal/config"
	"github.com/canonical/axiomaton/internal/cutoff"
	"github.com/canonical/axiomaton/internal/models/openrouter"
)

func catalogue() []openrouter.ModelInfo {
	alias := func(slug string) *struct {
		Slug string `json:"slug"`
		Name string `json:"name"`
	} {
		return &struct {
			Slug string `json:"slug"`
			Name string `json:"name"`
		}{Slug: slug}
	}
	return []openrouter.ModelInfo{
		// Release-to-cutoff gaps: gpt-5-mini 433 days, claude 241 days.
		{ID: "openai/gpt-5-mini", Name: "OpenAI: GPT-5 Mini", CanonicalSlug: "openai/gpt-5-mini-2025-08-07", KnowledgeCutoff: "2024-05-31", Created: time.Date(2025, 8, 7, 0, 0, 0, 0, time.UTC).Unix()},
		{ID: "openai/gpt-5-mini:free", Name: "OpenAI: GPT-5 Mini (free)", CanonicalSlug: "openai/gpt-5-mini-2025-08-07"},
		{ID: "anthropic/claude-sonnet-4.5", Name: "Anthropic: Claude Sonnet 4.5", CanonicalSlug: "anthropic/claude-4.5-sonnet-20250929", KnowledgeCutoff: "2025-01-31", Created: time.Date(2025, 9, 29, 0, 0, 0, 0, time.UTC).Unix()},
		{ID: "~openai/gpt-latest", Name: "GPT latest", CanonicalSlug: "~openai/gpt-latest", AliasTarget: alias("openai/gpt-5-mini")},
		{ID: "mystery/model", Name: "Mystery", CanonicalSlug: "mystery/model", Created: time.Date(2025, 10, 1, 12, 0, 0, 0, time.UTC).Unix()},
		{ID: "mystery/undated", Name: "Undated", CanonicalSlug: "mystery/undated"},
	}
}

func TestFindCutoff(t *testing.T) {
	cat := catalogue()
	cases := map[string]string{
		"openai/gpt-5-mini":                    "2024-05-31",
		"openai/gpt-5-mini-2025-08-07":         "2024-05-31", // canonical slug
		"OpenAI: GPT-5 Mini":                   "2024-05-31", // display name
		"gpt-5-mini":                           "2024-05-31", // bare slug
		"openai/gpt-5-mini:free":               "2024-05-31", // variant falls back to base
		"~openai/gpt-latest":                   "2024-05-31", // alias
		"Anthropic/Claude-Sonnet-4.5":          "2025-01-31", // case
		"anthropic/claude-sonnet-4.5:thinking": "2025-01-31", // unknown variant of a known base
	}
	for name, want := range cases {
		_, got, err := findCutoff(cat, name)
		if err != nil || got.Format("2006-01-02") != want {
			t.Errorf("%q: got %v %v, want %s", name, got, err, want)
		}
	}
	var missing *noCutoffError
	if m, _, err := findCutoff(cat, "mystery/model"); !errors.As(err, &missing) || missing.Model != "mystery/model" || m.Created == 0 {
		t.Errorf("mystery: %+v %v", m, err)
	}
	if _, _, err := findCutoff(cat, "openai/gpt-7"); err == nil || !strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "did you mean") {
		t.Errorf("gpt-7: %v", err)
	}
	if _, _, err := findCutoff(cat, "openai/gpt-5-min"); err == nil || !strings.Contains(err.Error(), "did you mean openai/gpt-5-mini?") {
		t.Errorf("suggestion: %v", err)
	}
}

func TestForCommand(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "cfg"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("OPENROUTER_API_KEY", "")
	fetches := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/models" || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		fetches++
		json.NewEncoder(w).Encode(map[string]any{"data": catalogue()})
	}))
	defer srv.Close()
	cfgPath := filepath.Join(root, "cfg", "axiomaton", "ax.cnf")
	os.MkdirAll(filepath.Dir(cfgPath), 0o755)
	os.WriteFile(cfgPath, []byte("providers:\n  openrouter:\n    base_url: "+srv.URL+"/api\n"), 0o644)
	repo := filepath.Join(root, "repo")
	os.MkdirAll(filepath.Join(repo, ".git"), 0o755)
	os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module m\n\ngo 1.26.0\n"), 0o644)
	os.WriteFile(filepath.Join(repo, "axiomaton.cnf"), []byte("subjects:\n  - go\n"), 0o644)

	// No models are configured, so queries are deterministic without
	// --offline; the global --offline flag is used below to prove the
	// catalogue cache is honoured.
	exec := func(args ...string) (string, string, error) {
		var out, errb strings.Builder
		err := run(context.Background(), append([]string{"-C", repo}, args...), &out, &errb)
		return out.String(), errb.String(), err
	}

	// Cutoff 2024-05-31 with go 1.26.0: Go 1.25 facts are in, 1.24 and
	// earlier (released before the cutoff) are out, 1.27 is out of range.
	out, errOut, err := exec("for", "gpt-5-mini")
	if err != nil {
		t.Fatalf("for: %v\n%s", err, errOut)
	}
	if !strings.Contains(errOut, "ax: openai/gpt-5-mini (OpenAI: GPT-5 Mini) knowledge cutoff 2024-05-31") {
		t.Fatalf("stderr:\n%s", errOut)
	}
	if !strings.Contains(out, "# Axiom changes\n") || strings.Contains(out, "2024-05-31") || !strings.Contains(out, "## go (version <= 1.26.0)") || !strings.Contains(out, "1.25.0") || strings.Contains(out, "1.27.0") {
		t.Fatalf("out:\n%s", out)
	}
	if fetches != 1 {
		t.Fatalf("expected one catalogue fetch, got %d", fetches)
	}

	// Later calls use the cache, also under --offline; -refresh fetches again.
	if _, _, err := exec("for", "anthropic/claude-sonnet-4.5", "go", "1.25.0"); err != nil || fetches != 1 {
		t.Fatalf("cached: %v fetches=%d", err, fetches)
	}
	if _, _, err := exec("--offline", "for", "anthropic/claude-sonnet-4.5"); err != nil || fetches != 1 {
		t.Fatalf("offline cached: %v fetches=%d", err, fetches)
	}
	if _, _, err := exec("for", "-refresh", "anthropic/claude-sonnet-4.5"); err != nil || fetches != 2 {
		t.Fatalf("refresh: %v fetches=%d", err, fetches)
	}
	// No published cutoff and no agent configured: estimate from the
	// release date minus the 90th percentile gap in the catalogue (433 days
	// with two observations),
	// store it, and reuse the stored value next time.
	storePath := filepath.Join(root, "data", "axiomaton", "cutoffs.json")
	out, errOut, err = exec("for", "mystery/model")
	if err != nil || !strings.Contains(errOut, "not searching for the cutoff with an agent") || !strings.Contains(errOut, "estimated as 2024-07-25 (OpenRouter release date 2025-10-01 minus 433 days, the 90th percentile release-to-cutoff gap across 2 OpenRouter models, seen for openai/gpt-5-mini)") || !strings.Contains(out, "# Axiom changes\n") || strings.Contains(out, "2024-07-25") {
		t.Fatalf("estimate: %v\n%s\n%s", err, errOut, out)
	}
	if data, _ := os.ReadFile(storePath); !strings.Contains(string(data), `"method": "estimate"`) {
		t.Fatalf("store: %s", data)
	}
	_, errOut, err = exec("for", "mystery/model")
	if err != nil || !strings.Contains(errOut, "knowledge cutoff 2024-07-25 (estimated: OpenRouter release date 2025-10-01 minus 433 days") || strings.Contains(errOut, "not searching") {
		t.Fatalf("stored estimate: %v\n%s", err, errOut)
	}
	// Neither cutoff nor release date: the global epoch, stored like the rest.
	out, errOut, err = exec("for", "mystery/undated")
	if err != nil || !strings.Contains(errOut, "defaulted as 2023-08-01 (no release date on OpenRouter; global epoch)") || !strings.Contains(out, "# Axiom changes\n") || strings.Contains(out, "2023-08-01") {
		t.Fatalf("undated: %v\n%s\n%s", err, errOut, out)
	}
	if data, _ := os.ReadFile(storePath); !strings.Contains(string(data), `"method": "epoch"`) {
		t.Fatalf("store: %s", data)
	}
	_, errOut, err = exec("for", "mystery/undated")
	if err != nil || !strings.Contains(errOut, "knowledge cutoff 2023-08-01 (global epoch:") {
		t.Fatalf("stored epoch: %v\n%s", err, errOut)
	}

	// With an agent configured the search runs first; a hit is stored as
	// authoritative and wins over the estimate from then on.
	os.WriteFile(cfgPath, []byte("providers:\n  openrouter:\n    base_url: "+srv.URL+"/api\n    api_key: k\nmodels:\n  agent: {model: fake/agent}\n"), 0o644)
	oldResearch := research
	defer func() { research = oldResearch }()
	var asked []cutoff.Model
	research = func(_ context.Context, _ *app, _ config.User, m cutoff.Model) (cutoff.Record, bool, error) {
		asked = append(asked, m)
		if m.ID == "mystery/undated" {
			return cutoff.Record{}, false, nil
		}
		return cutoff.Record{Model: m.ID, Cutoff: "2025-03-31", Method: cutoff.MethodAgent, Source: "https://vendor.example/card"}, true, nil
	}
	_, errOut, err = exec("for", "-refresh", "mystery/model")
	if err != nil || !strings.Contains(errOut, "searching for the knowledge cutoff") || !strings.Contains(errOut, "knowledge cutoff 2025-03-31 (found by agent at https://vendor.example/card)") {
		t.Fatalf("agent: %v\n%s", err, errOut)
	}
	if len(asked) != 1 || asked[0].Released.Format("2006-01-02") != "2025-10-01" {
		t.Fatalf("asked: %+v", asked)
	}
	_, errOut, err = exec("for", "mystery/model")
	if err != nil || len(asked) != 1 || !strings.Contains(errOut, "2025-03-31 (found by agent") {
		t.Fatalf("stored agent result: %v %d\n%s", err, len(asked), errOut)
	}
	if _, errOut, err := exec("for", "-refresh", "mystery/undated"); err != nil || !strings.Contains(errOut, "agent could not find") || !strings.Contains(errOut, "defaulted as 2023-08-01") {
		t.Fatalf("agent miss without release date: %v\n%s", err, errOut)
	}
	if _, errOut, err := exec("--offline", "for", "-refresh", "mystery/model"); err != nil || len(asked) != 2 || !strings.Contains(errOut, "--offline: not searching") {
		t.Fatalf("offline skips agent: %v\n%s", err, errOut)
	}
	if _, _, err := exec("for"); err == nil || !strings.Contains(err.Error(), "model name is required") {
		t.Fatalf("no model: %v", err)
	}

	// Offline with no cache fails clearly.
	os.RemoveAll(filepath.Join(root, "cache"))
	if _, _, err := exec("--offline", "for", "gpt-5-mini"); err == nil || !strings.Contains(err.Error(), "no cached model catalogue") {
		t.Fatalf("offline without cache: %v", err)
	}
}
