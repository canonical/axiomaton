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

package axiom

import (
	"strings"
	"testing"
	"time"
)

func date(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestCompareKinds(t *testing.T) {
	tests := []struct {
		kind Kind
		a, b string
		want Ordering
	}{
		{KindSemver, "1.26.1", "1.27.0", Less},
		{KindSemver, "v1.26.1", "1.26.1", Equal},
		{KindSemver, "1.10.0", "1.9.0", Greater},
		{KindGoVersion, "1.26", "1.26.1", Less},
		{KindGoVersion, "1.27rc1", "1.27.0", Less},
		{KindGoVersion, "1.27.0", "1.27", Equal},
		{KindGoVersion, "1.26.9", "1.27", Less},
		{KindNumber, "10", "9", Greater},
		{KindNumber, "1.5", "1.50", Equal},
		{KindString, "a", "b", Less},
	}
	for _, tc := range tests {
		d := ClockShape{Name: "x", Kind: tc.kind}
		if got := d.Compare(tc.a, tc.b); got != tc.want {
			t.Errorf("%s compare(%q,%q) = %v, want %v", tc.kind, tc.a, tc.b, got, tc.want)
		}
	}
}

func TestParse(t *testing.T) {
	d := ClockShape{Name: "version", Kind: KindGoVersion}
	if v, err := d.Parse("go1.26.1"); err != nil || v != "1.26.1" {
		t.Fatalf("Parse go1.26.1 = %q, %v", v, err)
	}
	if _, err := d.Parse("banana"); err == nil {
		t.Fatal("expected error for banana")
	}
	s := ClockShape{Name: "version", Kind: KindSemver}
	if v, err := s.Parse("v2.0.0"); err != nil || v != "2.0.0" {
		t.Fatalf("Parse v2.0.0 = %q, %v", v, err)
	}
}

func TestGit(t *testing.T) {
	old := GitAncestor
	defer func() { GitAncestor = old }()
	GitAncestor = func(repo, a, b string) (bool, error) {
		return a == "aaa" && b == "bbb", nil
	}
	d := ClockShape{Name: "commit", Kind: KindGit, Repo: "/r"}
	if got := d.Compare("aaa", "bbb"); got != Less {
		t.Fatalf("aaa<bbb got %v", got)
	}
	if got := d.Compare("bbb", "aaa"); got != Greater {
		t.Fatalf("bbb>aaa got %v", got)
	}
	if got := d.Compare("ccc", "ddd"); got != Incomparable {
		t.Fatalf("ccc?ddd got %v", got)
	}
	if got := d.Compare("ccc", "ccc"); got != Equal {
		t.Fatalf("ccc=ccc got %v", got)
	}
}

func TestNewerAndApplies(t *testing.T) {
	shape := []ClockShape{{Name: "version", Kind: KindGoVersion}}
	a := Axiom{Subject: "go", Time: date("2026-02-10"), Clock: map[string]string{"version": "1.26.0"}, Statement: "a"}
	b := Axiom{Subject: "go", Time: date("2026-08-19"), Clock: map[string]string{"version": "1.27.0"}, Statement: "b"}
	c := Axiom{Subject: "go", Time: date("2026-08-19"), Clock: map[string]string{"version": "1.26.5"}, Statement: "c"}
	for _, x := range []*Axiom{&a, &b, &c} {
		x.Normalise()
	}
	if !Newer(b, a, shape) || Newer(a, b, shape) {
		t.Fatal("b should be newer than a")
	}
	if !Newer(b, c, shape) {
		t.Fatal("same time: b (1.27.0) should be newer than c (1.26.5)")
	}
	q := map[string]string{"version": "1.26.1"}
	if !Applies(a, q, shape) {
		t.Fatal("1.26.0 axiom should apply to 1.26.1")
	}
	if Applies(b, q, shape) {
		t.Fatal("1.27.0 axiom should not apply to 1.26.1")
	}
	if !Applies(b, nil, shape) {
		t.Fatal("unconstrained query should accept everything")
	}
	noClock := Axiom{Subject: "go", Time: date("2020-01-01"), Statement: "x"}
	if !Applies(noClock, q, shape) {
		t.Fatal("axiom without sub-clock should apply")
	}
	xs := []Axiom{b, c, a}
	Sort(xs, shape)
	if xs[0].ID != a.ID || xs[1].ID != c.ID || xs[2].ID != b.ID {
		t.Fatalf("bad sort order: %v", []string{xs[0].Statement, xs[1].Statement, xs[2].Statement})
	}
}

func TestIDStable(t *testing.T) {
	a := Axiom{Subject: "s", Time: date("2026-01-01"), Clock: map[string]string{"x": "1", "y": "2"}, Statement: " hello "}
	b := Axiom{Subject: "s", Time: date("2026-01-01"), Clock: map[string]string{"y": "2", "x": "1"}, Statement: "hello"}
	a.Normalise()
	b.Normalise()
	if a.ID != b.ID {
		t.Fatalf("ids differ: %s %s", a.ID, b.ID)
	}
	if a.ID == "" || len(a.ID) != 16 {
		t.Fatalf("bad id %q", a.ID)
	}
}

func TestRegistry(t *testing.T) {
	r := NewRegistry()
	if err := r.Define("go", []ClockShape{{Name: "version", Kind: KindGoVersion}}); err != nil {
		t.Fatal(err)
	}
	if err := r.Define("bad", []ClockShape{{Name: "c", Kind: KindGit}}); err == nil {
		t.Fatal("git without repo should fail")
	}
	s, inferred := r.Resolve("go", []string{"1.26.1"})
	if inferred {
		t.Fatal("go should not be inferred")
	}
	clock, err := s.Clock([]string{"1.26.1"})
	if err != nil || clock["version"] != "1.26.1" {
		t.Fatalf("clock = %v, %v", clock, err)
	}
	if _, err := s.Clock([]string{"1", "2"}); err == nil {
		t.Fatal("too many values should fail")
	}
	n := Subject{Name: "n", Shape: []ClockShape{{Name: "base", Kind: KindNumber}}, Normalise: func(v []string) []string {
		return []string{strings.TrimPrefix(v[0], "core")}
	}}
	if clock, err := n.Clock([]string{"core24"}); err != nil || clock["base"] != "24" {
		t.Fatalf("normalised clock = %v, %v", clock, err)
	}
	if _, err := n.Clock([]string{"corex"}); err == nil {
		t.Fatal("normalising must not bypass validation")
	}
	s2, inferred := r.Resolve("other", []string{"2.3.4", "7"})
	if !inferred || s2.Shape[0].Kind != KindSemver || s2.Shape[1].Kind != KindNumber {
		t.Fatalf("inferred shape = %+v (%v)", s2.Shape, inferred)
	}
}
