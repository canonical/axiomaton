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

func TestMapHarnessModel(t *testing.T) {
	cat := catalogue()
	for ref, want := range map[string]string{
		"openrouter/openai/gpt-5-mini":                     "openai/gpt-5-mini",
		"openai/gpt-5-mini":                                "openai/gpt-5-mini",
		"openai/gpt-5-mini-2025-08-07":                     "openai/gpt-5-mini",
		"anthropic/claude-sonnet-4.5":                      "anthropic/claude-sonnet-4.5",
		"anthropic/claude-sonnet-4-5":                      "anthropic/claude-sonnet-4.5",
		"anthropic/claude-sonnet-4-5-20250929":             "anthropic/claude-sonnet-4.5",
		"google-vertex-anthropic/claude-sonnet-4-5@202509": "anthropic/claude-sonnet-4.5",
		"github-copilot/claude-sonnet-4.5":                 "anthropic/claude-sonnet-4.5",
		"opencode/gpt-5-mini":                              "openai/gpt-5-mini",
		"openai/claude-sonnet-4.5":                         "",
		"anthropic/claude-opus-9":                          "",
		"gpt-5-mini":                                       "",
	} {
		m, ok := mapHarnessModel(cat, ref)
		if got := map[bool]string{true: m.ID}[ok]; got != want {
			t.Errorf("%s: got %q, want %q", ref, got, want)
		}
	}
}

