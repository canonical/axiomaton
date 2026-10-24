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
	"fmt"
	"sort"
)

// Subject is a named topic with a declared sub-clock shape.
type Subject struct {
	Name  string
	Shape []ClockShape
}

// Clock builds a sub-clock map from positional values in shape order,
// validating each against its dimension.
func (s Subject) Clock(values []string) (map[string]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	if len(values) > len(s.Shape) {
		return nil, fmt.Errorf("subject %s has %d clock dimension(s) but %d value(s) were given", s.Name, len(s.Shape), len(values))
	}
	clock := make(map[string]string, len(values))
	for i, v := range values {
		c, err := s.Shape[i].Parse(v)
		if err != nil {
			return nil, fmt.Errorf("subject %s dimension %s: %w", s.Name, s.Shape[i].Name, err)
		}
		clock[s.Shape[i].Name] = c
	}
	return clock, nil
}

// Registry maps subject names to their shapes.
type Registry struct {
	shapes map[string][]ClockShape
}

// NewRegistry creates an empty registry.
func NewRegistry() *Registry {
	return &Registry{shapes: map[string][]ClockShape{}}
}

// Define registers (or overrides) a subject's shape.
func (r *Registry) Define(name string, shape []ClockShape) error {
	for _, d := range shape {
		if err := d.Validate(); err != nil {
			return fmt.Errorf("subject %s: %w", name, err)
		}
	}
	r.shapes[name] = shape
	return nil
}

// Lookup returns the subject if it has a declared shape.
func (r *Registry) Lookup(name string) (Subject, bool) {
	shape, ok := r.shapes[name]
	return Subject{Name: name, Shape: shape}, ok
}

// Resolve returns the subject, inferring a shape from the positional values
// when none is declared. The second result is true if the shape was inferred.
func (r *Registry) Resolve(name string, values []string) (Subject, bool) {
	if s, ok := r.Lookup(name); ok {
		return s, false
	}
	return Subject{Name: name, Shape: InferShape(values)}, len(values) > 0
}

// Names lists registered subjects in sorted order.
func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.shapes))
	for n := range r.shapes {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
