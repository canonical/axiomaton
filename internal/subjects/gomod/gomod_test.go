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

package gomod

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/canonical/axiomaton/internal/axiom"
)

const sampleMod = `module example.com/m

go 1.27.2

require (
	github.com/Azure/go-autorest v14.2.0+incompatible
	golang.org/x/mod v0.42.0
	golang.org/x/net v0.50.0
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/google/uuid v1.6.0 // indirect
	golang.org/x/text v0.0.0-20240101120000-abcdef123456 // indirect
)
`

func TestLookup(t *testing.T) {
	s, ok := Lookup("go:golang.org/x/net")
	if !ok || s.Module != "golang.org/x/net" || s.Name() != "go:golang.org/x/net" {
		t.Fatalf("lookup: %+v %v", s, ok)
	}
	if _, ok := Lookup("go:github.com/foo/bar/v2"); !ok {
		t.Fatal("major version suffix is a valid module path")
	}
	for _, bad := range []string{"go", "go:", "go:net", "go:golang.org/x/net ", "rust-crate-serde", "go:example.com/a b"} {
		if _, ok := Lookup(bad); ok {
			t.Fatalf("%q should not be a module subject", bad)
		}
	}
	if s.Shape()[0].Kind != axiom.KindSemver || s.Shape()[0].Name != "version" {
		t.Fatalf("shape: %+v", s.Shape())
	}
	if seed, err := s.Seed(); err != nil || seed != nil {
		t.Fatalf("seed: %v %v", seed, err)
	}
}

func TestDetect(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	os.MkdirAll(filepath.Join(repo, ".git"), 0o755)
	os.MkdirAll(filepath.Join(repo, "cmd", "x"), 0o755)
	if m := Detect(repo); len(m) != 0 {
		t.Fatalf("no go.mod: %+v", m)
	}
	os.WriteFile(filepath.Join(repo, "go.mod"), []byte(sampleMod), 0o644)

	members := Detect(filepath.Join(repo, "cmd", "x"))
	var names []string
	for _, m := range members {
		names = append(names, m.Subject.Name()+"@"+strings.Join(m.Clock, " "))
		if m.Source != filepath.Join(repo, "go.mod") {
			t.Fatalf("source: %s", m.Source)
		}
	}
	want := "go:github.com/Azure/go-autorest@v14.2.0+incompatible,go:golang.org/x/mod@v0.42.0,go:golang.org/x/net@v0.50.0,go:gopkg.in/yaml.v3@v3.0.1"
	if got := strings.Join(names, ","); got != want {
		t.Fatalf("direct requirements:\n got %s\nwant %s", got, want)
	}
	// Every detected clock parses under the subject's shape.
	for _, m := range members {
		if _, err := (axiom.Subject{Name: m.Subject.Name(), Shape: m.Subject.Shape()}).Clock(m.Clock); err != nil {
			t.Fatal(err)
		}
	}

	// A named member reads its own version, including an indirect one.
	s, _ := Lookup("go:golang.org/x/text")
	clock, src, ok := s.Detect(repo)
	if !ok || clock[0] != "v0.0.0-20240101120000-abcdef123456" || src != filepath.Join(repo, "go.mod") {
		t.Fatalf("member detect: %v %s %v", clock, src, ok)
	}
	if _, _, ok := (Subject{Module: "example.com/absent"}).Detect(repo); ok {
		t.Fatal("unrequired module must not detect")
	}
	if _, _, ok := s.Detect(root); ok {
		t.Fatal("no go.mod above the repository")
	}
}

func TestPrompts(t *testing.T) {
	s := Subject{Module: "github.com/Azure/azure-sdk-for-go"}
	ins := s.UpdateInstructions()
	for _, want := range []string{"github.com/Azure/azure-sdk-for-go", "proxy.golang.org/github.com/!azure/azure-sdk-for-go/@v/list", "propose_axiom", "append-only log"} {
		if !strings.Contains(ins, want) {
			t.Fatalf("instructions lack %q:\n%s", want, ins)
		}
	}
	empty := s.UpdateTask(nil)
	if !strings.Contains(empty, "holds 0 axioms") || !strings.Contains(empty, "on or after 2023-08-01") || strings.Contains(empty, "newest release it covers") {
		t.Fatalf("empty task:\n%s", empty)
	}
	existing := []axiom.Axiom{
		{Time: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), Clock: map[string]string{"version": "0.9.0"}, Statement: "nine"},
		{Time: time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC), Clock: map[string]string{"version": "0.10.0"}, Statement: "ten"},
	}
	task := s.UpdateTask(existing)
	if !strings.Contains(task, "newest release it covers is v0.10.0") || !strings.Contains(task, "- [2025-06-01 v0.10.0] ten") || strings.Contains(task, "The log is empty") {
		t.Fatalf("task:\n%s", task)
	}
	confirm := s.ConfirmTask(axiom.Axiom{Time: existing[1].Time, Clock: existing[1].Clock, Statement: "ten", Source: "https://x"})
	if !strings.Contains(confirm, "Release: v0.10.0 (released 2025-06-01)") || !strings.Contains(confirm, "submit_answer") {
		t.Fatalf("confirm task:\n%s", confirm)
	}
	if !strings.Contains(s.ConfirmInstructions(), "pkg.go.dev/github.com/Azure/azure-sdk-for-go@<version>") {
		t.Fatal("confirm instructions")
	}
}
