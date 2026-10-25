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
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/canonical/axiomaton/internal/axiom"
	"github.com/canonical/axiomaton/internal/axiomlog"
	"github.com/canonical/axiomaton/internal/subjects"
)

func TestParseInterleaved(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	s := fs.Bool("s", false, "")
	src := fs.String("source", "", "")
	pos, err := parseInterleaved(fs, []string{"2026-01-01", "go", "-s", "1.26", "-source", "u"})
	if err != nil || !*s || *src != "u" || strings.Join(pos, ",") != "2026-01-01,go,1.26" {
		t.Fatalf("pos=%v s=%v src=%q err=%v", pos, *s, *src, err)
	}
	fs = flag.NewFlagSet("t", flag.ContinueOnError)
	s = fs.Bool("s", false, "")
	pos, err = parseInterleaved(fs, []string{"a", "--", "-s", "b"})
	if err != nil || *s || strings.Join(pos, ",") != "a,-s,b" {
		t.Fatalf("terminator: pos=%v s=%v err=%v", pos, *s, err)
	}
}

// mustSeed returns the embedded go log.
func mustSeed(t *testing.T) []axiom.Axiom {
	t.Helper()
	b, _ := subjects.Lookup("go")
	seed, err := b.Seed()
	if err != nil {
		t.Fatal(err)
	}
	return seed
}

func TestCLI(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "cfg"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	repo := filepath.Join(root, "repo")
	os.MkdirAll(filepath.Join(repo, ".git"), 0o755)
	os.MkdirAll(filepath.Join(repo, "sub"), 0o755)
	os.WriteFile(filepath.Join(repo, "axiomaton.cnf"), []byte("subjects:\n  - [go, 1.26.1]\n  - name: svc\n    clock: [2.3.0]\n    shape: [{name: version, kind: semver}]\n"), 0o644)

	exec := func(args ...string) (string, string, error) {
		var out, errb strings.Builder
		err := run(context.Background(), append([]string{"-C", filepath.Join(repo, "sub"), "--offline"}, args...), &out, &errb)
		return out.String(), errb.String(), err
	}

	out, _, err := exec("since", "2025-08-01", "go", "1.26.1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "## go (version <= 1.26.1)") || !strings.Contains(out, "- **[1.26.0]** ") || strings.Contains(out, "1.27.0") {
		t.Fatalf("since output:\n%s", out)
	}
	if strings.Contains(out, "go.dev/doc") || strings.Contains(out, "2026-") || strings.Contains(out, "2025-") {
		t.Fatalf("sources and dates must not be rendered:\n%s", out)
	}
	// Log order, not hash order, within a release: the 1.25.0 items appear
	// in the order the seed log lists them.
	var seedOrder []string
	for _, a := range axiomlog.Filter(mustSeed(t), "go") {
		if a.Clock["version"] == "1.25.0" {
			seedOrder = append(seedOrder, a.Statement)
		}
	}
	if len(seedOrder) < 2 {
		t.Fatalf("seed has %d axioms for 1.25.0", len(seedOrder))
	}
	for i := 1; i < len(seedOrder); i++ {
		if strings.Index(out, seedOrder[i-1]) > strings.Index(out, seedOrder[i]) {
			t.Fatalf("expected log order within 1.25.0:\n%s", out)
		}
	}

	// svc has no axioms yet, so it is omitted from the default report.
	out, errOut, err := exec("-v", "since", "2026-01-01")
	if err != nil || !strings.Contains(out, "## go") || strings.Contains(out, "## svc") || !strings.Contains(errOut, `nothing to report for subject "svc"; omitted`) {
		t.Fatalf("default subjects: %v\n%s\n%s", err, out, errOut)
	}
	out, _, err = exec("since", "2026-01-01", "svc", "2.3.0")
	if err != nil || out != "# Axiom changes\n\nNo changes for svc (version <= 2.3.0).\n" {
		t.Fatalf("quiet subject alone: %v\n%s", err, out)
	}

	_, errOut, err = exec("add", "2026-09-01", "svc", "2.4.0", "svc exposes a v2 API", "-source", "https://example.com")
	if err != nil || !strings.Contains(errOut, "added") {
		t.Fatalf("add: %v %s", err, errOut)
	}
	out, _, err = exec("since", "2026-01-01")
	if err != nil || !strings.Contains(out, "## go") || strings.Contains(out, "## svc") {
		t.Fatalf("default svc @ 2.3.0 still has nothing to report: %v\n%s", err, out)
	}
	data, _ := os.ReadFile(filepath.Join(repo, "axiomlog"))
	if !strings.Contains(string(data), `"source":"https://example.com"`) {
		t.Fatalf("log: %s", data)
	}
	_, errOut, err = exec("add", "2026-09-01", "svc", "2.4.0", "svc exposes a v2 API")
	if err != nil || !strings.Contains(errOut, "already recorded") {
		t.Fatalf("duplicate add: %v %s", err, errOut)
	}
	if _, _, err = exec("add", "2026-09-01", "rust", "1.80", "x"); err == nil || !strings.Contains(err.Error(), "no declared clock shape") {
		t.Fatalf("undeclared subject add should fail: %v", err)
	}
	if _, _, err = exec("add", "2026-09-01", "svc", "banana", "x"); err == nil {
		t.Fatal("bad clock should fail")
	}

	out, _, err = exec("since", "2026-01-01", "svc", "2.4.0")
	if err != nil || !strings.Contains(out, "svc exposes a v2 API") {
		t.Fatalf("since svc: %v\n%s", err, out)
	}
	out, _, _ = exec("since", "2026-01-01", "svc", "2.3.0")
	if strings.Contains(out, "svc exposes a v2 API") {
		t.Fatalf("2.4.0 axiom must not apply to 2.3.0:\n%s", out)
	}

	out, errOut, err = exec("subjects")
	if err != nil || !strings.Contains(out, "go\tshape [version:goversion]") || !strings.Contains(out, "svc\tshape [version:semver]\t1 axiom(s)\tdefault @ 2.3.0") {
		t.Fatalf("subjects: %v\n%s\n%s", err, out, errOut)
	}

	_, errOut, err = exec("since", "2026-01-01", "go", "-s")
	if err == nil || !strings.Contains(err.Error(), "summary") {
		t.Fatalf("-s without a summary model should fail clearly: %v", err)
	}

	var out2, err2 strings.Builder
	if err := run(context.Background(), []string{"-C", root, "--offline", "since", "2026-01-01"}, &out2, &err2); err != nil || out2.Len() != 0 || !strings.Contains(err2.String(), "no default subjects") {
		t.Fatalf("no defaults: %v out=%q err=%q", err, out2.String(), err2.String())
	}
	if err := run(context.Background(), []string{"bogus"}, &out2, &err2); err == nil {
		t.Fatal("unknown command should fail")
	}
	if _, _, err := exec("update", "rust"); err == nil || !strings.Contains(err.Error(), "built-in") {
		t.Fatalf("update rust: %v", err)
	}
}

