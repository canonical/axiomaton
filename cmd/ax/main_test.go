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
	"errors"
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
	if _, _, err = exec("add", "2026-09-01", "nim", "2.2", "x"); err == nil || !strings.Contains(err.Error(), "no declared clock shape") {
		t.Fatalf("undeclared subject add should fail: %v", err)
	}
	if _, _, err = exec("add", "2026-09-01", "svc", "banana", "x"); err == nil {
		t.Fatal("bad clock should fail")
	}
	if _, _, err = exec("add", "2023-08-01", "svc", "2.4.0", "ancient"); err == nil || !strings.Contains(err.Error(), "not after the global epoch 2023-08-01") {
		t.Fatalf("pre-epoch add should fail: %v", err)
	}
	if _, _, err = exec("add", "2023-08-02", "svc", "2.4.0", "day after the epoch"); err != nil {
		t.Fatalf("day after the epoch add: %v", err)
	}

	// A cutoff before the epoch is raised to it with a warning.
	out, errOut, err = exec("since", "2001-01-01", "svc", "2.4.0")
	if err != nil || !strings.Contains(errOut, "cutoff 2001-01-01 is before the global epoch 2023-08-01") || !strings.Contains(out, "svc exposes a v2 API") || !strings.Contains(out, "day after the epoch") {
		t.Fatalf("clamped since: %v\n%s\n%s", err, out, errOut)
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
	if err != nil || !strings.Contains(out, "go\tshape [version:goversion]") || !strings.Contains(out, "svc\tshape [version:semver]\t2 axiom(s)\tdefault @ 2.3.0") {
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
	if _, _, err := exec("update", "nim"); err == nil || !strings.Contains(err.Error(), "built-in") || strings.Contains(err.Error(), "update brief (") {
		t.Fatalf("update nim: %v", err)
	}
	// A project subject needs an update brief before an agent can extend it.
	if _, _, err := exec("update", "svc"); err == nil || !strings.Contains(err.Error(), `"svc" is declared in`) || !strings.Contains(err.Error(), "without an update: block") {
		t.Fatalf("update svc: %v", err)
	}
	cnf := "subjects:\n  - [go, 1.26.1]\n  - name: svc\n    clock: [2.3.0]\n    shape: [{name: version, kind: semver}]\n    update: {about: a service, sources: [https://example.com/releases]}\n  - name: noshape\n    update: {about: shapeless, sources: [https://example.com]}\n"
	os.WriteFile(filepath.Join(repo, "axiomaton.cnf"), []byte(cnf), 0o644)
	if _, _, err := exec("update", "nim"); err == nil || !strings.Contains(err.Error(), "update brief (svc, noshape)") {
		t.Fatalf("update nim lists custom subjects: %v", err)
	}
	if _, _, err := exec("update", "noshape"); err == nil || !strings.Contains(err.Error(), "no clock shape") {
		t.Fatalf("update noshape: %v", err)
	}
	if out, _, err := exec("subjects"); err != nil || !strings.Contains(out, "svc\tshape [version:semver]\t2 axiom(s)\tdefault @ 2.3.0\tupdatable") || strings.Contains(out, "go\tshape [version:goversion]\t") && strings.Contains(out, "goversion]\t0 axiom(s)\tupdatable") {
		t.Fatalf("subjects marks updatable project subjects: %v\n%s", err, out)
	}
	// With a usable brief the next failure is the missing agent model.
	if _, _, err := exec("update", "svc"); err == nil || strings.Contains(err.Error(), "svc") {
		t.Fatalf("update svc with a brief should get past subject resolution: %v", err)
	}
	// Built-ins cannot be given a brief.
	os.WriteFile(filepath.Join(repo, "axiomaton.cnf"), []byte("subjects:\n  - name: go\n    update: {about: go, sources: [https://go.dev]}\n"), 0o644)
	if _, _, err := exec("update", "go"); err == nil || !strings.Contains(err.Error(), "built-in subject that ax updates by itself") {
		t.Fatalf("update go with a brief: %v", err)
	}
	os.WriteFile(filepath.Join(repo, "axiomaton.cnf"), []byte("subjects:\n  - [go, 1.26.1]\n  - name: svc\n    clock: [2.3.0]\n    shape: [{name: version, kind: semver}]\n"), 0o644)
}

func TestInit(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "cfg"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	repo := filepath.Join(root, "repo")
	os.MkdirAll(filepath.Join(repo, ".git"), 0o755)
	os.MkdirAll(filepath.Join(repo, "pkg"), 0o755)
	os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module example.com/m\n\ngo 1.26.1\n\nrequire (\n\tgolang.org/x/net v0.50.0\n\texample.com/dep v1.2.3\n\tgithub.com/google/uuid v1.6.0 // indirect\n)\n"), 0o644)

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
	// Inside a repository the detected subjects are the defaults before
	// any init, and nothing is written.
	out, errOut, err := exec(filepath.Join(repo, "pkg"), "-v", "since", "2025-08-01")
	if err != nil || !strings.Contains(out, "## go (version <= 1.26.1)") || !strings.Contains(errOut, "go detected from ../go.mod; no axiomaton.cnf lists it") || !strings.Contains(errOut, "go:golang.org/x/net detected from ../go.mod") {
		t.Fatalf("detected defaults: %v\n%s\n%s", err, out, errOut)
	}
	if _, err := os.Stat(filepath.Join(repo, "pkg", "axiomaton.cnf")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("since wrote a config: %v", err)
	}
	out, errOut, err = exec(filepath.Join(repo, "pkg"), "subjects")
	if err != nil || !strings.Contains(out, "go\tshape [version:goversion]") || !strings.Contains(out, "default @ 1.26.1 (detected)") {
		t.Fatalf("subjects before init: %v\n%s\n%s", err, out, errOut)
	}

	_, errOut, err = exec(filepath.Join(repo, "pkg"), "init")
	if err != nil {
		t.Fatalf("init: %v\n%s", err, errOut)
	}
	if !strings.Contains(errOut, "go @ 1.26.1 (from ../go.mod)") || !strings.Contains(errOut, "go:golang.org/x/net @ v0.50.0 (from ../go.mod)") || !strings.Contains(errOut, "run `ax setup`") {
		t.Fatalf("init output:\n%s", errOut)
	}
	if _, err := os.Stat(filepath.Join(repo, "pkg", "axiomlog")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("init wrote an axiomlog: %v", err)
	}
	if strings.Contains(errOut, "uuid") {
		t.Fatalf("indirect dependency listed:\n%s", errOut)
	}
	cnf, _ := os.ReadFile(filepath.Join(repo, "pkg", "axiomaton.cnf"))
	if !strings.Contains(string(cnf), "  - go  # detected: ../go.mod says 1.26.1") || !strings.Contains(string(cnf), "  - go:golang.org/x/net  # detected: ../go.mod says v0.50.0") {
		t.Fatalf("cnf:\n%s", cnf)
	}
	// A second init finds nothing new and leaves the file alone.
	_, errOut, err = exec(filepath.Join(repo, "pkg"), "init")
	if err != nil || !strings.Contains(errOut, "is up to date (") || !strings.Contains(errOut, "nothing written") {
		t.Fatalf("second init: %v\n%s", err, errOut)
	}
	if again, _ := os.ReadFile(filepath.Join(repo, "pkg", "axiomaton.cnf")); string(again) != string(cnf) {
		t.Fatalf("file changed by an up to date init:\n%s", again)
	}
	// Hand edits survive an update that adds a requested subject.
	custom := "  - name: juju\n    clock: [3.6.4]\n    shape: [{name: version, kind: semver}]\n    update:\n      about: Juju, the engine.\n      sources: [https://github.com/juju/juju/releases]\n"
	edited := strings.Replace(string(cnf), "subjects:\n", "subjects:\n"+custom, 1) + "shapes:\n  tree:\n    - {name: commit, kind: git, repo: ../tree}  # keep me\n"
	os.WriteFile(filepath.Join(repo, "pkg", "axiomaton.cnf"), []byte(edited), 0o644)
	_, errOut, err = exec(filepath.Join(repo, "pkg"), "init", "rust")
	if err != nil || !strings.Contains(errOut, "updated "+filepath.Join(repo, "pkg", "axiomaton.cnf")) || !strings.Contains(errOut, "ax:   + rust") || strings.Contains(errOut, "+ go") {
		t.Fatalf("init rust: %v\n%s", err, errOut)
	}
	updated, _ := os.ReadFile(filepath.Join(repo, "pkg", "axiomaton.cnf"))
	for _, want := range []string{
		"# axiomaton.cnf: the subjects this project depends on.",
		// yaml.v3 quotes the URL for its colon; the file is otherwise as written.
		"  - name: juju\n    clock: [3.6.4]\n    shape: [{name: version, kind: semver}]\n    update:\n      about: Juju, the engine.\n      sources: ['https://github.com/juju/juju/releases']\n",
		"  - go # detected: ../go.mod says 1.26.1\n",
		"  - go:golang.org/x/net # detected: ../go.mod says v0.50.0\n",
		"  - rust # requested; clock is detected at query time when possible\n",
		"# Other subjects need a clock shape before clocked axioms can be added:\n",
		"shapes:\n  tree:\n    - {name: commit, kind: git, repo: ../tree} # keep me\n",
	} {
		if !strings.Contains(string(updated), want) {
			t.Fatalf("updated cnf lacks %q:\n%s", want, updated)
		}
	}
	if _, _, err := exec(filepath.Join(repo, "pkg"), "since", "2025-08-01"); err != nil {
		t.Fatalf("updated cnf must still load: %v", err)
	}
	// Listed built-ins that are no longer detected are pointed out.
	_, errOut, err = exec(filepath.Join(repo, "pkg"), "init")
	if err != nil || !strings.Contains(errOut, "lists rust, which is not detected in") || !strings.Contains(errOut, "nothing written") {
		t.Fatalf("stale subject warning: %v\n%s", err, errOut)
	}
	// A broken file is not touched.
	os.WriteFile(filepath.Join(repo, "pkg", "axiomaton.cnf"), []byte("subjects: notalist\n"), 0o644)
	if _, _, err := exec(filepath.Join(repo, "pkg"), "init"); err == nil || !strings.Contains(err.Error(), "-force") {
		t.Fatalf("broken cnf: %v", err)
	}
	// An empty file gains a subjects list.
	os.WriteFile(filepath.Join(repo, "pkg", "axiomaton.cnf"), nil, 0o644)
	if _, errOut, err := exec(filepath.Join(repo, "pkg"), "init"); err != nil || !strings.Contains(errOut, "+ go @ 1.26.1") {
		t.Fatalf("empty cnf: %v\n%s", err, errOut)
	}
	if data, _ := os.ReadFile(filepath.Join(repo, "pkg", "axiomaton.cnf")); !strings.HasPrefix(string(data), "subjects:\n  - go # detected") {
		t.Fatalf("empty cnf result:\n%s", data)
	}
	os.WriteFile(filepath.Join(repo, "pkg", "axiomaton.cnf"), cnf, 0o644)
	if _, _, err := exec(filepath.Join(repo, "pkg"), "init", "-force", "nim"); err == nil || !strings.Contains(err.Error(), "not a built-in subject") {
		t.Fatalf("init nim: %v", err)
	}

	// Defaults now come from the file, with the clock read from go.mod.
	out, errOut, err = exec(filepath.Join(repo, "pkg"), "since", "2025-08-01")
	if err != nil || !strings.Contains(out, "## go (version <= 1.26.1)") {
		t.Fatalf("default since: %v\n%s", err, out)
	}
	// A dependency that ships no log is empty and says so; it is omitted
	// from the report. (golang.org/x modules ship logs, so use another.)
	if !strings.Contains(errOut, `no axioms known for subject "go:example.com/dep" (run ax update go:example.com/dep)`) {
		t.Fatalf("empty module subject should point at update:\n%s", errOut)
	}
	if strings.Contains(out, "go:example.com/dep") {
		t.Fatalf("subject with nothing to report should be omitted:\n%s", out)
	}
	out, _, _ = exec(filepath.Join(repo, "pkg"), "subjects")
	if !strings.Contains(out, "go\tshape [version:goversion]") || !strings.Contains(out, "default @ 1.26.1 (detected)") || !strings.Contains(out, "go:golang.org/x/net\tshape [version:semver]\t") || !strings.Contains(out, "default @ v0.50.0 (detected)") || !strings.Contains(out, "go:example.com/dep\tshape [version:semver]\t0 axiom(s)\tdefault @ v1.2.3 (detected)") {
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

	// A Rust crate: detected from Cargo.toml, clocked by rust-version, or
	// unclocked when nothing is pinned.
	crate := filepath.Join(root, "crate")
	os.MkdirAll(filepath.Join(crate, ".git"), 0o755)
	os.WriteFile(filepath.Join(crate, "Cargo.toml"), []byte("[package]\nname = \"c\"\nrust-version = \"1.80\"\n"), 0o644)
	_, errOut, err = exec(crate, "init")
	if err != nil || !strings.Contains(errOut, "rust @ 1.80 (from Cargo.toml)") {
		t.Fatalf("rust init: %v %s", err, errOut)
	}
	// Nothing in the built-in rust log is both after the cutoff and within
	// rust-version 1.80, so the subject is omitted.
	out, errOut, err = exec(crate, "-v", "since", "2025-08-01")
	if err != nil || !strings.Contains(errOut, `nothing to report for subject "rust"; omitted`) || strings.Contains(out, "## rust") {
		t.Fatalf("rust since: %v\n%s\n%s", err, out, errOut)
	}
	if _, _, err = exec(crate, "add", "2026-01-15", "rust", "1.90.0", "rust fact"); err != nil {
		t.Fatal(err)
	}
	out, _, _ = exec(crate, "since", "2025-08-01")
	if strings.Contains(out, "rust fact") {
		t.Fatalf("1.90.0 axiom must not apply at rust-version 1.80:\n%s", out)
	}
	os.WriteFile(filepath.Join(crate, "Cargo.toml"), []byte("[package]\nname = \"c\"\n"), 0o644)
	out, _, _ = exec(crate, "since", "2025-08-01")
	if !strings.Contains(out, "## rust\n") || !strings.Contains(out, "- **[1.90.0]** rust fact") {
		t.Fatalf("unpinned rust is unconstrained:\n%s", out)
	}
	_, errOut, err = exec(crate, "init", "-force")
	if err != nil || !strings.Contains(errOut, "rust (from Cargo.toml; no version pinned)") {
		t.Fatalf("unpinned init: %v %s", err, errOut)
	}
	cnf, _ = os.ReadFile(filepath.Join(crate, "axiomaton.cnf"))
	if !strings.Contains(string(cnf), "  - rust  # detected from Cargo.toml; no version pinned, so every release is reported") {
		t.Fatalf("cnf:\n%s", cnf)
	}

	// A Python project: detected from pyproject.toml, clocked by the
	// requires-python lower bound, and bounded by it in reports.
	py := filepath.Join(root, "py")
	os.MkdirAll(filepath.Join(py, ".git"), 0o755)
	os.WriteFile(filepath.Join(py, "pyproject.toml"), []byte("[project]\nname = \"p\"\nrequires-python = \">=3.12\"\n"), 0o644)
	_, errOut, err = exec(py, "init")
	if err != nil || !strings.Contains(errOut, "python @ 3.12 (from pyproject.toml)") {
		t.Fatalf("python init: %v %s", err, errOut)
	}
	if _, _, err = exec(py, "add", "2024-10-07", "python", "3.13.0", "python fact"); err != nil {
		t.Fatal(err)
	}
	out, _, _ = exec(py, "since", "2024-01-01")
	if strings.Contains(out, "python fact") {
		t.Fatalf("3.13.0 axiom must not apply at requires-python 3.12:\n%s", out)
	}
	os.WriteFile(filepath.Join(py, ".python-version"), []byte("3.13.1\n"), 0o644)
	out, _, _ = exec(py, "since", "2024-01-01")
	if !strings.Contains(out, "## python (version <= 3.13.1)") || !strings.Contains(out, "- **[3.13.0]** python fact") {
		t.Fatalf("pinned interpreter:\n%s", out)
	}

	// A Zig project: detected from build.zig, clocked by minimum_zig_version.
	zg := filepath.Join(root, "zig")
	os.MkdirAll(filepath.Join(zg, ".git"), 0o755)
	os.WriteFile(filepath.Join(zg, "build.zig"), []byte("const std = @import(\"std\");\n"), 0o644)
	os.WriteFile(filepath.Join(zg, "build.zig.zon"), []byte(".{\n    .name = .z,\n    .minimum_zig_version = \"0.13.0\",\n}\n"), 0o644)
	_, errOut, err = exec(zg, "init")
	if err != nil || !strings.Contains(errOut, "zig @ 0.13.0 (from build.zig.zon)") {
		t.Fatalf("zig init: %v %s", err, errOut)
	}
	if _, _, err = exec(zg, "add", "2025-03-05", "zig", "0.14.0", "zig fact"); err != nil {
		t.Fatal(err)
	}
	out, _, _ = exec(zg, "since", "2024-01-01")
	if strings.Contains(out, "zig fact") {
		t.Fatalf("0.14.0 axiom must not apply at minimum_zig_version 0.13.0:\n%s", out)
	}
	os.WriteFile(filepath.Join(zg, ".zigversion"), []byte("0.14.1\n"), 0o644)
	out, _, _ = exec(zg, "since", "2024-01-01")
	if !strings.Contains(out, "## zig (version <= 0.14.1)") || !strings.Contains(out, "- **[0.14.0]** zig fact") {
		t.Fatalf("pinned compiler:\n%s", out)
	}

	// A snap project: detected from snapcraft.yaml, clocked by its base;
	// axioms may carry the base alone or the base and a release.
	sn := filepath.Join(root, "snap")
	os.MkdirAll(filepath.Join(sn, ".git"), 0o755)
	os.MkdirAll(filepath.Join(sn, "snap"), 0o755)
	os.WriteFile(filepath.Join(sn, "snap", "snapcraft.yaml"), []byte("name: s\nbase: core22\n"), 0o644)
	_, errOut, err = exec(sn, "init")
	if err != nil || !strings.Contains(errOut, "snapcraft @ 22 (from snap/snapcraft.yaml)") {
		t.Fatalf("snapcraft init: %v %s", err, errOut)
	}
	if _, _, err = exec(sn, "add", "2024-07-11", "snapcraft", "24", "8.3.0", "platforms replace architectures"); err != nil {
		t.Fatal(err)
	}
	if _, _, err = exec(sn, "add", "2024-02-01", "snapcraft", "22", "craftctl replaces snapcraftctl"); err != nil {
		t.Fatal(err)
	}
	out, _, _ = exec(sn, "since", "2024-01-01")
	if !strings.Contains(out, "## snapcraft (base <= 22)") || !strings.Contains(out, "- **[22]** craftctl") || strings.Contains(out, "platforms replace") {
		t.Fatalf("core22 project:\n%s", out)
	}
	os.WriteFile(filepath.Join(sn, "snap", "snapcraft.yaml"), []byte("name: s\nbase: core24\n"), 0o644)
	out, _, _ = exec(sn, "since", "2024-01-01")
	if !strings.Contains(out, "## snapcraft (base <= 24)") || !strings.Contains(out, "- **[24 8.3.0]** platforms replace architectures") {
		t.Fatalf("core24 project:\n%s", out)
	}
	out, _, _ = exec(sn, "since", "2024-01-01", "snapcraft", "24", "8.2.0")
	if strings.Contains(out, "platforms replace") {
		t.Fatalf("release bound should exclude 8.3.0 at 8.2.0:\n%s", out)
	}
	// The base may be typed as it appears in snapcraft.yaml.
	if _, _, err = exec(sn, "add", "2025-09-01", "snapcraft", "core26", "8.11.0", "core26 is a supported base"); err != nil {
		t.Fatalf("add with core26: %v", err)
	}
	out, _, _ = exec(sn, "since", "2024-01-01", "snapcraft", "core26", "8.11.0")
	if !strings.Contains(out, "- **[26 8.11.0]** core26 is a supported base") {
		t.Fatalf("core26 should be read as base 26:\n%s", out)
	}
	if _, errOut, err = exec(sn, "add", "2025-09-01", "snapcraft", "bare", "8.11.0", "nonsense"); err == nil || !strings.Contains(err.Error()+errOut, "is not a number") {
		t.Fatalf("an unrecognised base must still be rejected: %v %s", err, errOut)
	}

	// A charm project: detected from charmcraft.yaml, clocked by its
	// Ubuntu base.
	ch := filepath.Join(root, "charm")
	os.MkdirAll(filepath.Join(ch, ".git"), 0o755)
	os.WriteFile(filepath.Join(ch, "charmcraft.yaml"), []byte("type: charm\nbase: ubuntu@22.04\n"), 0o644)
	_, errOut, err = exec(ch, "init")
	if err != nil || !strings.Contains(errOut, "charmcraft @ 22.04 (from charmcraft.yaml)") {
		t.Fatalf("charmcraft init: %v %s", err, errOut)
	}
	if _, _, err = exec(ch, "add", "2024-09-10", "charmcraft", "24.04", "3.2.0", "platforms replace bases"); err != nil {
		t.Fatal(err)
	}
	if _, _, err = exec(ch, "add", "2024-02-01", "charmcraft", "22.04", "fetch-libs reads charmcraft.yaml"); err != nil {
		t.Fatal(err)
	}
	out, _, _ = exec(ch, "since", "2024-01-01")
	if !strings.Contains(out, "## charmcraft (base <= 22.04)") || !strings.Contains(out, "- **[22.04]** fetch-libs") || strings.Contains(out, "platforms replace") {
		t.Fatalf("22.04 charm:\n%s", out)
	}
	os.WriteFile(filepath.Join(ch, "charmcraft.yaml"), []byte("type: charm\nbase: ubuntu@24.04\n"), 0o644)
	out, _, _ = exec(ch, "since", "2024-01-01")
	if !strings.Contains(out, "## charmcraft (base <= 24.04)") || !strings.Contains(out, "- **[24.04 3.2.0]** platforms replace bases") {
		t.Fatalf("24.04 charm:\n%s", out)
	}

	// A rock project: detected from rockcraft.yaml, clocked by its base,
	// with bare rocks taking the build-base.
	rk := filepath.Join(root, "rock")
	os.MkdirAll(filepath.Join(rk, ".git"), 0o755)
	os.WriteFile(filepath.Join(rk, "rockcraft.yaml"), []byte("name: r\nbase: bare\nbuild-base: ubuntu@22.04\n"), 0o644)
	_, errOut, err = exec(rk, "init")
	if err != nil || !strings.Contains(errOut, "rockcraft @ 22.04 (from rockcraft.yaml)") {
		t.Fatalf("rockcraft init: %v %s", err, errOut)
	}
	if _, _, err = exec(rk, "add", "2024-06-20", "rockcraft", "24.04", "1.5.0", "noble rocks"); err != nil {
		t.Fatal(err)
	}
	if _, _, err = exec(rk, "add", "2024-02-01", "rockcraft", "22.04", "pebble is the entrypoint"); err != nil {
		t.Fatal(err)
	}
	out, _, _ = exec(rk, "since", "2024-01-01")
	if !strings.Contains(out, "## rockcraft (base <= 22.04)") || !strings.Contains(out, "- **[22.04]** pebble") || strings.Contains(out, "noble rocks") {
		t.Fatalf("22.04 rock:\n%s", out)
	}
	os.WriteFile(filepath.Join(rk, "rockcraft.yaml"), []byte("name: r\nbase: ubuntu@24.04\n"), 0o644)
	out, _, _ = exec(rk, "since", "2024-01-01")
	if !strings.Contains(out, "## rockcraft (base <= 24.04)") || !strings.Contains(out, "- **[24.04 1.5.0]** noble rocks") {
		t.Fatalf("24.04 rock:\n%s", out)
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
