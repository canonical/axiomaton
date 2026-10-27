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
	"strings"
	"testing"
	"time"

	"github.com/canonical/axiomaton/internal/axiom"
)

func TestLoadUser(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ax.cnf")
	os.WriteFile(path, []byte(`
providers:
    openrouter:
        api-key: "sk-test"
models:
    embedding: {model: "openai/text-embedding-3-small"}
    decision: {model: "typesafe/jev-1.13"}
    summary: {provider: "local", model: "llama", base-url: "http://localhost:1234/v1", api-key: "x"}
summary-budget: 800
search.candidates: 3
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
	os.WriteFile(path, []byte("models:\n    bogus: {model: \"x\"}\n"), 0o644)
	if _, err := LoadUser(path); err == nil {
		t.Fatal("unknown role should fail")
	}
	os.WriteFile(path, []byte("consent.docker: true\n"), 0o644)
	if _, err := LoadUser(path); err == nil || !strings.Contains(err.Error(), "unknown consent key") {
		t.Fatalf("unknown consent key should fail: %v", err)
	}
	os.WriteFile(path, []byte("consent: {podman: true, lxd: false}\n"), 0o644)
	if cfg, err := LoadUser(path); err != nil || !cfg.Consent[ConsentPodman] || cfg.Consent[ConsentLXD] {
		t.Fatalf("consent: %+v %v", cfg.Consent, err)
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
    - ["go", "1.26.1"]
    - {name: "svc", clock: ["2.3.0"], shape: [{name: "version", kind: "semver"}]}
    - "plain"
shapes:
    juju:
        - {name: "commit", kind: "git", repo: "../juju"}
`), 0o644)
	p, err := LoadProject(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Subjects) != 3 || p.Subjects[0].Name != "go" || p.Subjects[0].Clock[0] != "1.26.1" {
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

func TestLoadProjectUpdate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ProjectFile)
	os.WriteFile(path, []byte(`
subjects:
    ---
    name: "juju"
    clock: ["3.6.4"]
    shape: [{name: "version", kind: "semver"}]
    update:
        about: "Juju, the orchestration engine."
        sources:
            - "https://github.com/juju/juju/releases"
            - "../juju"
            - "/abs/checkout"
        setup: "snap install juju --classic"
        clock: "the release without v"
        verify: "bootstrap localhost"
        vm: true
    ---
    name: "plain"
    shape: [{name: "version", kind: "semver"}]
    ---
`), 0o644)
	p, err := LoadProject(path)
	if err != nil {
		t.Fatal(err)
	}
	u := p.Subjects[0].Update
	if u == nil || u.About != "Juju, the orchestration engine." || u.Setup != "snap install juju --classic" || u.Clock != "the release without v" || u.Verify != "bootstrap localhost" || !u.VM {
		t.Fatalf("update: %+v", u)
	}
	if len(u.Sources) != 3 || u.Sources[0] != "https://github.com/juju/juju/releases" || u.Sources[1] != filepath.Join(dir, "../juju") || u.Sources[2] != "/abs/checkout" {
		t.Fatalf("sources: %v", u.Sources)
	}
	if p.Subjects[1].Update != nil {
		t.Fatal("plain has no brief")
	}
	for _, tc := range []struct{ cnf, want string }{
		{"subjects:\n    - {name: \"x\", update: {sources: [\"https://a\"]}}\n", "update.about is required"},
		{"subjects:\n    - {name: \"x\", update: {about: \"a\"}}\n", "update.sources is required"},
		{"subjects:\n    - {name: \"x\", update: {about: \"a\", sources: [\"git@github.com:a/b.git\"]}}\n", "uses SSH"},
		{"subjects:\n    - {name: \"x\", update: {about: \"a\", sources: [\"ssh://git@github.com/a/b\"]}}\n", "uses SSH"},
		{"subjects:\n    - {name: \"x\", update: {about: \"a\", sources: [\"http://a\"]}}\n", "plain http"},
		{"subjects:\n    - {name: \"x\", update: {about: \"a\", sources: [\"\"]}}\n", "empty entry"},
	} {
		os.WriteFile(path, []byte(tc.cnf), 0o644)
		if _, err := LoadProject(path); err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "subject x") {
			t.Errorf("%q: error %v, want %q", tc.cnf, err, tc.want)
		}
	}
	// Malformed subjects are refused with a reason.
	for _, bad := range []string{"subjects: [1]\n", "subjects: [[]]\n", "subjects: [{clock: [\"1\"]}]\n", "subjects: [true]\n"} {
		os.WriteFile(path, []byte(bad), 0o644)
		if _, err := LoadProject(path); err == nil {
			t.Errorf("%q should not load", bad)
		}
	}
	if IsLocalSource("https://x") || !IsLocalSource("../x") || !IsLocalSource("/x") {
		t.Fatal("IsLocalSource")
	}
}

