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

package python

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/canonical/axiomaton/internal/axiom"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLowerBound(t *testing.T) {
	cases := map[string]string{
		">=3.11":          "3.11",
		">= 3.9, <4":      "3.9",
		"<4,>=3.10.2":     "3.10.2",
		"~=3.10.2":        "3.10.2",
		"==3.12.*":        "3.12",
		"^3.11":           "3.11",
		"~3.11":           "3.11",
		"3.12":            "3.12",
		">3.8":            "3.8",
		"<4":              "",
		"":                "",
		">=3.13.0rc1":     "",
		">=3.11,!=3.11.0": "3.11",
	}
	for spec, want := range cases {
		if got := lowerBound(spec); got != want {
			t.Errorf("lowerBound(%q) = %q, want %q", spec, got, want)
		}
	}
}

func TestDetect(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	os.MkdirAll(filepath.Join(repo, ".git"), 0o755)
	pkg := filepath.Join(repo, "src", "pkg")
	os.MkdirAll(pkg, 0o755)
	write(t, filepath.Join(root, "pyproject.toml"), "[project]\nrequires-python = \">=3.6\"\n")

	if _, _, ok := (Subject{}).Detect(pkg); ok {
		t.Fatal("pyproject above the repository must not count")
	}

	// requirements.txt alone marks a project, with no clock.
	write(t, filepath.Join(repo, "requirements.txt"), "requests\n")
	clock, source, ok := (Subject{}).Detect(pkg)
	if !ok || clock != nil || source != filepath.Join(repo, "requirements.txt") {
		t.Fatalf("unpinned: %v %s %v", clock, source, ok)
	}

	// requires-python lower bound.
	write(t, filepath.Join(repo, "pyproject.toml"), "[build-system]\nrequires = [\"hatchling\"]\n\n[project]\nname = \"pkg\"\nversion = \"1.0\" # the package\nrequires-python = \">=3.11, <4\"\n")
	clock, source, ok = (Subject{}).Detect(pkg)
	if !ok || strings.Join(clock, ",") != "3.11" || source != filepath.Join(repo, "pyproject.toml") {
		t.Fatalf("requires-python: %v %s %v", clock, source, ok)
	}
	if _, err := (axiom.Subject{Name: Name, Shape: Subject{}.Shape()}).Clock(clock); err != nil {
		t.Fatal(err)
	}

	// Poetry constraint when [project] has no requires-python.
	write(t, filepath.Join(repo, "pyproject.toml"), "[tool.poetry]\nname = \"pkg\"\n\n[tool.poetry.dependencies]\npython = \"^3.10\"\nrequests = \"*\"\n")
	if clock, _, _ = (Subject{}).Detect(pkg); strings.Join(clock, ",") != "3.10" {
		t.Fatalf("poetry: %v", clock)
	}

	// Pipfile.
	os.Remove(filepath.Join(repo, "pyproject.toml"))
	write(t, filepath.Join(repo, "Pipfile"), "[[source]]\nurl = \"https://pypi.org/simple\"\n\n[requires]\npython_version = \"3.9\"\n")
	clock, source, _ = (Subject{}).Detect(pkg)
	if strings.Join(clock, ",") != "3.9" || source != filepath.Join(repo, "Pipfile") {
		t.Fatalf("pipfile: %v %s", clock, source)
	}

	// A pinned interpreter wins; non-release entries are skipped.
	write(t, filepath.Join(repo, ".python-version"), "system\npypy3.10\n3.12.4 # exact\n")
	clock, source, _ = (Subject{}).Detect(pkg)
	if strings.Join(clock, ",") != "3.12.4" || source != filepath.Join(repo, ".python-version") {
		t.Fatalf("pin: %v %s", clock, source)
	}
	write(t, filepath.Join(repo, ".python-version"), "3.13.0rc2\n")
	if clock, _, _ = (Subject{}).Detect(pkg); strings.Join(clock, ",") != "3.9" {
		t.Fatalf("pre-release pin is ignored: %v", clock)
	}
}

func TestPrompts(t *testing.T) {
	s := Subject{}
	seed, err := s.Seed()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range seed {
		if a.Subject != Name || a.Clock["version"] == "" {
			t.Fatalf("bad seed axiom: %+v", a)
		}
	}
	ins := s.UpdateInstructions()
	for _, want := range []string{"whatsnew/3.N.html", "propose_axiom", "append-only log", "uv python install", "Hard limit: never propose an axiom dated on or before 2023-08-01"} {
		if !strings.Contains(ins, want) {
			t.Fatalf("instructions lack %q", want)
		}
	}
	if task := s.UpdateTask(nil); !strings.Contains(task, "holds 0 axioms") || !strings.Contains(task, "after 2023-08-01, the global epoch") {
		t.Fatalf("empty task:\n%s", task)
	}
	existing := []axiom.Axiom{
		{Time: time.Date(2024, 10, 7, 0, 0, 0, 0, time.UTC), Clock: map[string]string{"version": "3.13.0"}, Statement: "repl"},
		{Time: time.Date(2023, 10, 2, 0, 0, 0, 0, time.UTC), Clock: map[string]string{"version": "3.12.0"}, Statement: "fstrings"},
	}
	if task := s.UpdateTask(existing); !strings.Contains(task, "newest Python release it covers is 3.13.0") || !strings.Contains(task, "- [2024-10-07 3.13.0] repl") {
		t.Fatalf("task:\n%s", task)
	}
	confirm := s.ConfirmTask(existing[0])
	if !strings.Contains(confirm, "Python release: 3.13.0 (released 2024-10-07)") || !strings.Contains(confirm, "a correction giving the") {
		t.Fatalf("confirm:\n%s", confirm)
	}
	if !strings.Contains(s.ConfirmInstructions(), "distinguish two cases") || !strings.Contains(s.ConfirmInstructions(), "uv run --python <version>") {
		t.Fatal("confirm instructions")
	}
}
