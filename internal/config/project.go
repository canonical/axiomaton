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

package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/canonical/axiomaton/internal/axiom"
)

// ProjectFile is the name of the per directory project configuration.
const ProjectFile = "axiomaton.cnf"

// LogFile is the name of the per directory axiom log.
const LogFile = "axiomlog"

// SubjectRef names a subject and the project's current position on its clock.
type SubjectRef struct {
	Name string
	// Clock holds positional sub-clock values.
	Clock []string
	// Shape optionally declares the sub-clock dimensions inline.
	Shape []axiom.ClockShape
}

// UnmarshalYAML accepts either the compact list form [name, v1, v2] or the
// mapping form {name: x, clock: [v1], shape: [...]}.
func (s *SubjectRef) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.SequenceNode:
		var parts []string
		if err := n.Decode(&parts); err != nil {
			return err
		}
		if len(parts) == 0 {
			return fmt.Errorf("line %d: empty subject", n.Line)
		}
		s.Name = parts[0]
		s.Clock = parts[1:]
		return nil
	case yaml.ScalarNode:
		return n.Decode(&s.Name)
	case yaml.MappingNode:
		var m struct {
			Name  string             `yaml:"name"`
			Clock []string           `yaml:"clock"`
			Shape []axiom.ClockShape `yaml:"shape"`
		}
		if err := n.Decode(&m); err != nil {
			return err
		}
		if m.Name == "" {
			return fmt.Errorf("line %d: subject has no name", n.Line)
		}
		s.Name, s.Clock, s.Shape = m.Name, m.Clock, m.Shape
		return nil
	}
	return fmt.Errorf("line %d: subject must be a list or mapping", n.Line)
}

// Project is a parsed axiomaton.cnf.
type Project struct {
	Subjects []SubjectRef                  `yaml:"subjects"`
	Shapes   map[string][]axiom.ClockShape `yaml:"shapes"`

	// Dir is the directory containing the file.
	Dir string `yaml:"-"`
}

// LoadProject parses the axiomaton.cnf at path. Relative git repo paths in
// shapes are resolved against the file's directory.
func LoadProject(path string) (Project, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Project{}, err
	}
	var p Project
	if err := yaml.Unmarshal(data, &p); err != nil {
		return Project{}, fmt.Errorf("parse %s: %w", path, err)
	}
	p.Dir = filepath.Dir(path)
	for name, shape := range p.Shapes {
		for i := range shape {
			shape[i].Repo = resolveRepo(p.Dir, shape[i])
		}
		p.Shapes[name] = shape
	}
	for i := range p.Subjects {
		for j := range p.Subjects[i].Shape {
			p.Subjects[i].Shape[j].Repo = resolveRepo(p.Dir, p.Subjects[i].Shape[j])
		}
	}
	return p, nil
}

func resolveRepo(dir string, d axiom.ClockShape) string {
	if d.Kind != axiom.KindGit || d.Repo == "" || filepath.IsAbs(d.Repo) {
		return d.Repo
	}
	return filepath.Join(dir, d.Repo)
}

// Apply registers the project's shapes into the registry. Inline shapes on
// subjects take precedence over the shapes block.
func (p Project) Apply(reg *axiom.Registry) error {
	for name, shape := range p.Shapes {
		if err := reg.Define(name, shape); err != nil {
			return fmt.Errorf("%s: %w", filepath.Join(p.Dir, ProjectFile), err)
		}
	}
	for _, s := range p.Subjects {
		if len(s.Shape) > 0 {
			if err := reg.Define(s.Name, s.Shape); err != nil {
				return fmt.Errorf("%s: %w", filepath.Join(p.Dir, ProjectFile), err)
			}
		}
	}
	return nil
}
