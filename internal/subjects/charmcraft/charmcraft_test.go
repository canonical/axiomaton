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

package charmcraft

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

const oldFormat = `type: charm
bases:
  - build-on:
      - name: ubuntu
        channel: "22.04"
    run-on:
      - name: ubuntu
        channel: "22.04"
      - name: ubuntu
        channel: "24.04"
parts:
  charm:
    plugin: charm
`

func TestDetect(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	os.MkdirAll(filepath.Join(repo, ".git"), 0o755)
	src := filepath.Join(repo, "src")
	os.MkdirAll(src, 0o755)
	write(t, filepath.Join(root, "charmcraft.yaml"), "type: charm\nbase: ubuntu@20.04\n")

	if _, _, ok := (Subject{}).Detect(src); ok {
		t.Fatal("charmcraft.yaml above the repository must not count")
	}

	manifest := filepath.Join(repo, "charmcraft.yaml")
	write(t, manifest, "type: charm\nname: demo\nbase: ubuntu@24.04\nplatforms:\n  amd64:\n")
	clock, source, ok := (Subject{}).Detect(src)
	if !ok || strings.Join(clock, ",") != "24.04" || source != manifest {
		t.Fatalf("base: %v %s %v", clock, source, ok)
	}
	subj := axiom.Subject{Name: Name, Shape: Subject{}.Shape()}
	if c, err := subj.Clock(clock); err != nil || c["base"] != "24.04" {
		t.Fatalf("clock: %v %v", c, err)
	}
	if c, err := subj.Clock([]string{"22.04", "3.4.0"}); err != nil || c["version"] != "3.4.0" {
		t.Fatalf("full clock: %v %v", c, err)
	}

	write(t, manifest, "type: charm\nbase: ubuntu@22.04\nbuild-base: ubuntu@24.04\n")
	if clock, _, _ = (Subject{}).Detect(src); strings.Join(clock, ",") != "24.04" {
		t.Fatalf("build-base wins: %v", clock)
	}
	write(t, manifest, "type: charm\nbase: ubuntu@devel\n")
	if clock, _, ok = (Subject{}).Detect(src); !ok || clock != nil {
		t.Fatalf("devel: %v %v", clock, ok)
	}
	write(t, manifest, oldFormat)
	if clock, _, _ = (Subject{}).Detect(src); strings.Join(clock, ",") != "22.04" {
		t.Fatalf("bases build-on: %v", clock)
	}
	write(t, manifest, "type: bundle\n")
	if clock, _, ok = (Subject{}).Detect(src); !ok || clock != nil {
		t.Fatalf("bundle: %v %v", clock, ok)
	}
	write(t, manifest, "base: [\n")
	if clock, _, ok = (Subject{}).Detect(src); !ok || clock != nil {
		t.Fatalf("bad yaml: %v %v", clock, ok)
	}

	// Ubuntu release numbers order correctly as numbers, including .10.
	shape := Subject{}.Shape()
	if shape[0].Compare("24.10", "24.04") != axiom.Greater || shape[0].Compare("22.04", "24.04") != axiom.Less {
		t.Fatal("base ordering")
	}
	a22 := axiom.Axiom{Clock: map[string]string{"base": "22.04"}}
	a24 := axiom.Axiom{Clock: map[string]string{"base": "24.04", "version": "3.4.0"}}
	if !axiom.Applies(a22, map[string]string{"base": "24.04"}, shape) || axiom.Applies(a24, map[string]string{"base": "22.04"}, shape) || axiom.Applies(a24, map[string]string{"base": "24.04", "version": "3.3.0"}, shape) {
		t.Fatal("applies")
	}
	for in, want := range map[string]string{"ubuntu@24.04": "24.04", "22.04": "22.04", "ubuntu@24.04/stable": "24.04", "centos@7": "", "bare": "", "": ""} {
		if got := baseNumber(in); got != want {
			t.Errorf("baseNumber(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPrompts(t *testing.T) {
	s := Subject{}
	if !s.RequiresVM() {
		t.Fatal("charmcraft agents need a VM")
	}
	seed, err := s.Seed()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range seed {
		if a.Subject != Name || a.Clock["base"] == "" {
			t.Fatalf("bad seed axiom: %+v", a)
		}
	}
	ins := s.UpdateInstructions()
	for _, want := range []string{"release-notes", "snap install charmcraft --classic", "lxd init", "propose_axiom", "append-only log", "fetch-lib", "Hard limit: never propose an axiom dated on or before 2023-08-01"} {
		if !strings.Contains(ins, want) {
			t.Fatalf("instructions lack %q", want)
		}
	}
	if task := s.UpdateTask(nil); !strings.Contains(task, "holds 0 axioms") || !strings.Contains(task, "after 2023-08-01, the global epoch") {
		t.Fatalf("empty task:\n%s", task)
	}
	existing := []axiom.Axiom{
		{Time: time.Date(2024, 9, 10, 0, 0, 0, 0, time.UTC), Clock: map[string]string{"base": "24.04", "version": "3.2.0"}, Statement: "platforms"},
		{Time: time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC), Clock: map[string]string{"base": "22.04"}, Statement: "libs"},
	}
	task := s.UpdateTask(existing)
	if !strings.Contains(task, "newest charmcraft release it covers is 3.2.0") || !strings.Contains(task, "- [2024-09-10 ubuntu@24.04 3.2.0] platforms") || !strings.Contains(task, "- [2024-02-01 ubuntu@22.04] libs") {
		t.Fatalf("task:\n%s", task)
	}
	confirm := s.ConfirmTask(existing[1])
	if !strings.Contains(confirm, "Base: ubuntu@22.04 (clock value 22.04)") || !strings.Contains(confirm, `["24.04", "1.5.0"]`) || !strings.Contains(confirm, "Charmcraft release: any supporting the base (released 2024-02-01)") || !strings.Contains(confirm, "a correction giving the") {
		t.Fatalf("confirm:\n%s", confirm)
	}
	if !strings.Contains(s.ConfirmInstructions(), "then the release) instead") {
		t.Fatal("confirm instructions must spell out the base number form")
	}
	if !strings.Contains(s.ConfirmInstructions(), "distinguish two cases") || !strings.Contains(s.ConfirmInstructions(), "give a split instead") {
		t.Fatal("confirm instructions")
	}
}

func TestNormaliseClock(t *testing.T) {
	s := Subject{}
	for _, tc := range []struct{ in, want []string }{
		{[]string{"ubuntu@24.04", "1.2.3"}, []string{"24.04", "1.2.3"}},
		{[]string{"ubuntu@22.04/stable"}, []string{"22.04"}},
		{[]string{"24.04", "1.2.3"}, []string{"24.04", "1.2.3"}},
		{[]string{"bare"}, []string{"bare"}},
		{nil, nil},
	} {
		in := append([]string(nil), tc.in...)
		if got := s.NormaliseClock(tc.in); strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Fatalf("NormaliseClock(%v) = %v, want %v", tc.in, got, tc.want)
		}
		if strings.Join(in, ",") != strings.Join(tc.in, ",") {
			t.Fatalf("NormaliseClock(%v) changed its argument", in)
		}
	}
	subj := axiom.Subject{Name: Name, Shape: s.Shape(), Normalise: s.NormaliseClock}
	if c, err := subj.Clock([]string{"ubuntu@24.04", "1.2.3"}); err != nil || c["base"] != "24.04" || c["version"] != "1.2.3" {
		t.Fatalf("clock via normaliser: %v %v", c, err)
	}
	if _, err := subj.Clock([]string{"bare"}); err == nil {
		t.Fatal("an unrecognised base must still be rejected")
	}
}
