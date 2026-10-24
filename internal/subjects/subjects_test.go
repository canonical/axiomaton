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
	if len(seeds) != 1 || len(seeds[0]) < 20 {
		t.Fatalf("unexpected seeds: %d", len(seeds))
	}
	b, ok := Lookup("golang")
	if !ok || b.Shape()[0].Name != "version" {
		t.Fatal("golang lookup")
	}
	for _, a := range seeds[0] {
		if a.Subject != "golang" || a.Clock["version"] == "" {
			t.Fatalf("bad seed axiom %+v", a)
		}
	}
	task := b.UpdateTask(seeds[0])
	if !strings.Contains(task, "newest Go release it covers is 1.27.0") {
		t.Fatalf("task: %s", task)
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
	if len(d) != 1 || d[0].Subject.Name() != "golang" || strings.Join(d[0].Clock, ",") != "1.27.2" || d[0].Source != filepath.Join(repo, "go.mod") {
		t.Fatalf("detect: %+v", d)
	}
	os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module example.com/m\ngo 1.26\n"), 0o644)
	if d := Detect(repo); len(d) != 1 || d[0].Clock[0] != "1.26" {
		t.Fatalf("bare go version: %+v", d)
	}
	os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module example.com/m\n"), 0o644)
	if d := Detect(repo); len(d) != 0 {
		t.Fatalf("go.mod without a go directive should not detect: %+v", d)
	}
}
