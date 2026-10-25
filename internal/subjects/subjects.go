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

// Package subjects registers the built-in subjects that ship with ax.
package subjects

import (
	"sort"
	"strings"

	"github.com/canonical/axiomaton/internal/axiom"
	"github.com/canonical/axiomaton/internal/subjects/golang"
	"github.com/canonical/axiomaton/internal/subjects/gomod"
	"github.com/canonical/axiomaton/internal/subjects/python"
	"github.com/canonical/axiomaton/internal/subjects/rust"
)

// Builtin is a subject ax knows how to update by itself.
type Builtin interface {
	Name() string
	Shape() []axiom.ClockShape
	// Seed returns the axioms embedded in the binary.
	Seed() ([]axiom.Axiom, error)
	// UpdateInstructions is the system prompt for the update agent.
	UpdateInstructions() string
	// UpdateTask is the user prompt for the update agent given what the log
	// already knows.
	UpdateTask(existing []axiom.Axiom) string
	// ConfirmInstructions is the system prompt for a confirmation agent.
	ConfirmInstructions() string
	// ConfirmTask is the user prompt asking to verify one proposed axiom.
	ConfirmTask(a axiom.Axiom) string
	// Detect reports whether the project at dir uses this subject and, if
	// so, the project's current clock and where it was read from.
	Detect(dir string) (clock []string, source string, ok bool)
}

// Detected is a built-in subject found in a project directory.
type Detected struct {
	Subject Builtin
	Clock   []string
	// Source is the file the clock was read from.
	Source string
}

// Family is an open-ended set of built-in subjects sharing a name prefix,
// such as one subject per Go module dependency. Its members cannot be
// enumerated, so they have no seeds and are only registered when named or
// detected.
type Family interface {
	// Prefix starts every member's name.
	Prefix() string
	// Pattern describes member names in help text, e.g. go:<module>.
	Pattern() string
	// Lookup builds the member with the given full name.
	Lookup(name string) (Builtin, bool)
	// Detect lists the members the project at dir uses.
	Detect(dir string) []Detected
}

// Detect runs every built-in subject's and family's detection against dir.
func Detect(dir string) []Detected {
	var out []Detected
	for _, n := range staticNames() {
		if clock, source, ok := all[n].Detect(dir); ok {
			out = append(out, Detected{Subject: all[n], Clock: clock, Source: source})
		}
	}
	for _, f := range families {
		out = append(out, f.Detect(dir)...)
	}
	return out
}

var all = map[string]Builtin{
	golang.Name: golang.Subject{},
	python.Name: python.Subject{},
	rust.Name:   rust.Subject{},
}

var families = []Family{gomodFamily{}}

// gomodFamily adapts the gomod package to the Family interface.
type gomodFamily struct{}

func (gomodFamily) Prefix() string  { return gomod.Prefix }
func (gomodFamily) Pattern() string { return gomod.Pattern }

func (gomodFamily) Lookup(name string) (Builtin, bool) {
	s, ok := gomod.Lookup(name)
	if !ok {
		return nil, false
	}
	return s, true
}

func (gomodFamily) Detect(dir string) []Detected {
	var out []Detected
	for _, m := range gomod.Detect(dir) {
		out = append(out, Detected{Subject: m.Subject, Clock: m.Clock, Source: m.Source})
	}
	return out
}

// Lookup returns a built-in subject by name, including family members such
// as go:golang.org/x/net.
func Lookup(name string) (Builtin, bool) {
	if b, ok := all[name]; ok {
		return b, true
	}
	for _, f := range families {
		if strings.HasPrefix(name, f.Prefix()) {
			return f.Lookup(name)
		}
	}
	return nil, false
}

// Names lists the built-in subjects, with each family shown as its name
// pattern.
func Names() []string {
	names := staticNames()
	for _, f := range families {
		names = append(names, f.Pattern())
	}
	return names
}

func staticNames() []string {
	names := make([]string, 0, len(all))
	for n := range all {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Shapes returns the clock shape of every enumerable built-in subject.
// Family members are not included; use Lookup for those.
func Shapes() map[string][]axiom.ClockShape {
	out := map[string][]axiom.ClockShape{}
	for n, b := range all {
		out[n] = b.Shape()
	}
	return out
}

// Seeds returns the embedded logs of every built-in subject.
func Seeds() ([][]axiom.Axiom, error) {
	var out [][]axiom.Axiom
	for _, n := range staticNames() {
		seed, err := all[n].Seed()
		if err != nil {
			return nil, err
		}
		out = append(out, seed)
	}
	return out, nil
}