func TestSyncHarness(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "cfg"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("PI_CODING_AGENT_DIR", filepath.Join(root, "pi-agent"))
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
	os.WriteFile(cfgPath, []byte("providers.openrouter.base-url: \""+srv.URL+"/api\"\n"), 0o644)
	newProject := func(name string) string {
		dir := filepath.Join(root, name)
		os.MkdirAll(filepath.Join(dir, ".git"), 0o755)
		os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module m\n\ngo 1.26.0\n"), 0o644)
		os.WriteFile(filepath.Join(dir, "axiomaton.cnf"), []byte("subjects: [\"go\"]\n"), 0o644)
		return dir
	}
	exec := func(dir, in string, tty bool, args ...string) (string, string, error) {
		oldIn, oldTTY := stdin, interactive
		stdin, interactive = strings.NewReader(in), func() bool { return tty }
		defer func() { stdin, interactive = oldIn, oldTTY }()
		var out, errb strings.Builder
		err := run(context.Background(), append([]string{"-C", dir}, args...), &out, &errb)
		return out.String(), errb.String(), err
	}
	installedOK := func(path, marker string) {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if s := string(data); strings.Contains(s, "__AX__") || !strings.Contains(s, `process.env.AXIOMATON_AX || "`) || !strings.Contains(s, marker) {
			t.Fatalf("%s:\n%s", path, s)
		}
	}

	repo := newProject("repo")
	ext := filepath.Join(repo, ".pi", "extensions", "axiomaton.ts")

	// Installing needs a yes: without a terminal or -y nothing happens.
	if _, _, err := exec(repo, "", false, "sync", "-agent", "pi", "2024-05-31"); err == nil || !strings.Contains(err.Error(), "needs confirmation") {
		t.Fatalf("unconfirmed install: %v", err)
	}
	if _, err := os.Stat(ext); err == nil {
		t.Fatal("installed without confirmation")
	}
	if cfg, _ := config.LoadUser(cfgPath); len(cfg.Sync.Projects) != 0 {
		t.Fatalf("remembered without installing: %+v", cfg.Sync.Projects)
	}

	// Declining on a terminal: nothing installed or remembered.
	if _, errOut, err := exec(repo, "\nn\n", true, "sync", "-agent", "pi", "2024-05-31"); err != nil || !strings.Contains(errOut, "for this project or for every project") || !strings.Contains(errOut, "was not installed") {
		t.Fatalf("declined: %v\n%s", err, errOut)
	}
	if _, err := os.Stat(ext); err == nil {
		t.Fatal("installed after declining")
	}

	// -y installs into the project, warns that git would commit it, and
	// remembers the harness.
	_, errOut, err := exec(repo, "", false, "sync", "-y", "-agent", "pi", "2024-05-31")
	if err != nil {
		t.Fatalf("install: %v\n%s", err, errOut)
	}
	for _, want := range []string{"installed the pi extension at " + filepath.Join(".pi", "extensions", "axiomaton.ts"), "not ignored", "once the project is trusted", "pi will add"} {
		if !strings.Contains(errOut, want) {
			t.Fatalf("stderr lacks %q:\n%s", want, errOut)
		}
	}
	installedOK(ext, "before_agent_start")
	cfg, err := config.LoadUser(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if p := cfg.Sync.Projects[repo]; p.Harness != "pi" || p.File != "" || p.Global || p.Since != "2024-05-31" {
		t.Fatalf("remembered: %+v", p)
	}

	// Later runs need no arguments and no confirmation while it is current.
	if _, errOut, err := exec(repo, "", false, "sync"); err != nil || !strings.Contains(errOut, "is up to date") {
		t.Fatalf("up to date: %v\n%s", err, errOut)
	}
	// A changed file is replaced only after asking.
	os.WriteFile(ext, []byte("// old\n"), 0o644)
	if _, _, err := exec(repo, "", false, "sync"); err == nil || !strings.Contains(err.Error(), "needs confirmation") {
		t.Fatalf("unconfirmed replace: %v", err)
	}
	if _, errOut, err := exec(repo, "\n", true, "sync"); err != nil || !strings.Contains(errOut, "Replace the pi extension") || !strings.Contains(errOut, "updated the pi extension") {
		t.Fatalf("replace: %v\n%s", err, errOut)
	}
	installedOK(ext, "before_agent_start")

	// -print writes the report, from a subdirectory too, and changes
	// nothing.
	before, _ := os.ReadFile(cfgPath)
	sub := filepath.Join(repo, "internal", "pkg")
	os.MkdirAll(sub, 0o755)
	for _, dir := range []string{repo, sub} {
		out, errOut, err := exec(dir, "", false, "sync", "-print")
		if err != nil || !strings.HasPrefix(out, "# Axiom changes") || !strings.Contains(out, "## go (version <= 1.26.0)") {
			t.Fatalf("print in %s: %v\n%s\n%s", dir, err, out, errOut)
		}
	}
	// The harness's model is used when OpenRouter knows it, and the
	// remembered cutoff otherwise.
	if _, errOut, err := exec(repo, "", false, "-v", "sync", "-print", "-model", "anthropic/claude-sonnet-4-5-20250929"); err != nil || !strings.Contains(errOut, "using the knowledge cutoff of anthropic/claude-sonnet-4.5 (2025-01-31)") {
		t.Fatalf("print with model: %v\n%s", err, errOut)
	}
	if out, errOut, err := exec(repo, "", false, "-v", "sync", "-print", "-model", "local/llama-9"); err != nil || strings.Contains(errOut, "using the knowledge cutoff") || !strings.Contains(out, "# Axiom changes") {
		t.Fatalf("print with unknown model: %v\n%s", err, errOut)
	}
	if after, _ := os.ReadFile(cfgPath); string(after) != string(before) {
		t.Fatal("-print changed ax.cnf")
	}
	// Nothing known: nothing printed, and no error to show in the agent.
	stranger := newProject("stranger")
	if out, errOut, err := exec(stranger, "", false, "sync", "-print"); err != nil || out != "" || !strings.Contains(errOut, "no cutoff for "+stranger) {
		t.Fatalf("print without cutoff: %v %q\n%s", err, out, errOut)
	}

	// -global installs into the user's configuration and is remembered;
	// nothing is written to the project.
	other := newProject("other")
	plugin := filepath.Join(root, "cfg", "opencode", "plugins", "axiomaton.ts")
	if _, errOut, err := exec(other, "", false, "sync", "-y", "-global", "-agent", "opencode", "2024-05-31"); err != nil || !strings.Contains(errOut, "installed the opencode plugin") || strings.Contains(errOut, "not ignored") {
		t.Fatalf("global install: %v\n%s", err, errOut)
	}
	installedOK(plugin, "experimental.chat.system.transform")
	if _, err := os.Stat(filepath.Join(other, ".opencode")); err == nil {
		t.Fatal("global install wrote into the project")
	}
	cfg, _ = config.LoadUser(cfgPath)
	if p := cfg.Sync.Projects[other]; p.Harness != "opencode" || !p.Global {
		t.Fatalf("remembered global: %+v", p)
	}
	// Another project is served by the global copy without asking where.
	third := newProject("third")
	if _, errOut, err := exec(third, "", true, "sync", "-agent", "opencode", "2024-05-31"); err != nil || !strings.Contains(errOut, "already serves this project") || strings.Contains(errOut, "for this project or") {
		t.Fatalf("covered by global: %v\n%s", err, errOut)
	}
	if _, err := os.Stat(filepath.Join(third, ".opencode")); err == nil {
		t.Fatal("project copy beside the global one")
	}

	// Answering global at the prompt.
	fourth := newProject("fourth")
	if _, errOut, err := exec(fourth, "global\ny\n", true, "sync", "-agent", "pi", "2024-05-31"); err != nil || !strings.Contains(errOut, "Install the pi extension") {
		t.Fatalf("prompted global: %v\n%s", err, errOut)
	}
	installedOK(filepath.Join(root, "pi-agent", "extensions", "axiomaton.ts"), "before_agent_start")

	// Switching a project back to a context file forgets the harness.
	if _, _, err := exec(repo, "", false, "sync", "-agent", "claude"); err != nil {
		t.Fatal(err)
	}
	cfg, _ = config.LoadUser(cfgPath)
	if p := cfg.Sync.Projects[repo]; p.Harness != "" || p.File != "CLAUDE.local.md" {
		t.Fatalf("switched back: %+v", p)
	}

	for _, args := range [][]string{
		{"sync", "-agent", "claude", "-global"},
		{"sync", "-model", "openai/gpt-5-mini"},
		{"sync", "-print", "2024-05-31"},
		{"sync", "-global", "-project", "-agent", "pi"},
	} {
		if _, _, err := exec(repo, "", false, args...); err == nil {
			t.Errorf("%v: expected an error", args)
		}
	}
}

func TestSetupHarness(t *testing.T) {
	srv, _ := fakeOpenRouter(t)
	root := t.TempDir()
	path := filepath.Join(root, "ax.cnf")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "cfg"))
	t.Setenv("PI_CODING_AGENT_DIR", filepath.Join(root, "pi-agent"))
	t.Setenv("OPENROUTER_API_KEY", "")
	exec := func(in string, tty bool, args ...string) (string, string, error) {
		oldIn, oldTTY := stdin, interactive
		stdin, interactive = strings.NewReader(in), func() bool { return tty }
		defer func() { stdin, interactive = oldIn, oldTTY }()
		var out, errb strings.Builder
		err := run(context.Background(), append([]string{"-c", path, "setup", "-no-check"}, args...), &out, &errb)
		return out.String(), errb.String(), err
	}
	if _, errOut, err := exec("", false, "-y", "-base-url", srv.URL+"/api", "-key", "key"); err != nil {
		t.Fatalf("setup: %v\n%s", err, errOut)
	}

	// A file agent sets sync.file; there is nothing global to install.
	if _, errOut, err := exec("", false, "-harness", "claude", "-sync-cutoff", "openai/gpt-5-mini"); err != nil || !strings.Contains(errOut, "sync default: none -> CLAUDE.local.md") || !strings.Contains(errOut, "sync cutoff: none -> openai/gpt-5-mini") {
		t.Fatalf("claude: %v\n%s", err, errOut)
	}
	if _, _, err := exec("", false, "-harness", "claude", "-global-extension"); err == nil {
		t.Fatal("-global-extension needs a harness with an extension")
	}

	// A harness with -global-extension installs it without asking.
	plugin := filepath.Join(root, "cfg", "opencode", "plugins", "axiomaton.ts")
	if _, errOut, err := exec("", false, "-harness", "opencode", "-global-extension", "-sync-cutoff", "2024-05-31"); err != nil || !strings.Contains(errOut, "installed the opencode plugin") {
		t.Fatalf("opencode: %v\n%s", err, errOut)
	}
	if _, err := os.Stat(plugin); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadUser(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Sync.Harness != "opencode" || cfg.Sync.File != "" || cfg.Sync.Since != "2024-05-31" || cfg.Sync.Model != "" {
		t.Fatalf("sync defaults: %+v", cfg.Sync)
	}

	// Interactively: the harness, the global extension, the default.
	answers := strings.Join([]string{
		"",   // base URL
		"",   // keep stored key
		"",   // embedding
		"",   // rerank
		"",   // decision
		"",   // summary
		"",   // agent
		"",   // image
		"pi", // harness
		"y",  // global extension
		"",   // keep the default cutoff
	}, "\n") + "\n"
	_, errOut, err := exec(answers, true, "-force")
	if err != nil || !strings.Contains(errOut, "Agent harness to keep current") || !strings.Contains(errOut, "so every pi session gets") || !strings.Contains(errOut, "Default for ax sync in new projects") {
		t.Fatalf("interactive: %v\n%s", err, errOut)
	}
	if _, err := os.Stat(filepath.Join(root, "pi-agent", "extensions", "axiomaton.ts")); err != nil {
		t.Fatal(err)
	}
	cfg, _ = config.LoadUser(path)
	if cfg.Sync.Harness != "pi" || cfg.Sync.Since != "2024-05-31" {
		t.Fatalf("interactive sync defaults: %+v", cfg.Sync)
	}

	// -y alone never installs anything.
	os.Remove(plugin)
	if _, _, err := exec("", false, "-y", "-harness", "opencode"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(plugin); err == nil {
		t.Fatal("-y installed the plugin")
	}
}
