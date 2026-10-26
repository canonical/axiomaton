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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
	// Update is the research brief that lets ax update extend a subject
	// that is not built in. Nil means the subject is maintained by hand.
	Update *UpdateDef
}

// UpdateDef tells the update agent what a custom subject is, where its
// truth lives and how to prepare the sandbox. Everything else about an
// update (the sandbox, the proxy, proposing, confirming, review) is the
// same for every subject.
type UpdateDef struct {
	// About says what the subject is and what its axioms are for, in a
	// sentence or two. It is the only description the agents get.
	About string `yaml:"about"`
	// Sources are release notes, documentation, registries or repositories
	// the agents work from, most authoritative first: https URLs, or paths
	// to local git checkouts (relative to the project file), which are
	// shipped into the sandbox as git bundles. SSH remotes cannot be used.
	Sources []string `yaml:"sources"`
	// Setup is a shell command run in every fresh sandbox before an agent
	// starts, for example to install the tool under study.
	Setup string `yaml:"setup,omitempty"`
	// Clock explains how to fill the clock values, in addition to what the
	// shape says, e.g. "the release the statement holds from, without v".
	Clock string `yaml:"clock,omitempty"`
	// Verify is extra guidance for the confirmation agent.
	Verify string `yaml:"verify,omitempty"`
	// VM runs the agents in an LXD virtual machine rather than a container.
	VM bool `yaml:"vm,omitempty"`
}

// Validate checks that the brief has what the agents need.
func (d UpdateDef) Validate() error {
	if strings.TrimSpace(d.About) == "" {
		return errors.New("update.about is required: say what the subject is and what its axioms are for")
	}
	if len(d.Sources) == 0 {
		return errors.New("update.sources is required: list at least one https URL or local git checkout")
	}
	for _, s := range d.Sources {
		s = strings.TrimSpace(s)
		switch {
		case s == "":
			return errors.New("update.sources has an empty entry")
		case strings.HasPrefix(s, "ssh://") || strings.HasPrefix(s, "git@") || strings.HasPrefix(s, "git+ssh://"):
			return fmt.Errorf("update source %q uses SSH, which the sandbox cannot reach; use an https URL or a local checkout", s)
		case strings.HasPrefix(s, "http://"):
			return fmt.Errorf("update source %q is plain http; use https", s)
		}
	}
	return nil
}

// IsLocalSource reports whether an update source is a filesystem path
// rather than a URL.
func IsLocalSource(s string) bool {
	return !strings.Contains(s, "://")
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
			Name   string             `yaml:"name"`
			Clock  []string           `yaml:"clock"`
			Shape  []axiom.ClockShape `yaml:"shape"`
			Update *UpdateDef         `yaml:"update"`
		}
		if err := n.Decode(&m); err != nil {
			return err
		}
		if m.Name == "" {
			return fmt.Errorf("line %d: subject has no name", n.Line)
		}
		s.Name, s.Clock, s.Shape, s.Update = m.Name, m.Clock, m.Shape, m.Update
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
		if u := p.Subjects[i].Update; u != nil {
			if err := u.Validate(); err != nil {
				return Project{}, fmt.Errorf("%s: subject %s: %w", path, p.Subjects[i].Name, err)
			}
			for k, s := range u.Sources {
				s = strings.TrimSpace(s)
				if IsLocalSource(s) && !filepath.IsAbs(s) {
					s = filepath.Join(p.Dir, s)
				}
				u.Sources[k] = s
			}
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
