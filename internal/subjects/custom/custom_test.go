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

package custom

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/canonical/axiomaton/internal/axiom"
	"github.com/canonical/axiomaton/internal/config"
	"github.com/canonical/axiomaton/internal/subjects"
	"github.com/canonical/axiomaton/internal/update"
)

var semverShape = []axiom.ClockShape{{Name: "version", Kind: axiom.KindSemver}}

func TestNew(t *testing.T) {
	dir := t.TempDir()
	def := config.UpdateDef{About: "Juju, the orchestration engine.", Sources: []string{"https://github.com/juju/juju/releases"}}
	s, err := New("juju", semverShape, def)
	if err != nil {
		t.Fatal(err)
	}
	var _ subjects.Builtin = s
	var _ subjects.VMRequirer = s
	var _ update.Prepared = s
	if s.Name() != "juju" || s.RequiresVM() || s.Setup() != "" || len(s.Shipments()) != 0 {
		t.Fatalf("subject: %+v", s)
	}
	if seed, err := s.Seed(); err != nil || seed != nil {
		t.Fatal("custom subjects have no seed")
	}
	if _, _, ok := s.Detect(dir); ok {
		t.Fatal("custom subjects are never detected")
	}
	for _, tc := range []struct {
		name  string
		shape []axiom.ClockShape
		def   config.UpdateDef
		want  string
	}{
		{"", semverShape, def, "no name"},
		{"x", nil, def, "no clock shape"},
		{"x", semverShape, config.UpdateDef{Sources: def.Sources}, "update.about is required"},
		{"x", semverShape, config.UpdateDef{About: "a"}, "update.sources is required"},
		{"x", semverShape, config.UpdateDef{About: "a", Sources: []string{"git@github.com:juju/juju.git"}}, "uses SSH"},
		{"x", semverShape, config.UpdateDef{About: "a", Sources: []string{"http://example.com"}}, "plain http"},
		{"x", semverShape, config.UpdateDef{About: "a", Sources: []string{filepath.Join(dir, "missing")}}, "no such file"},
	} {
		if _, err := New(tc.name, tc.shape, tc.def); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("New(%q, %v, %+v) error = %v, want %q", tc.name, tc.shape, tc.def, err, tc.want)
		}
	}
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
}

func TestBundle(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	repo := filepath.Join(root, "juju")
	os.MkdirAll(repo, 0o755)
	git(t, repo, "init", "-q")
	os.WriteFile(filepath.Join(repo, "README"), []byte("hi\n"), 0o644)
	git(t, repo, "add", "README")
	git(t, repo, "commit", "-q", "-m", "first")
	other := filepath.Join(root, "other", "juju")
	os.MkdirAll(other, 0o755)
	git(t, other, "init", "-q")
	os.WriteFile(filepath.Join(other, "README"), []byte("other\n"), 0o644)
	git(t, other, "add", "README")
	git(t, other, "commit", "-q", "-m", "first")
	plain := filepath.Join(root, "plain")
	os.MkdirAll(plain, 0o755)

	def := config.UpdateDef{About: "about", Sources: []string{"https://example.com/notes", repo, other, "https://docs.example.com"}}
	s, err := New("juju", semverShape, def)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(s.LocalSources(), ","); got != repo+","+other {
		t.Fatalf("local sources: %s", got)
	}
	ins := s.UpdateInstructions()
	for _, want := range []string{
		"1. https://example.com/notes",
		"2. The git repository at " + repo + " on the user's machine",
		"git clone /work/src/juju.bundle /work/juju",
		"git clone /work/src/juju-2.bundle /work/juju-2",
		"4. https://docs.example.com",
	} {
		if !strings.Contains(ins, want) {
			t.Fatalf("instructions lack %q:\n%s", want, ins)
		}
	}

	out := t.TempDir()
	if err := s.Bundle(context.Background(), out); err != nil {
		t.Fatal(err)
	}
	ships := s.Shipments()
	if len(ships) != 2 || ships[0].Remote != "/work/src/juju.bundle" || ships[1].Remote != "/work/src/juju-2.bundle" ||
		ships[0].Local != filepath.Join(out, "juju.bundle") || ships[1].Local != filepath.Join(out, "juju-2.bundle") {
		t.Fatalf("shipments: %+v", ships)
	}
	for _, sh := range ships {
		if st, err := os.Stat(sh.Local); err != nil || st.Size() == 0 {
			t.Fatalf("bundle %s: %v", sh.Local, err)
		}
	}
	// The bundle clones.
	clone := filepath.Join(root, "clone")
	git(t, root, "clone", "-q", ships[0].Local, clone)
	if data, _ := os.ReadFile(filepath.Join(clone, "README")); string(data) != "hi\n" {
		t.Fatalf("clone content: %q", data)
	}

	// A directory that is not a repository cannot be bundled.
	bad, _ := New("x", semverShape, config.UpdateDef{About: "a", Sources: []string{plain}})
	if err := bad.Bundle(context.Background(), t.TempDir()); err == nil || !strings.Contains(err.Error(), "bundling "+plain) {
		t.Fatalf("plain dir: %v", err)
	}
}

