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

package subjects

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSeeds(t *testing.T) {
	seeds, err := Seeds()
	if err != nil {
		t.Fatal(err)
	}
	// One seed per enumerable built-in: the fixed subjects in name order,
	// then the shipped golang.org/x modules, each holding only its own
	// subject's axioms. Seeds other than go may be empty until generated
	// with ax update.
	var names []string
	for _, b := range Builtins() {
		names = append(names, b.Name())
	}
	goSeed := -1
	for i, n := range names {
		if n == "go" {
			goSeed = i
		}
	}
	if len(seeds) != len(names) || !strings.HasPrefix(strings.Join(names, ","), "charmcraft,go,python,rockcraft,rust,snapcraft,zig,go:golang.org/x/crypto,") || !strings.Contains(strings.Join(names, ","), ",go:golang.org/x/net,") || goSeed < 0 || len(seeds[goSeed]) < 20 {
		t.Fatalf("unexpected seeds: %d for %v", len(seeds), names)
	}
	for i, seed := range seeds {
		for _, a := range seed {
			if a.Subject != names[i] || a.Clock["version"] == "" {
				t.Fatalf("bad %s seed axiom %+v", names[i], a)
			}
		}
	}
	if shape, ok := Shapes()["go:golang.org/x/net"]; !ok || shape[0].Kind != "semver" {
		t.Fatal("shipped module should be registered up front")
	}
	b, ok := Lookup("go")
	if !ok || b.Shape()[0].Name != "version" {
		t.Fatal("go lookup")
	}
	task := b.UpdateTask(seeds[goSeed])
	if !strings.Contains(task, "newest Go release it covers is 1.27.0") {
		t.Fatalf("task: %s", task)
	}
}

func TestFamilies(t *testing.T) {
	if got := strings.Join(Names(), ","); got != "charmcraft,go,python,rockcraft,rust,snapcraft,zig,go:<module path>" {
		t.Fatalf("names: %s", got)
	}
	b, ok := Lookup("go:golang.org/x/net")
	if !ok || b.Name() != "go:golang.org/x/net" || b.Shape()[0].Kind != "semver" {
		t.Fatalf("family lookup: %v %v", b, ok)
	}
	if _, ok := Lookup("go:"); ok {
		t.Fatal("empty module path must not resolve")
	}
	if _, ok := Lookup("go:<module path>"); ok {
		t.Fatal("the pattern itself is not a subject")
	}
	if _, ok := Shapes()["go:example.com/unshipped"]; ok {
		t.Fatal("unshipped family members are not enumerable")
	}
}

func TestAxiomSubject(t *testing.T) {
	snap, _ := Lookup("snapcraft")
	s := AxiomSubject(snap)
	if s.Name != "snapcraft" || len(s.Shape) != 2 || s.Normalise == nil {
		t.Fatalf("snapcraft subject: %+v", s)
	}
	if c, err := s.Clock([]string{"core26", "8.11.0"}); err != nil || c["base"] != "26" || c["version"] != "8.11.0" {
		t.Fatalf("core26 should normalise to 26: %v %v", c, err)
	}
	for _, name := range []string{"charmcraft", "rockcraft"} {
		b, _ := Lookup(name)
		if c, err := AxiomSubject(b).Clock([]string{"ubuntu@24.04"}); err != nil || c["base"] != "24.04" {
			t.Fatalf("%s ubuntu@24.04 should normalise to 24.04: %v %v", name, c, err)
		}
	}
	g, _ := Lookup("go")
	if s := AxiomSubject(g); s.Normalise != nil {
		t.Fatal("go has no clock normaliser")
	}
}

