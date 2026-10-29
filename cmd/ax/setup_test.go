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
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/canonical/axiomaton/internal/config"
)

func fakeOpenRouter(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if r.Header.Get("Authorization") != "Bearer key" {
			http.Error(w, `{"error":{"code":401,"message":"bad key"}}`, 401)
			return
		}
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		switch r.URL.Path {
		case "/api/v1/embeddings":
			json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"index": 0, "embedding": []float32{1, 0}}}})
		case "/api/v1/rerank":
			json.NewEncoder(w).Encode(map[string]any{"results": []any{map[string]any{"index": 0, "relevance_score": 0.9}}})
		case "/api/alpha/decisions":
			json.NewEncoder(w).Encode(map[string]any{"answers": map[string]any{"relation": map[string]any{
				"choice": "contradicts", "confidence": 0.9, "probabilities": map[string]float64{"contradicts": 0.9},
			}}})
		case "/api/v1/chat/completions":
			if req["model"] == "broken/model" {
				http.Error(w, `{"error":{"code":404,"message":"no such model"}}`, 404)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": "OK"}}}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &paths
}

func TestSetup(t *testing.T) {
	srv, paths := fakeOpenRouter(t)
	path := filepath.Join(t.TempDir(), "nested", "ax.cnf")
	t.Setenv("OPENROUTER_API_KEY", "")

	exec := func(in string, tty bool, args ...string) (string, string, error) {
		oldIn, oldTTY := stdin, interactive
		stdin, interactive = strings.NewReader(in), func() bool { return tty }
		defer func() { stdin, interactive = oldIn, oldTTY }()
		var out, errb strings.Builder
		err := run(context.Background(), append([]string{"-c", path, "setup"}, args...), &out, &errb)
		return out.String(), errb.String(), err
	}

	// Non-interactive with flags: writes the file and probes every role.
	out, errOut, err := exec("", false, "-y", "-base-url", srv.URL+"/api", "-key", "key", "-rerank", "-")
	if err != nil {
		t.Fatalf("setup -y: %v\n%s\n%s", err, out, errOut)
	}
	if !strings.Contains(errOut, "wrote "+path) {
		t.Fatalf("stderr: %s", errOut)
	}
	for _, want := range []string{"embedding  ok", "rerank     skip", "decision   ok", "summary    ok", "agent      ok", "podman"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("stat: %v %v", info, err)
	}
	cfg, err := config.LoadUser(path)
	if err != nil {
		t.Fatal(err)
	}
	ep, err := cfg.Resolve(config.RoleDecision)
	if err != nil || ep.Model != "typesafe/jev-1.13" || ep.APIKey != "key" || ep.BaseURL != srv.URL+"/api" {
		t.Fatalf("decision endpoint %+v %v", ep, err)
	}
	seen := strings.Join(*paths, " ")
	if !strings.Contains(seen, "/api/v1/embeddings") || !strings.Contains(seen, "/api/alpha/decisions") || strings.Contains(seen, "/api/v1/rerank") {
		t.Fatalf("probed paths: %v", *paths)
	}

	// Existing file, nothing to change: rewritten as it was.
	if _, errOut, err := exec("", false, "-y", "-no-check"); err != nil || !strings.Contains(errOut, "rewrote "+path+" with no changes") {
		t.Fatalf("no-op update: %v\n%s", err, errOut)
	}
	// A value flag updates just that field, keeping the stored key.
	_, errOut, err = exec("", false, "-no-check", "-summary", "openai/other")
	if err != nil || !strings.Contains(errOut, "updated "+path) || !strings.Contains(errOut, "summary model: openai/gpt-oss-120b -> openai/other") || strings.Contains(errOut, "agent model:") {
		t.Fatalf("targeted update: %v\n%s", err, errOut)
	}
	cfg, _ = config.LoadUser(path)
	if cfg.Models[config.RoleSummary].Model != "openai/other" || cfg.Models[config.RoleDecision].Model != "typesafe/jev-1.13" {
		t.Fatalf("targeted update result: %+v", cfg.Models)
	}
	if ep, err := cfg.Resolve(config.RoleAgent); err != nil || ep.APIKey != "key" {
		t.Fatalf("targeted update lost the key: %+v %v", ep, err)
	}
	// Even on a terminal a value flag asks nothing.
	if _, errOut, err := exec("", true, "-no-check", "-rerank", "cohere/x"); err != nil || strings.Contains(errOut, "Reconfigure") || strings.Contains(errOut, "[") || !strings.Contains(errOut, "rerank model: none -> cohere/x") {
		t.Fatalf("interactive targeted update: %v\n%s", err, errOut)
	}
	if _, errOut, err := exec("", false, "-no-check", "-rerank", "-"); err != nil || !strings.Contains(errOut, "rerank model: cohere/x -> none") {
		t.Fatalf("targeted removal: %v\n%s", err, errOut)
	}

	// A failing model makes the command fail but the file is still written.
	_, _, err = exec("", false, "-y", "-force", "-base-url", srv.URL+"/api", "-key", "key", "-agent", "broken/model")
	if err == nil || !strings.Contains(err.Error(), "1 model check(s) failed") {
		t.Fatalf("expected a failed check, got %v", err)
	}
	cfg, _ = config.LoadUser(path)
	if cfg.Models[config.RoleAgent].Model != "broken/model" {
		t.Fatalf("config not rewritten: %+v", cfg.Models)
	}

	// Interactive: confirm overwrite, keep stored key, disable embedding,
	// accept the rest, pick an image.
	answers := strings.Join([]string{
		"y",                  // reconfigure
		"",                   // base URL
		"",                   // keep stored key
		"-",                  // embedding off
		"",                   // rerank default
		"",                   // decision
		"",                   // summary
		"anthropic/claude-x", // agent
		"docker.io/library/ubuntu:24.04",
	}, "\n") + "\n"
	_, errOut, err = exec(answers, true, "-no-check")
	if err != nil {
		t.Fatalf("interactive: %v\n%s", err, errOut)
	}
	if !strings.Contains(errOut, "Reconfigure it? [y/N]") || !strings.Contains(errOut, "blank to keep the stored key") || !strings.Contains(errOut, "not both configured") {
		t.Fatalf("prompts:\n%s", errOut)
	}
	cfg, err = config.LoadUser(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Models[config.RoleEmbedding].Configured() || cfg.Models[config.RoleRerank].Configured() || cfg.Models[config.RoleAgent].Model != "anthropic/claude-x" || cfg.Update.Image != "docker.io/library/ubuntu:24.04" {
		t.Fatalf("interactive result: %+v %+v", cfg.Models, cfg.Update)
	}
	if ep, err := cfg.Resolve(config.RoleAgent); err != nil || ep.APIKey != "key" {
		t.Fatalf("stored key lost: %+v %v", ep, err)
	}

	// Declining the overwrite leaves the file alone.
	before, _ := os.ReadFile(path)
	if _, errOut, err := exec("n\n", true); err != nil || !strings.Contains(errOut, "cancelled") {
		t.Fatalf("decline: %v %s", err, errOut)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("file changed after declining")
	}

	// Environment variable mode warns when the variable is unset.
	path = filepath.Join(t.TempDir(), "ax.cnf")
	_, errOut, err = exec("", false, "-y", "-no-check", "-key-env", "MY_KEY")
	if err != nil || !strings.Contains(errOut, "$MY_KEY is not set") {
		t.Fatalf("key-env: %v %s", err, errOut)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o644 {
		t.Fatalf("no inline key should give 0644, got %v", info.Mode().Perm())
	}
	if _, _, err := exec("", false, "-y", "-force", "-key", "a", "-key-env", "B"); err == nil {
		t.Fatal("-key with -key-env should fail")
	}
}
