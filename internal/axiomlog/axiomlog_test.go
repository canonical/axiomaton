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

package axiomlog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/canonical/axiomaton/internal/axiom"
	"github.com/canonical/axiomaton/internal/config"
)

func mk(subject, date, ver, stmt string) axiom.Axiom {
	t, _ := time.Parse("2006-01-02", date)
	a := axiom.Axiom{Subject: subject, Time: t, Statement: stmt}
	if ver != "" {
		a.Clock = map[string]string{"version": ver}
	}
	a.Normalise()
	return a
}

func TestReadAppendRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "axiomlog")
	a := mk("go", "2026-02-10", "1.26.0", "new(expr) is allowed")
	b := mk("go", "2026-08-19", "1.27.0", "methods may be generic")
	if err := Append(path, a); err != nil {
		t.Fatal(err)
	}
	// Simulate a hand edit without trailing newline.
	f, _ := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	f.WriteString(`# comment`)
	f.Close()
	if err := Append(path, b); err != nil {
		t.Fatal(err)
	}
	got, err := ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != a.ID || got[1].ID != b.ID || got[1].Origin != path {
		t.Fatalf("round trip: %+v", got)
	}
	if got, err := ReadFile(filepath.Join(dir, "nope")); err != nil || got != nil {
		t.Fatalf("missing file: %v %v", got, err)
	}
	if _, err := Read(strings.NewReader(`{"subject":"x"}`), "bad"); err == nil || !strings.Contains(err.Error(), "bad:1") {
		t.Fatalf("expected validation error with location, got %v", err)
	}
}

