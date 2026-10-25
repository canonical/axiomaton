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
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/mod/semver"

	"github.com/canonical/axiomaton/internal/axiom"
)

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestBatch(t *testing.T) {
	commits := []Commit{
		// Out of order on purpose; March, two commits in one month.
		{Hash: "c3c3c3c3c3c3c3c3c3c3", Time: at("2025-03-20T10:00:00Z"), GoVersion: "1.23"},
		{Hash: "c1c1c1c1c1c1c1c1c1c1", Time: at("2025-03-02T09:00:00Z"), GoVersion: "1.23"},
		// Same month, go directive bumped: a new release mid-month.
		{Hash: "c4c4c4c4c4c4c4c4c4c4", Time: at("2025-03-25T10:00:00Z"), GoVersion: "1.24"},
		// April: new calendar month.
		{Hash: "c5c5c5c5c5c5c5c5c5c5", Time: at("2025-04-03T10:00:00+02:00"), GoVersion: "1.24"},
		// July after a long gap.
		{Hash: "c6c6c6c6c6c6c6c6c6c6", Time: at("2025-07-15T10:00:00Z"), GoVersion: "1.24"},
		{Hash: "c7c7c7c7c7c7c7c7c7c7", Time: at("2025-07-16T10:00:00Z"), GoVersion: "1.24"},
	}
	rel := Batch(commits, "")
	var got []string
	for _, r := range rel {
		got = append(got, r.Version+" "+r.First[:2]+"-"+r.Last[:2]+" x"+strconv.Itoa(r.Commits)+" ("+r.Reason+")")
	}
	want := []string{
		"v0.0.0-20250320100000-c3c3c3c3c3c3 c1-c3 x2 (first commit)",
		"v0.0.0-20250325100000-c4c4c4c4c4c4 c4-c4 x1 (go directive changed to 1.24)",
		"v0.0.0-20250403080000-c5c5c5c5c5c5 c5-c5 x1 (new calendar month)",
		"v0.0.0-20250716100000-c7c7c7c7c7c7 c6-c7 x2 (more than a month since the previous commit)",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("batches:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// Pseudo-versions are valid clocks that order chronologically.
	shape := Subject{}.Shape()
	subj := axiom.Subject{Name: "go:x", Shape: shape}
	for i, r := range rel {
		clock, err := subj.Clock([]string{r.Version})
		if err != nil {
			t.Fatal(err)
		}
		if i > 0 && shape[0].Compare(clock["version"], strings.TrimPrefix(rel[i-1].Version, "v")) != axiom.Greater {
			t.Fatalf("%s should order after %s", r.Version, rel[i-1].Version)
		}
	}
	if rel[1].Time != at("2025-03-25T10:00:00Z") || rel[2].Time != at("2025-04-03T08:00:00Z") {
		t.Fatalf("times: %v %v", rel[1].Time, rel[2].Time)
	}

	// After a tag the pseudo-versions follow the go command's form.
	tagged := Batch(commits[:2], "v0.5.0")
	if len(tagged) != 1 || tagged[0].Version != "v0.5.1-0.20250320100000-c3c3c3c3c3c3" || semver.Compare(tagged[0].Version, "v0.5.0") <= 0 {
		t.Fatalf("tagged base: %+v", tagged)
	}
	if b := Batch(nil, ""); b != nil {
		t.Fatal("no commits, no releases")
	}
	// Unknown go versions do not split batches among themselves.
	same := Batch([]Commit{{Hash: "a1a1a1a1a1a1", Time: at("2025-03-01T00:00:00Z")}, {Hash: "b2b2b2b2b2b2", Time: at("2025-03-02T00:00:00Z")}}, "")
	if len(same) != 1 || same[0].Version != "v0.0.0-20250302000000-b2b2b2b2b2b2" {
		t.Fatalf("unknown go versions: %+v", same)
	}
}
