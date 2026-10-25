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

package rust

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

func TestDetect(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	os.MkdirAll(filepath.Join(repo, ".git"), 0o755)
	crate := filepath.Join(repo, "crates", "foo")
	src := filepath.Join(crate, "src")
	os.MkdirAll(src, 0o755)
	write(t, filepath.Join(root, "Cargo.toml"), "[package]\nname = \"outside\"\nrust-version = \"1.60\"\n")

	if _, _, ok := (Subject{}).Detect(src); ok {
		t.Fatal("Cargo.toml above the repository must not count")
	}

	// A crate with no pin at all is detected without a clock.
	write(t, filepath.Join(crate, "Cargo.toml"), "[package]\nname = \"foo\"\nversion = \"0.1.0\" # the crate, not rust\n\n[dependencies]\nserde = \"1\"\n")
	clock, source, ok := (Subject{}).Detect(src)
	if !ok || clock != nil || source != filepath.Join(crate, "Cargo.toml") {
		t.Fatalf("unpinned: %v %s %v", clock, source, ok)
	}

	// package.rust-version is the clock.
	write(t, filepath.Join(crate, "Cargo.toml"), "[package]\nname = \"foo\"\nrust-version = \"1.70\" # msrv\n\n[dependencies]\nserde = { version = \"1\", features = [\"derive\"] }\n")
	clock, source, ok = (Subject{}).Detect(src)
	if !ok || strings.Join(clock, ",") != "1.70" || source != filepath.Join(crate, "Cargo.toml") {
		t.Fatalf("rust-version: %v %s %v", clock, source, ok)
	}
	if _, err := (axiom.Subject{Name: Name, Shape: Subject{}.Shape()}).Clock(clock); err != nil {
		t.Fatal(err)
	}

	// Workspace inheritance follows to the workspace root, both spellings.
	write(t, filepath.Join(repo, "Cargo.toml"), "[workspace]\nmembers = [\"crates/*\"]\n\n[workspace.package]\nedition = \"2024\"\nrust-version = \"1.85.0\"\n")
	for _, spelling := range []string{"rust-version.workspace = true", "rust-version = { workspace = true }"} {
		write(t, filepath.Join(crate, "Cargo.toml"), "[package]\nname = \"foo\"\n"+spelling+"\n")
		clock, source, ok = (Subject{}).Detect(src)
		if !ok || strings.Join(clock, ",") != "1.85.0" || source != filepath.Join(repo, "Cargo.toml") {
			t.Fatalf("%s: %v %s %v", spelling, clock, source, ok)
		}
	}

	// A pinned toolchain wins over rust-version, but only when it names a
	// release.
	write(t, filepath.Join(repo, "rust-toolchain.toml"), "# pin\n[toolchain]\nchannel = \"stable\"\ncomponents = [\"rustfmt\"]\n")
	if clock, _, _ = (Subject{}).Detect(src); strings.Join(clock, ",") != "1.85.0" {
		t.Fatalf("stable channel is not a release: %v", clock)
	}
	write(t, filepath.Join(repo, "rust-toolchain.toml"), "[toolchain]\nchannel = \"1.82.0\"\n")
	clock, source, _ = (Subject{}).Detect(src)
	if strings.Join(clock, ",") != "1.82.0" || source != filepath.Join(repo, "rust-toolchain.toml") {
		t.Fatalf("toolchain pin: %v %s", clock, source)
	}
	os.Remove(filepath.Join(repo, "rust-toolchain.toml"))
	write(t, filepath.Join(crate, "rust-toolchain"), "1.79\n")
	clock, source, _ = (Subject{}).Detect(src)
	if strings.Join(clock, ",") != "1.79" || source != filepath.Join(crate, "rust-toolchain") {
		t.Fatalf("legacy toolchain file: %v %s", clock, source)
	}
	write(t, filepath.Join(crate, "rust-toolchain"), "nightly-2026-01-01\n")
	if clock, _, _ = (Subject{}).Detect(src); strings.Join(clock, ",") != "1.85.0" {
		t.Fatalf("nightly is not a release: %v", clock)
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
	for _, want := range []string{"RELEASES.md", "propose_axiom", "append-only log", "sh.rustup.rs"} {
		if !strings.Contains(ins, want) {
			t.Fatalf("instructions lack %q", want)
		}
	}
	if task := s.UpdateTask(nil); !strings.Contains(task, "holds 0 axioms") || !strings.Contains(task, "after 2023-08-01, the global epoch") {
		t.Fatalf("empty task:\n%s", task)
	}
	existing := []axiom.Axiom{
		{Time: time.Date(2024, 10, 17, 0, 0, 0, 0, time.UTC), Clock: map[string]string{"version": "1.82.0"}, Statement: "raw"},
		{Time: time.Date(2024, 9, 5, 0, 0, 0, 0, time.UTC), Clock: map[string]string{"version": "1.81.0"}, Statement: "expect"},
	}
	if task := s.UpdateTask(existing); !strings.Contains(task, "newest Rust release it covers is 1.82.0") || !strings.Contains(task, "- [2024-10-17 1.82.0] raw") {
		t.Fatalf("task:\n%s", task)
	}
	confirm := s.ConfirmTask(existing[0])
	if !strings.Contains(confirm, "Rust release: 1.82.0 (released 2024-10-17)") || !strings.Contains(confirm, "a correction giving the") {
		t.Fatalf("confirm:\n%s", confirm)
	}
	if !strings.Contains(s.ConfirmInstructions(), "distinguish two cases") || !strings.Contains(s.ConfirmInstructions(), "cargo +<version>") {
		t.Fatal("confirm instructions")
	}
}