func TestInit(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "cfg"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	repo := filepath.Join(root, "repo")
	os.MkdirAll(filepath.Join(repo, ".git"), 0o755)
	os.MkdirAll(filepath.Join(repo, "pkg"), 0o755)
	os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module example.com/m\n\ngo 1.26.1\n\nrequire (\n\tgolang.org/x/net v0.50.0\n\tgithub.com/google/uuid v1.6.0 // indirect\n)\n"), 0o644)

	exec := func(dir string, args ...string) (string, string, error) {
		var out, errb strings.Builder
		err := run(context.Background(), append([]string{"-C", dir, "--offline"}, args...), &out, &errb)
		return out.String(), errb.String(), err
	}

	// Explicit subject without a clock reads go.mod even before init.
	out, _, err := exec(filepath.Join(repo, "pkg"), "since", "2025-08-01", "go")
	if err != nil || !strings.Contains(out, "## go (version <= 1.26.1)") || strings.Contains(out, "1.27.0") {
		t.Fatalf("detected clock: %v\n%s", err, out)
	}

	_, errOut, err := exec(filepath.Join(repo, "pkg"), "init")
	if err != nil {
		t.Fatalf("init: %v\n%s", err, errOut)
	}
	if !strings.Contains(errOut, "go @ 1.26.1 (from ../go.mod)") || !strings.Contains(errOut, "go:golang.org/x/net @ v0.50.0 (from ../go.mod)") || !strings.Contains(errOut, "created "+filepath.Join(repo, "pkg", "axiomlog")) || !strings.Contains(errOut, "run `ax setup`") {
		t.Fatalf("init output:\n%s", errOut)
	}
	if strings.Contains(errOut, "uuid") {
		t.Fatalf("indirect dependency listed:\n%s", errOut)
	}
	cnf, _ := os.ReadFile(filepath.Join(repo, "pkg", "axiomaton.cnf"))
	if !strings.Contains(string(cnf), "  - go  # detected: ../go.mod says 1.26.1") || !strings.Contains(string(cnf), "  - go:golang.org/x/net  # detected: ../go.mod says v0.50.0") {
		t.Fatalf("cnf:\n%s", cnf)
	}
	if _, _, err := exec(filepath.Join(repo, "pkg"), "init"); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("second init: %v", err)
	}
	if _, _, err := exec(filepath.Join(repo, "pkg"), "init", "-force", "rust"); err == nil || !strings.Contains(err.Error(), "not a built-in subject") {
		t.Fatalf("init rust: %v", err)
	}

	// Defaults now come from the file, with the clock read from go.mod.
	out, errOut, err = exec(filepath.Join(repo, "pkg"), "since", "2025-08-01")
	if err != nil || !strings.Contains(out, "## go (version <= 1.26.1)") {
		t.Fatalf("default since: %v\n%s", err, out)
	}
	if !strings.Contains(errOut, `no axioms known for subject "go:golang.org/x/net" (run ax update go:golang.org/x/net)`) {
		t.Fatalf("empty module subject should point at update:\n%s", errOut)
	}
	if strings.Contains(out, "go-mod") {
		t.Fatalf("subject with nothing to report should be omitted:\n%s", out)
	}
	out, _, _ = exec(filepath.Join(repo, "pkg"), "subjects")
	if !strings.Contains(out, "go\tshape [version:goversion]") || !strings.Contains(out, "default @ 1.26.1 (detected)") || !strings.Contains(out, "go:golang.org/x/net\tshape [version:semver]\t0 axiom(s)\tdefault @ v0.50.0 (detected)") {
		t.Fatalf("subjects:\n%s", out)
	}

	// Module subjects take the built-in semver shape without declaring one,
	// and the detected clock bounds the report.
	if _, errOut, err = exec(filepath.Join(repo, "pkg"), "add", "2026-02-01", "go:golang.org/x/net", "v0.49.0", "net/http2 gained a Transport.MaxReadFrameSize field."); err != nil || !strings.Contains(errOut, "added") {
		t.Fatalf("add module axiom: %v %s", err, errOut)
	}
	if _, _, err = exec(filepath.Join(repo, "pkg"), "add", "2026-03-01", "go:golang.org/x/net", "0.51.0", "future change"); err != nil {
		t.Fatal(err)
	}
	if _, _, err = exec(filepath.Join(repo, "pkg"), "add", "2026-03-01", "go:golang.org/x/net", "banana", "x"); err == nil || !strings.Contains(err.Error(), "not a semantic version") {
		t.Fatalf("bad module clock: %v", err)
	}
	out, _, err = exec(filepath.Join(repo, "pkg"), "since", "2025-08-01")
	if err != nil || !strings.Contains(out, "## go:golang.org/x/net (version <= 0.50.0)") || !strings.Contains(out, "MaxReadFrameSize") || strings.Contains(out, "future change") {
		t.Fatalf("module since: %v\n%s", err, out)
	}
	out, _, err = exec(filepath.Join(repo, "pkg"), "since", "2025-08-01", "go:golang.org/x/net", "0.51.0")
	if err != nil || !strings.Contains(out, "future change") {
		t.Fatalf("explicit module clock: %v\n%s", err, out)
	}
	if _, _, err = exec(filepath.Join(repo, "pkg"), "update", "go:nodots"); err == nil || !strings.Contains(err.Error(), "go:<module path>") {
		t.Fatalf("update with a bad module path: %v", err)
	}

	// Bumping go.mod moves the clock without touching the config.
	os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module example.com/m\n\ngo 1.27\n"), 0o644)
	out, _, _ = exec(filepath.Join(repo, "pkg"), "since", "2025-08-01")
	if !strings.Contains(out, "## go (version <= 1.27.0)") || !strings.Contains(out, "1.27.0") {
		t.Fatalf("bumped go.mod:\n%s", out)
	}

	// A pinned clock in the file wins over detection.
	os.WriteFile(filepath.Join(repo, "pkg", "axiomaton.cnf"), []byte("subjects:\n  - [go, 1.25.0]\n"), 0o644)
	out, _, _ = exec(filepath.Join(repo, "pkg"), "since", "2025-08-01")
	if !strings.Contains(out, "## go (version <= 1.25.0)") {
		t.Fatalf("pinned:\n%s", out)
	}

	// No Go module: init writes a file with no subjects and warns.
	other := filepath.Join(root, "other")
	os.MkdirAll(other, 0o755)
	_, errOut, err = exec(other, "init")
	if err != nil || !strings.Contains(errOut, "no built-in subject detected") {
		t.Fatalf("empty init: %v %s", err, errOut)
	}
	_, errOut, err = exec(other, "init", "-force", "go")
	if err != nil || !strings.Contains(errOut, "ax:   go") {
		t.Fatalf("requested init: %v %s", err, errOut)
	}
	out, _, err = exec(other, "since", "2025-08-01")
	if err != nil || !strings.Contains(out, "## go\n") {
		t.Fatalf("unclocked default should be unconstrained: %v\n%s", err, out)
	}
}
