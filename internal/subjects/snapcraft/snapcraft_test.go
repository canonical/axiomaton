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

package snapcraft

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
	src := filepath.Join(repo, "src")
	os.MkdirAll(src, 0o755)
	write(t, filepath.Join(root, "snap", "snapcraft.yaml"), "name: outside\nbase: core20\n")

	if _, _, ok := (Subject{}).Detect(src); ok {
		t.Fatal("snapcraft.yaml above the repository must not count")
	}

	write(t, filepath.Join(repo, "snap", "snapcraft.yaml"), "name: demo\nbase: core24\nparts:\n  demo:\n    plugin: nil\n")
	clock, source, ok := (Subject{}).Detect(src)
	if !ok || strings.Join(clock, ",") != "24" || source != filepath.Join(repo, "snap", "snapcraft.yaml") {
		t.Fatalf("base: %v %s %v", clock, source, ok)
	}
	subj := axiom.Subject{Name: Name, Shape: Subject{}.Shape()}
	if c, err := subj.Clock(clock); err != nil || c["base"] != "24" || c["version"] != "" {
		t.Fatalf("clock: %v %v", c, err)
	}
	if c, err := subj.Clock([]string{"22", "8.3.0"}); err != nil || c["version"] != "8.3.0" {
		t.Fatalf("full clock: %v %v", c, err)
	}

	// build-base decides when present; bare with no build-base is unclocked.
	write(t, filepath.Join(repo, "snap", "snapcraft.yaml"), "name: demo\nbase: bare\nbuild-base: core22\n")
	if clock, _, _ = (Subject{}).Detect(src); strings.Join(clock, ",") != "22" {
		t.Fatalf("build-base: %v", clock)
	}
	write(t, filepath.Join(repo, "snap", "snapcraft.yaml"), "name: demo\nbase: bare\n")
	if clock, _, ok = (Subject{}).Detect(src); !ok || clock != nil {
		t.Fatalf("bare: %v %v", clock, ok)
	}
	write(t, filepath.Join(repo, "snap", "snapcraft.yaml"), "name: demo\nbase: core\n")
	if clock, _, _ = (Subject{}).Detect(src); strings.Join(clock, ",") != "16" {
		t.Fatalf("core: %v", clock)
	}

	// Other manifest locations.
	os.RemoveAll(filepath.Join(repo, "snap"))
	write(t, filepath.Join(repo, "build-aux", "snap", "snapcraft.yaml"), "base: core22\n")
	if clock, source, _ = (Subject{}).Detect(src); strings.Join(clock, ",") != "22" || !strings.HasSuffix(source, filepath.Join("build-aux", "snap", "snapcraft.yaml")) {
		t.Fatalf("build-aux: %v %s", clock, source)
	}
	write(t, filepath.Join(repo, "snapcraft.yaml"), "base: core24\n")
	if clock, _, _ = (Subject{}).Detect(src); strings.Join(clock, ",") != "24" {
		t.Fatalf("top-level manifest wins over build-aux: %v", clock)
	}
	// Unparseable YAML still detects the project.
	write(t, filepath.Join(repo, "snapcraft.yaml"), "base: [\n")
	if clock, _, ok = (Subject{}).Detect(src); !ok || clock != nil {
		t.Fatalf("bad yaml: %v %v", clock, ok)
	}

	// Ordering across the two dimensions.
	a22 := axiom.Axiom{Clock: map[string]string{"base": "22"}}
	a24 := axiom.Axiom{Clock: map[string]string{"base": "24", "version": "8.3.0"}}
	shape := Subject{}.Shape()
	if !axiom.Applies(a22, map[string]string{"base": "24"}, shape) || axiom.Applies(a24, map[string]string{"base": "22"}, shape) {
		t.Fatal("base ordering")
	}
	if !axiom.Applies(a24, map[string]string{"base": "24"}, shape) || axiom.Applies(a24, map[string]string{"base": "24", "version": "8.2.0"}, shape) {
		t.Fatal("version ordering")
	}
}

func TestPrompts(t *testing.T) {
	s := Subject{}
	if !s.RequiresVM() {
		t.Fatal("snapcraft agents need a VM")
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
	for _, want := range []string{"release-notes", "snap install snapcraft --classic", "lxd init", "propose_axiom", "append-only log", "Hard limit: never propose an axiom dated on or before 2023-08-01", "two values in\norder"} {
		if !strings.Contains(ins, want) {
			t.Fatalf("instructions lack %q", want)
		}
	}
	if task := s.UpdateTask(nil); !strings.Contains(task, "holds 0 axioms") || !strings.Contains(task, "after 2023-08-01, the global epoch") {
		t.Fatalf("empty task:\n%s", task)
	}
	existing := []axiom.Axiom{
		{Time: time.Date(2024, 7, 11, 0, 0, 0, 0, time.UTC), Clock: map[string]string{"base": "24", "version": "8.3.0"}, Statement: "platforms"},
		{Time: time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC), Clock: map[string]string{"base": "22"}, Statement: "craftctl"},
	}
	task := s.UpdateTask(existing)
	if !strings.Contains(task, "newest snapcraft release it covers is 8.3.0") || !strings.Contains(task, "- [2024-07-11 core24 8.3.0] platforms") || !strings.Contains(task, "- [2024-02-01 core22] craftctl") {
		t.Fatalf("task:\n%s", task)
	}
	confirm := s.ConfirmTask(existing[1])
	if !strings.Contains(confirm, "Base: core22 (clock value 22)") || !strings.Contains(confirm, `["24", "8.3.0"]`) || !strings.Contains(confirm, "Snapcraft release: any supporting the base (released 2024-02-01)") || !strings.Contains(confirm, "a correction giving the") {
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
		{[]string{"core26", "1.2.3"}, []string{"26", "1.2.3"}},
		{[]string{"core"}, []string{"16"}},
		{[]string{"26", "1.2.3"}, []string{"26", "1.2.3"}},
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
	if c, err := subj.Clock([]string{"core26", "1.2.3"}); err != nil || c["base"] != "26" || c["version"] != "1.2.3" {
		t.Fatalf("clock via normaliser: %v %v", c, err)
	}
	if _, err := subj.Clock([]string{"bare"}); err == nil {
		t.Fatal("an unrecognised base must still be rejected")
	}
}
