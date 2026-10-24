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

package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/canonical/axiomaton/internal/axiom"
)

func TestLoadUser(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ax.cnf")
	os.WriteFile(path, []byte(`
providers:
  openrouter:
    api_key: sk-test
models:
  embedding: {model: openai/text-embedding-3-small}
  decision: {model: typesafe/jev-1.13}
  summary: {provider: local, model: llama, base_url: http://localhost:1234/v1, api_key: x}
summary_budget: 800
search:
  candidates: 3
`), 0o644)
	cfg, err := LoadUser(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SummaryBudget != 800 || cfg.Search.Candidates != 3 || cfg.Search.MinSimilarity != 0.35 {
		t.Fatalf("merge failed: %+v", cfg)
	}
	ep, err := cfg.Resolve(RoleEmbedding)
	if err != nil {
		t.Fatal(err)
	}
	if ep.BaseURL != "https://openrouter.ai/api" || ep.APIKey != "sk-test" || ep.Model != "openai/text-embedding-3-small" {
		t.Fatalf("bad endpoint %+v", ep)
	}
	if _, err := cfg.Resolve(RoleRerank); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("expected ErrNotConfigured, got %v", err)
	}
	ep, err = cfg.Resolve(RoleSummary)
	if err != nil || ep.BaseURL != "http://localhost:1234/v1" {
		t.Fatalf("summary endpoint %+v %v", ep, err)
	}
	if _, err := LoadUser(filepath.Join(dir, "missing")); err != nil {
		t.Fatalf("missing config should use defaults: %v", err)
	}
	os.WriteFile(path, []byte("models:\n  bogus: {model: x}\n"), 0o644)
	if _, err := LoadUser(path); err == nil {
		t.Fatal("unknown role should fail")
	}
}

func TestResolveEnv(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "")
	cfg := DefaultUser()
	cfg.Models[RoleDecision] = Model{Model: "typesafe/jev-1.13"}
	if _, err := cfg.Resolve(RoleDecision); err == nil {
		t.Fatal("expected missing key error")
	}
	t.Setenv("OPENROUTER_API_KEY", "sk-env")
	ep, err := cfg.Resolve(RoleDecision)
	if err != nil || ep.APIKey != "sk-env" {
		t.Fatalf("env key: %+v %v", ep, err)
	}
}

func TestLoadProject(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ProjectFile)
	os.WriteFile(path, []byte(`
subjects:
  - [golang, 1.26.1]
  - name: svc
    clock: [2.3.0]
    shape:
      - {name: version, kind: semver}
  - plain
shapes:
  juju:
    - {name: commit, kind: git, repo: ../juju}
`), 0o644)
	p, err := LoadProject(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Subjects) != 3 || p.Subjects[0].Name != "golang" || p.Subjects[0].Clock[0] != "1.26.1" {
		t.Fatalf("subjects: %+v", p.Subjects)
	}
	if p.Subjects[2].Name != "plain" || len(p.Subjects[2].Clock) != 0 {
		t.Fatalf("scalar subject: %+v", p.Subjects[2])
	}
	want := filepath.Join(dir, "../juju")
	if p.Shapes["juju"][0].Repo != want {
		t.Fatalf("repo = %q want %q", p.Shapes["juju"][0].Repo, want)
	}
	reg := axiom.NewRegistry()
	if err := p.Apply(reg); err != nil {
		t.Fatal(err)
	}
	if s, ok := reg.Lookup("svc"); !ok || s.Shape[0].Kind != axiom.KindSemver {
		t.Fatalf("svc not registered: %+v %v", s, ok)
	}
	if _, ok := reg.Lookup("juju"); !ok {
		t.Fatal("juju not registered")
	}
}
