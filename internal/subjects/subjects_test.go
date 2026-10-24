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