func TestRenderRoundTrip(t *testing.T) {
	u := DefaultUser()
	p := u.Providers["openrouter"]
	p.APIKey = "sk-or-v1:odd/chars, here"
	u.Providers["openrouter"] = p
	u.Providers["local"] = Provider{BaseURL: "http://localhost:1234/v1", APIKey: "x"}
	u.Models[RoleEmbedding] = Model{Model: "openai/text-embedding-3-small"}
	u.Models[RoleSummary] = Model{Model: "7", Provider: "local"}
	u.Search.MinSimilarity = 0.4
	u.Update.Image = "docker.io/library/ubuntu:24.04"
	u.Sync.File = "CLAUDE.local.md"
	u.Sync.Model = "openai/gpt-6-luna-pro"
	u.Consent[ConsentLXDNetwork] = true
	u.Sync.Projects = map[string]SyncProject{
		"/home/me/proj: odd": {File: "AGENTS.override.md", Since: "2025-06-01"},
		"/home/me/other":     {Model: "gpt-5-mini"},
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "ax.cnf")
	if err := WriteUser(path, u, time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("inline key should give 0600, got %v", info.Mode().Perm())
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "# rerank: not configured") || !strings.Contains(string(data), "written by ax setup on 2026-10-24") {
		t.Fatalf("render:\n%s", data)
	}
	back, err := LoadUser(path)
	if err != nil {
		t.Fatalf("%v\n%s", err, data)
	}
	ep, err := back.Resolve(RoleEmbedding)
	if err != nil || ep.APIKey != p.APIKey || ep.BaseURL != "https://openrouter.ai/api" {
		t.Fatalf("embedding %+v %v", ep, err)
	}
	ep, err = back.Resolve(RoleSummary)
	if err != nil || ep.Model != "7" || ep.BaseURL != "http://localhost:1234/v1" || ep.APIKey != "x" {
		t.Fatalf("summary %+v %v", ep, err)
	}
	if back.Search.MinSimilarity != 0.4 || back.Search.Candidates != 8 || back.Update.Image != u.Update.Image || back.Update.ExecTimeout != 300 || back.Update.Confirmations != DefaultConfirmations() || DefaultConfirmations() < 1 || back.Proxy.Disabled || back.Proxy.MaxObjectMB != 1024 || back.Update.Backend != "auto" || back.Update.LXDImage != "ubuntu:26.04" {
		t.Fatalf("tunables: %+v", back)
	}
	if _, err := back.Resolve(RoleRerank); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("rerank: %v", err)
	}
	if back.Sync.File != "CLAUDE.local.md" || back.Sync.Model != "openai/gpt-6-luna-pro" || back.Sync.Since != "" || len(back.Sync.Projects) != 2 ||
		back.Sync.Projects["/home/me/proj: odd"] != (SyncProject{File: "AGENTS.override.md", Since: "2025-06-01"}) || back.Sync.Projects["/home/me/other"] != (SyncProject{Model: "gpt-5-mini"}) {
		t.Fatalf("sync: %+v", back.Sync)
	}
	if !strings.Contains(string(data), "consent:\n") || !strings.Contains(string(data), "lxd-network: true") || !strings.Contains(string(data), "podman: false") {
		t.Fatalf("consent render:\n%s", data)
	}
	if !back.Consent[ConsentLXDNetwork] || back.Consent[ConsentPodman] || back.Consent[ConsentLXD] {
		t.Fatalf("consent: %+v", back.Consent)
	}
	if v, isDate, ok := back.Sync.Projects["/home/me/proj: odd"].Cutoff(); v != "2025-06-01" || !isDate || !ok {
		t.Fatal("Cutoff date")
	}
	if v, isDate, ok := (SyncProject{Model: "m"}).Cutoff(); v != "m" || isDate || !ok {
		t.Fatal("Cutoff model")
	}
	if _, _, ok := (SyncProject{}).Cutoff(); ok {
		t.Fatal("Cutoff empty")
	}
	for _, bad := range []string{"sync: {model: \"m\", since: \"2025-01-01\"}\n", "sync.since: \"yesterday\"\n", "sync.projects: {\"/p\": {model: \"m\", since: \"2025-01-01\"}}\n"} {
		os.WriteFile(path, []byte(bad), 0o644)
		if _, err := LoadUser(path); err == nil {
			t.Fatalf("%q should not load", bad)
		}
	}

	// Without an inline key the file is world readable, and with no model
	// configured it still parses.
	if err := WriteUser(path, DefaultUser(), time.Now()); err != nil {
		t.Fatal(err)
	}
	info, _ = os.Stat(path)
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("expected 0644, got %v", info.Mode().Perm())
	}
	if back, err := LoadUser(path); err != nil || len(back.Models) != 0 || back.Search.Candidates != 8 {
		t.Fatalf("defaults round trip: %v %+v", err, back)
	}
}
