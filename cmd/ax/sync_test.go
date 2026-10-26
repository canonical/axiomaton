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
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/canonical/axiomaton/internal/config"
)

func TestSpliceSync(t *testing.T) {
	block := "# Axiom changes\n\nbody\n"
	want := "# Axiom changes\n\nbody\n**end of axiom changes**\n"
	for _, tc := range []struct {
		name, doc, want string
		replaced        bool
	}{
		{"empty", "", want, false},
		{"append after text", "# Project\n\nnotes\n", "# Project\n\nnotes\n\n" + want, false},
		{"append keeps one blank line", "notes\n\n", "notes\n\n" + want, false},
		{"append to unterminated text", "notes", "notes\n\n" + want, false},
		{"replace between markers", "top\n\n# Axiom changes\n\nold\n**end of axiom changes**\n\nbottom\n", "top\n\n" + want + "\nbottom\n", true},
		{"replace to end without footer", "top\n# Axiom changes\nold\nmore old\n", "top\n" + want, true},
		{"markers with surrounding spaces", "  # Axiom changes  \nold\n **end of axiom changes** \ntail\n", want + "tail\n", true},
		{"only the first block", "# Axiom changes\nold\n**end of axiom changes**\n# Axiom changes\nkeep\n", want + "# Axiom changes\nkeep\n", true},
	} {
		got, replaced := spliceSync(tc.doc, block)
		if got != tc.want || replaced != tc.replaced {
			t.Errorf("%s: got (%q, %v), want (%q, %v)", tc.name, got, replaced, tc.want, tc.replaced)
		}
	}
	// Idempotent: splicing the result again changes nothing.
	once, _ := spliceSync("intro\n", block)
	twice, replaced := spliceSync(once, block)
	if twice != once || !replaced {
		t.Fatalf("not idempotent:\n%s\n---\n%s", once, twice)
	}
}

func TestAgentFile(t *testing.T) {
	if agentFile("claude") != "CLAUDE.local.md" || agentFile("Claude") != "CLAUDE.local.md" || agentFile("codex") != "AGENTS.override.md" || agentFile("notes/ctx.md") != "notes/ctx.md" {
		t.Fatal("agent mapping")
	}
}

func TestIgnoredFallback(t *testing.T) {
	// A .git directory that is not a usable repository exercises the
	// .gitignore scan.
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".git"), 0o755)
	ctx := context.Background()
	if ignored(ctx, root, "CLAUDE.local.md") {
		t.Fatal("nothing ignored yet")
	}
	os.WriteFile(filepath.Join(root, ".gitignore"), []byte("*.log\n/CLAUDE.local.md\n"), 0o644)
	if !ignored(ctx, root, "CLAUDE.local.md") || !ignored(ctx, root, "sub/CLAUDE.local.md") {
		t.Fatal("listed path should count as ignored")
	}
	if ignored(ctx, root, "GEMINI.md") {
		t.Fatal("unlisted path is not ignored")
	}
	if r, ok := repoRoot(filepath.Join(root, "a", "b")); !ok || r != root {
		t.Fatalf("repoRoot: %s %v", r, ok)
	}
	if _, ok := repoRoot(t.TempDir()); ok {
		t.Fatal("no repository above a fresh temp dir")
	}
}