func TestPrompts(t *testing.T) {
	shape := []axiom.ClockShape{{Name: "series", Kind: axiom.KindNumber}, {Name: "version", Kind: axiom.KindSemver}}
	def := config.UpdateDef{
		About:   "Juju, the application orchestration engine. Axioms are for charm authors.",
		Sources: []string{"https://github.com/juju/juju/releases", "https://documentation.ubuntu.com/juju/"},
		Setup:   "snap install juju --classic",
		Clock:   "the series is the major.minor track, the version the full release",
		Verify:  "juju bootstrap localhost is the fastest check",
		VM:      true,
	}
	s, err := New("juju", shape, def)
	if err != nil {
		t.Fatal(err)
	}
	if !s.RequiresVM() || s.Setup() != "snap install juju --classic" {
		t.Fatal("vm and setup")
	}
	ins := s.UpdateInstructions()
	for _, want := range []string{
		"axioms about juju. Juju, the application orchestration engine.",
		"fresh Ubuntu virtual machine",
		"This command has already been run in the sandbox for you:\n  snap install juju --classic",
		"1. https://github.com/juju/juju/releases",
		"The clock has 2 value(s), in this order:\n  1. series: a decimal number\n  2. version: a semantic version such as 1.2.3, without a leading v\nthe series is the major.minor track, the version the full release.",
		"propose_axiom",
		"Hard limit: never propose an axiom dated on or before 2023-08-01",
		"ssh does not",
	} {
		if !strings.Contains(ins, want) {
			t.Fatalf("instructions lack %q:\n%s", want, ins)
		}
	}
	if strings.Contains(ins, "batch_commits") {
		t.Fatal("no git clock, no batching")
	}
	if task := s.UpdateTask(nil); !strings.Contains(task, "holds 0 axioms about juju") || strings.Contains(task, "newest clock it covers is") || !strings.Contains(task, "after 2023-08-01, the global epoch") {
		t.Fatalf("empty task:\n%s", task)
	}
	existing := []axiom.Axiom{
		{Time: time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC), Clock: map[string]string{"series": "3.4", "version": "3.4.0"}, Statement: "older"},
		{Time: time.Date(2025, 1, 9, 0, 0, 0, 0, time.UTC), Clock: map[string]string{"series": "3.6", "version": "3.6.1"}, Statement: "newer"},
	}
	task := s.UpdateTask(existing)
	if !strings.Contains(task, "newest clock it covers is 3.6 3.6.1") || !strings.Contains(task, "- [2025-01-09 3.6 3.6.1] newer") || !strings.Contains(task, "- [2024-02-01 3.4 3.4.0] older") {
		t.Fatalf("task:\n%s", task)
	}
	ci := s.ConfirmInstructions()
	for _, want := range []string{"fact checker", "juju bootstrap localhost is the fastest check", "distinguish two cases", "give a split instead", "The clock has 2 value(s)", "snap install juju --classic"} {
		if !strings.Contains(ci, want) {
			t.Fatalf("confirm instructions lack %q:\n%s", want, ci)
		}
	}
	ct := s.ConfirmTask(existing[1])
	for _, want := range []string{"Clock series: 3.6\nClock version: 3.6.1\nDate: 2025-01-09\nStatement: newer", `["24", "1.2.3"]`, "submit_answer"} {
		if !strings.Contains(ct, want) {
			t.Fatalf("confirm task lacks %q:\n%s", want, ct)
		}
	}
	if ct := s.ConfirmTask(axiom.Axiom{Clock: map[string]string{"series": "3.6"}}); !strings.Contains(ct, "Clock version: any (the statement is claimed for every release)") {
		t.Fatalf("partial clock:\n%s", ct)
	}
}

func TestGitClockPrompts(t *testing.T) {
	shape := []axiom.ClockShape{{Name: "commit", Kind: axiom.KindGit, Repo: "/r"}}
	s, err := New("tree", shape, config.UpdateDef{About: "a tree", Sources: []string{"https://example.com/repo.git"}})
	if err != nil {
		t.Fatal(err)
	}
	ins := s.UpdateInstructions()
	for _, want := range []string{"batch_commits", "named by the full hash of its last commit", "1. commit: the full hash of the last commit of the release"} {
		if !strings.Contains(ins, want) {
			t.Fatalf("instructions lack %q:\n%s", want, ins)
		}
	}
	if ct := s.ConfirmTask(axiom.Axiom{Clock: map[string]string{"commit": "abc"}}); !strings.Contains(ct, `["3f9c2a1d8e7b6c5a4f3e2d1c0b9a8f7e6d5c4b3a"]`) {
		t.Fatalf("confirm task:\n%s", ct)
	}
}
