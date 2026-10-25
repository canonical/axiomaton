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

package zig

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

const zon = `.{
    .name = .demo,
    .version = "0.1.0", // the package, not the compiler
    // .minimum_zig_version = "0.11.0",
    .minimum_zig_version = "0.14.0",
    .dependencies = .{},
    .paths = .{""},
}
`

func TestDetect(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	os.MkdirAll(filepath.Join(repo, ".git"), 0o755)
	src := filepath.Join(repo, "src")
	os.MkdirAll(src, 0o755)
	write(t, filepath.Join(root, "build.zig"), "")

	if _, _, ok := (Subject{}).Detect(src); ok {
		t.Fatal("build.zig above the repository must not count")
	}

	write(t, filepath.Join(repo, "build.zig"), "const std = @import(\"std\");\n")
	clock, source, ok := (Subject{}).Detect(src)
	if !ok || clock != nil || source != filepath.Join(repo, "build.zig") {
		t.Fatalf("unpinned: %v %s %v", clock, source, ok)
	}

	write(t, filepath.Join(repo, "build.zig.zon"), zon)
	clock, source, ok = (Subject{}).Detect(src)
	if !ok || strings.Join(clock, ",") != "0.14.0" || source != filepath.Join(repo, "build.zig.zon") {
		t.Fatalf("minimum_zig_version: %v %s %v", clock, source, ok)
	}
	if _, err := (axiom.Subject{Name: Name, Shape: Subject{}.Shape()}).Clock(clock); err != nil {
		t.Fatal(err)
	}

	write(t, filepath.Join(repo, ".zigversion"), "0.15.0-dev.1234+abcdef12\n")
	clock, source, _ = (Subject{}).Detect(src)
	if strings.Join(clock, ",") != "0.15.0-dev.1234+abcdef12" || source != filepath.Join(repo, ".zigversion") {
		t.Fatalf("pin: %v %s", clock, source)
	}
	if _, err := (axiom.Subject{Name: Name, Shape: Subject{}.Shape()}).Clock(clock); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(repo, ".zigversion"), "master\n")
	if clock, _, _ = (Subject{}).Detect(src); strings.Join(clock, ",") != "0.14.0" {
		t.Fatalf("non-release pin is ignored: %v", clock)
	}
	if v := minimumVersion(".{ .minimum_zig_version = \"nope\" }"); v != "" {
		t.Fatalf("invalid minimum: %q", v)
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
	for _, want := range []string{"release-notes.html", "download/index.json", "propose_axiom", "append-only log", "Hard limit: never propose an axiom dated on or before 2023-08-01"} {
		if !strings.Contains(ins, want) {
			t.Fatalf("instructions lack %q", want)
		}
	}
	if task := s.UpdateTask(nil); !strings.Contains(task, "holds 0 axioms") || !strings.Contains(task, "after 2023-08-01, the global epoch") {
		t.Fatalf("empty task:\n%s", task)
	}
	existing := []axiom.Axiom{
		{Time: time.Date(2025, 3, 5, 0, 0, 0, 0, time.UTC), Clock: map[string]string{"version": "0.14.0"}, Statement: "labeled switch"},
		{Time: time.Date(2024, 6, 6, 0, 0, 0, 0, time.UTC), Clock: map[string]string{"version": "0.13.0"}, Statement: "older"},
	}
	if task := s.UpdateTask(existing); !strings.Contains(task, "newest Zig release it covers is 0.14.0") || !strings.Contains(task, "- [2025-03-05 0.14.0] labeled switch") {
		t.Fatalf("task:\n%s", task)
	}
	confirm := s.ConfirmTask(existing[0])
	if !strings.Contains(confirm, "Zig release: 0.14.0 (released 2025-03-05)") || !strings.Contains(confirm, "a correction giving the") {
		t.Fatalf("confirm:\n%s", confirm)
	}
	if !strings.Contains(s.ConfirmInstructions(), "distinguish two cases") || !strings.Contains(s.ConfirmInstructions(), "correct date (YYYY-MM-DD) and clock") {
		t.Fatal("confirm instructions")
	}
}
