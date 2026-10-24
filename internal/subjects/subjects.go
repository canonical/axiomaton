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

	"github.com/canonical/axiomaton/internal/axiom"
	"github.com/canonical/axiomaton/internal/subjects/golang"
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

// Detect runs every built-in subject's detection against dir.
func Detect(dir string) []Detected {
	var out []Detected
	for _, n := range Names() {
		if clock, source, ok := all[n].Detect(dir); ok {
			out = append(out, Detected{Subject: all[n], Clock: clock, Source: source})
		}
	}
	return out
}

var all = map[string]Builtin{
	golang.Name: golang.Subject{},
}

// Lookup returns a built-in subject by name.
func Lookup(name string) (Builtin, bool) {
	b, ok := all[name]
	return b, ok
}

// Names lists the built-in subjects.
func Names() []string {
	names := make([]string, 0, len(all))
	for n := range all {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Shapes returns every built-in clock shape.
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
	for _, n := range Names() {
		seed, err := all[n].Seed()
		if err != nil {
			return nil, err
		}
		out = append(out, seed)
	}
	return out, nil
}