func TestDetect(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	os.MkdirAll(filepath.Join(repo, ".git"), 0o755)
	os.MkdirAll(filepath.Join(repo, "cmd", "x"), 0o755)
	os.WriteFile(filepath.Join(root, "go.mod"), []byte("module outside\n\ngo 1.20\n"), 0o644)

	if d := Detect(filepath.Join(repo, "cmd", "x")); len(d) != 0 {
		t.Fatalf("go.mod above the repository must not count: %+v", d)
	}
	os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module example.com/m\n\ngo 1.27.2\n\ntoolchain go1.27.3\n"), 0o644)
	d := Detect(filepath.Join(repo, "cmd", "x"))
	if len(d) != 1 || d[0].Subject.Name() != "go" || strings.Join(d[0].Clock, ",") != "1.27.2" || d[0].Source != filepath.Join(repo, "go.mod") {
		t.Fatalf("detect: %+v", d)
	}
	os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module example.com/m\n\ngo 1.27.2\n\nrequire (\n\tgolang.org/x/net v0.50.0\n\tgithub.com/google/uuid v1.6.0 // indirect\n)\n"), 0o644)
	d = Detect(repo)
	if len(d) != 2 || d[0].Subject.Name() != "go" || d[1].Subject.Name() != "go:golang.org/x/net" || d[1].Clock[0] != "v0.50.0" {
		t.Fatalf("detect with requirements: %+v", d)
	}
	os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module example.com/m\ngo 1.26\n"), 0o644)
	if d := Detect(repo); len(d) != 1 || d[0].Clock[0] != "1.26" {
		t.Fatalf("bare go version: %+v", d)
	}
	os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module example.com/m\n"), 0o644)
	if d := Detect(repo); len(d) != 0 {
		t.Fatalf("go.mod without a go directive should not detect: %+v", d)
	}
	os.WriteFile(filepath.Join(repo, "Cargo.toml"), []byte("[package]\nname = \"x\"\nrust-version = \"1.80\"\n"), 0o644)
	if d := Detect(repo); len(d) != 1 || d[0].Subject.Name() != "rust" || d[0].Clock[0] != "1.80" {
		t.Fatalf("rust: %+v", d)
	}
	if b, ok := Lookup("rust"); !ok || b.Shape()[0].Kind != "semver" {
		t.Fatal("rust lookup")
	}
	os.WriteFile(filepath.Join(repo, "pyproject.toml"), []byte("[project]\nrequires-python = \">=3.12\"\n"), 0o644)
	d = Detect(repo)
	if len(d) != 2 || d[0].Subject.Name() != "python" || d[0].Clock[0] != "3.12" || d[1].Subject.Name() != "rust" {
		t.Fatalf("python and rust: %+v", d)
	}
	os.WriteFile(filepath.Join(repo, "build.zig.zon"), []byte(".{ .minimum_zig_version = \"0.14.0\" }\n"), 0o644)
	d = Detect(repo)
	if len(d) != 3 || d[2].Subject.Name() != "zig" || d[2].Clock[0] != "0.14.0" {
		t.Fatalf("zig: %+v", d)
	}
	os.MkdirAll(filepath.Join(repo, "snap"), 0o755)
	os.WriteFile(filepath.Join(repo, "snap", "snapcraft.yaml"), []byte("name: x\nbase: core24\n"), 0o644)
	d = Detect(repo)
	if len(d) != 4 || d[2].Subject.Name() != "snapcraft" || d[2].Clock[0] != "24" || !RequiresVM(d[2].Subject) {
		t.Fatalf("snapcraft: %+v", d)
	}
	os.WriteFile(filepath.Join(repo, "charmcraft.yaml"), []byte("type: charm\nbase: ubuntu@24.04\n"), 0o644)
	d = Detect(repo)
	if len(d) != 5 || d[0].Subject.Name() != "charmcraft" || d[0].Clock[0] != "24.04" || !RequiresVM(d[0].Subject) {
		t.Fatalf("charmcraft: %+v", d)
	}
	os.WriteFile(filepath.Join(repo, "rockcraft.yaml"), []byte("name: x\nbase: ubuntu@22.04\n"), 0o644)
	d = Detect(repo)
	if len(d) != 6 || d[2].Subject.Name() != "rockcraft" || d[2].Clock[0] != "22.04" || !RequiresVM(d[2].Subject) {
		t.Fatalf("rockcraft: %+v", d)
	}
}