func TestMerge(t *testing.T) {
	a := mk("go", "2026-02-10", "1.26.0", "x")
	a.Origin = "builtin"
	a2 := a
	a2.Origin = "project"
	b := mk("go", "2026-02-10", "1.26.0", "y")
	got := Merge([]axiom.Axiom{a}, []axiom.Axiom{a2, b})
	if len(got) != 2 || got[0].Origin != "project" {
		t.Fatalf("merge: %+v", got)
	}
	if len(Filter(got, "Go")) != 2 || len(Filter(got, "rust")) != 0 {
		t.Fatal("filter")
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDiscover(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "xdg"))
	// root/outer/axiomaton.cnf        (outside repo)
	// root/outer/repo/.git
	// root/outer/repo/axiomaton.cnf  + axiomlog
	// root/outer/repo/a/b/axiomaton.cnf
	// root/outer/repo/a/b/c           (cwd)
	writeFile(t, filepath.Join(root, "outer", "axiomaton.cnf"), "subjects: [[\"outer\", \"1\"]]\nshapes.outer: [{name: \"n\", kind: \"number\"}]\n")
	writeFile(t, filepath.Join(root, "outer", "axiomlog"), `{"subject":"outer","time":"2026-01-01T00:00:00Z","clock":{"n":"1"},"statement":"outer fact"}`+"\n")
	os.MkdirAll(filepath.Join(root, "outer", "repo", ".git"), 0o755)
	writeFile(t, filepath.Join(root, "outer", "repo", "axiomaton.cnf"), "subjects: [[\"go\", \"1.26.1\"], [\"svc\", \"1.0.0\"]]\nshapes.svc: [{name: \"version\", kind: \"semver\"}]\n")
	writeFile(t, filepath.Join(root, "outer", "repo", "axiomlog"), `{"subject":"go","time":"2026-02-10T00:00:00Z","clock":{"version":"1.26.0"},"statement":"repo fact"}`+"\n")
	writeFile(t, filepath.Join(root, "outer", "repo", "a", "b", "axiomaton.cnf"), "subjects: [[\"go\", \"1.25.0\"]]\n")
	cwd := filepath.Join(root, "outer", "repo", "a", "b", "c")
	os.MkdirAll(cwd, 0o755)

	ws, err := Discover(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if ws.RepoRoot != filepath.Join(root, "outer", "repo") {
		t.Fatalf("repo root = %q", ws.RepoRoot)
	}
	defs := ws.DefaultSubjects(false)
	names := []string{}
	for _, d := range defs {
		names = append(names, d.Name+"@"+strings.Join(d.Clock, ","))
	}
	if strings.Join(names, " ") != "go@1.25.0 svc@1.0.0" {
		t.Fatalf("default subjects = %v", names)
	}
	defs = ws.DefaultSubjects(true)
	if len(defs) != 3 || defs[2].Name != "outer" {
		t.Fatalf("global subjects = %+v", defs)
	}
	reg, err := ws.Registry(map[string][]axiom.ClockShape{"go": {{Name: "version", Kind: axiom.KindGoVersion}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"go", "svc", "outer"} {
		if _, ok := reg.Lookup(n); !ok {
			t.Fatalf("%s missing from registry", n)
		}
	}
	all, err := ws.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("loaded %d axioms, want 2 (outer log is consulted even outside repo)", len(all))
	}
	target, err := ws.WriteTarget()
	if err != nil {
		t.Fatal(err)
	}
	if target != filepath.Join(root, "outer", "repo", "a", "b", "axiomlog") {
		t.Fatalf("write target = %q", target)
	}

	// No project files at all inside a repo: falls back to the user log,
	// even past a stray axiomlog with no axiomaton.cnf beside it.
	bare := filepath.Join(root, "bare")
	os.MkdirAll(filepath.Join(bare, ".git"), 0o755)
	os.MkdirAll(filepath.Join(bare, "x"), 0o755)
	writeFile(t, filepath.Join(bare, "x", "axiomlog"), "")
	ws, _ = Discover(filepath.Join(bare, "x"))
	if target, _ := ws.WriteTarget(); target != filepath.Join(root, "xdg", "axiomaton", "axiomlog") {
		t.Fatalf("bare target = %q", target)
	}
	if len(ws.DefaultSubjects(false)) != 0 {
		t.Fatal("bare repo should have no defaults")
	}

	// No repo: the working directory's own cnf, else the user log.
	ws, _ = Discover(filepath.Join(root, "outer"))
	if target, _ := ws.WriteTarget(); target != filepath.Join(root, "outer", "axiomlog") {
		t.Fatalf("outer target = %q (own dir has a cnf)", target)
	}
	loose := filepath.Join(root, "loose")
	os.MkdirAll(loose, 0o755)
	ws, _ = Discover(loose)
	if target, _ := ws.WriteTarget(); target != filepath.Join(root, "xdg", "axiomaton", "axiomlog") {
		t.Fatalf("loose target = %q", target)
	}
}

func TestProjectSubject(t *testing.T) {
	root := t.TempDir()
	inner := filepath.Join(root, "repo", "svc")
	os.MkdirAll(inner, 0o755)
	os.MkdirAll(filepath.Join(root, "repo", ".git"), 0o755)
	os.WriteFile(filepath.Join(root, config.ProjectFile), []byte("subjects:\n    - {name: \"juju\", shape: [{name: \"version\", kind: \"semver\"}], update: {about: \"outer\", sources: [\"https://outer\"]}}\n    - {name: \"top\", update: {about: \"top\", sources: [\"https://top\"]}}\n"), 0o644)
	os.WriteFile(filepath.Join(root, "repo", config.ProjectFile), []byte("subjects:\n    - {name: \"juju\", shape: [{name: \"version\", kind: \"semver\"}], update: {about: \"inner\", sources: [\"https://inner\"]}}\n    - \"plain\"\n"), 0o644)
	ws, err := Discover(inner)
	if err != nil {
		t.Fatal(err)
	}
	ref, file, ok := ws.ProjectSubject("juju")
	if !ok || ref.Update == nil || ref.Update.About != "inner" || file != filepath.Join(root, "repo", config.ProjectFile) {
		t.Fatalf("nearest wins: %+v %s %v", ref, file, ok)
	}
	if ref, _, ok := ws.ProjectSubject("plain"); !ok || ref.Update != nil {
		t.Fatalf("plain: %+v %v", ref, ok)
	}
	if ref, file, ok := ws.ProjectSubject("top"); !ok || ref.Update.About != "top" || file != filepath.Join(root, config.ProjectFile) {
		t.Fatalf("outer layers count: %+v %s %v", ref, file, ok)
	}
	if _, _, ok := ws.ProjectSubject("nope"); ok {
		t.Fatal("undeclared")
	}
	if got := strings.Join(ws.Updatable(), ","); got != "juju,top" {
		t.Fatalf("updatable: %s", got)
	}
}