func TestSyncCommand(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "cfg"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("OPENROUTER_API_KEY", "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/models" {
			http.NotFound(w, r)
			return
		}
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

	exec := func(in string, tty bool, args ...string) (string, string, error) {
		oldIn, oldTTY := stdin, interactive
		stdin, interactive = strings.NewReader(in), func() bool { return tty }
		defer func() { stdin, interactive = oldIn, oldTTY }()
		var out, errb strings.Builder
		err := run(context.Background(), append([]string{"-C", repo}, args...), &out, &errb)
		return out.String(), errb.String(), err
	}
	ctxFile := filepath.Join(repo, "CLAUDE.local.md")

	// Nothing known and nobody to ask.
	if _, _, err := exec("", false, "sync"); err == nil || !strings.Contains(err.Error(), "no cutoff for "+repo) {
		t.Fatalf("no cutoff: %v", err)
	}
	if _, _, err := exec("", false, "sync", "2024-05-31"); err == nil || !strings.Contains(err.Error(), "no context file for "+repo) {
		t.Fatalf("no file: %v", err)
	}
	if _, err := os.Stat(ctxFile); err == nil {
		t.Fatal("nothing should be written on failure")
	}

	// A model, an agent, -y: the file is created and remembered, with a
	// warning that it is not ignored; .gitignore is left alone.
	out, errOut, err := exec("", false, "sync", "-y", "-agent", "claude", "gpt-5-mini")
	if err != nil || out != "" {
		t.Fatalf("sync: %v\n%s\n%s", err, out, errOut)
	}
	for _, want := range []string{"knowledge cutoff 2024-05-31", "CLAUDE.local.md is inside the git repository at " + repo + " and not ignored; add /CLAUDE.local.md to " + filepath.Join(repo, ".gitignore"), "appended the axiom changes to CLAUDE.local.md (since the knowledge cutoff of gpt-5-mini, 2024-05-31)"} {
		if !strings.Contains(errOut, want) {
			t.Fatalf("stderr lacks %q:\n%s", want, errOut)
		}
	}
	data, _ := os.ReadFile(ctxFile)
	if !strings.HasPrefix(string(data), "# Axiom changes\n\n") || !strings.HasSuffix(string(data), "\n**end of axiom changes**\n") || !strings.Contains(string(data), "## go (version <= 1.26.0)") {
		t.Fatalf("context file:\n%s", data)
	}
	if _, err := os.Stat(filepath.Join(repo, ".gitignore")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf(".gitignore written: %v", err)
	}
	cfg, err := config.LoadUser(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if p := cfg.Sync.Projects[repo]; p.File != "CLAUDE.local.md" || p.Model != "gpt-5-mini" || p.Since != "" {
		t.Fatalf("remembered: %+v", cfg.Sync.Projects)
	}
	if ep, err := cfg.ProviderEndpoint("openrouter"); err != nil || ep.BaseURL != srv.URL+"/api" {
		t.Fatalf("provider lost when remembering: %+v %v", ep, err)
	}

	// The user's own text around the block survives a second run, which
	// needs no arguments; once ignored, the warning stops.
	os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("/CLAUDE.local.md\n"), 0o644)
	os.WriteFile(ctxFile, []byte("# My notes\n\nkeep me\n\n"+string(data)+"\nafter\n"), 0o644)
	_, errOut, err = exec("", false, "sync")
	if err != nil || !strings.Contains(errOut, "replaced the axiom changes in CLAUDE.local.md") || strings.Contains(errOut, "not ignored") {
		t.Fatalf("second sync: %v\n%s", err, errOut)
	}
	data, _ = os.ReadFile(ctxFile)
	if !strings.HasPrefix(string(data), "# My notes\n\nkeep me\n\n# Axiom changes\n\n") || !strings.HasSuffix(string(data), "**end of axiom changes**\n\nafter\n") || strings.Count(string(data), "# Axiom changes") != 1 {
		t.Fatalf("second context file:\n%s", data)
	}

	// A date given again replaces the stored model.
	_, errOut, err = exec("", false, "sync", "2025-06-01")
	if err != nil || !strings.Contains(errOut, "(since 2025-06-01)") {
		t.Fatalf("date sync: %v\n%s", err, errOut)
	}
	cfg, _ = config.LoadUser(cfgPath)
	if p := cfg.Sync.Projects[repo]; p.Since != "2025-06-01" || p.Model != "" || p.File != "CLAUDE.local.md" {
		t.Fatalf("remembered date: %+v", p)
	}

	// Interactive first run in another project: both answers are asked and
	// the agent name maps to its file; nothing is asked about .gitignore.
	other := filepath.Join(root, "other")
	os.MkdirAll(filepath.Join(other, ".git"), 0o755)
	os.WriteFile(filepath.Join(other, "go.mod"), []byte("module o\n\ngo 1.26.0\n"), 0o644)
	os.WriteFile(filepath.Join(other, "axiomaton.cnf"), []byte("subjects:\n  - go\n"), 0o644)
	repo, other = other, repo
	_, errOut, err = exec("2024-05-31\ncodex\n", true, "sync")
	repo, other = other, repo
	if err != nil || !strings.Contains(errOut, "Report changes since") || !strings.Contains(errOut, "Agent context file to update") || strings.Contains(errOut, "gitignore?") || !strings.Contains(errOut, "AGENTS.override.md is inside the git repository") || !strings.Contains(errOut, "appended the axiom changes to AGENTS.override.md") {
		t.Fatalf("interactive sync: %v\n%s", err, errOut)
	}
	if _, err := os.Stat(filepath.Join(other, "AGENTS.override.md")); err != nil {
		t.Fatal(err)
	}
	cfg, _ = config.LoadUser(cfgPath)
	if p := cfg.Sync.Projects[other]; p.File != "AGENTS.override.md" || p.Since != "2024-05-31" {
		t.Fatalf("remembered other: %+v", p)
	}

	// A file git already tracks is still written, with a warning.
	third := filepath.Join(root, "third")
	os.MkdirAll(third, 0o755)
	os.WriteFile(filepath.Join(third, "axiomaton.cnf"), []byte("subjects:\n  - [go, 1.26.0]\n"), 0o644)
	os.WriteFile(filepath.Join(third, "GEMINI.md"), []byte("mine\n"), 0o644)
	for _, args := range [][]string{{"init", "-q"}, {"add", "GEMINI.md"}} {
		if out, err := osexec.Command("git", append([]string{"-C", third}, args...)...).CombinedOutput(); err != nil {
			t.Skipf("git %v: %v\n%s", args, err, out)
		}
	}
	repo, third = third, repo
	_, errOut, err = exec("", true, "sync", "-agent", "gemini", "2024-05-31")
	repo, third = third, repo
	if err != nil || !strings.Contains(errOut, "GEMINI.md is tracked by git in "+third) || !strings.Contains(errOut, "appended the axiom changes to GEMINI.md") {
		t.Fatalf("tracked: %v\n%s", err, errOut)
	}
	if data, _ := os.ReadFile(filepath.Join(third, "GEMINI.md")); !strings.HasPrefix(string(data), "mine\n") || !strings.Contains(string(data), "# Axiom changes") {
		t.Fatalf("tracked file not written:\n%s", data)
	}

	// Defaults in ax.cnf serve a project never synced, with no prompts.
	fourth := filepath.Join(root, "fourth")
	os.MkdirAll(fourth, 0o755) // not a git repository: no ignore check
	os.WriteFile(filepath.Join(fourth, "axiomaton.cnf"), []byte("subjects:\n  - [go, 1.26.0]\n"), 0o644)
	cfg.Sync.File, cfg.Sync.Since = "notes/context.md", "2024-05-31"
	if err := config.WriteUser(cfgPath, cfg, time.Now()); err != nil {
		t.Fatal(err)
	}
	repo, fourth = fourth, repo
	_, errOut, err = exec("", true, "sync")
	repo, fourth = fourth, repo
	if err != nil || strings.Contains(errOut, "?") || !strings.Contains(errOut, "appended the axiom changes to "+filepath.Join("notes", "context.md")) {
		t.Fatalf("defaults: %v\n%s", err, errOut)
	}
	if _, err := os.Stat(filepath.Join(fourth, "notes", "context.md")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := exec("", false, "sync", "-agent", "nope"); err == nil || !strings.Contains(err.Error(), "unknown agent") {
		t.Fatalf("unknown agent: %v", err)
	}
	if _, _, err := exec("", false, "sync", "-agent", "claude", "-file", "x"); err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("exclusive flags: %v", err)
	}
}
