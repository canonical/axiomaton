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

package axiomlog

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/canonical/axiomaton/internal/axiom"
	"github.com/canonical/axiomaton/internal/config"
)

// Layer is one directory on the walk from the working directory to the root.
type Layer struct {
	Dir string
	// Project is the parsed axiomaton.cnf in Dir, if any.
	Project *config.Project
	// LogPath is the axiomlog in Dir, which may not exist yet.
	LogPath string
	// HasLog reports whether LogPath exists.
	HasLog bool
	// InRepo is true when Dir is inside the nearest enclosing git repository.
	InRepo bool
	// RepoRoot is true when Dir contains the .git entry.
	RepoRoot bool
}

// Workspace is everything discovered for a working directory.
type Workspace struct {
	// Layers run from the working directory outward to the root.
	Layers []Layer
	// UserLog is the XDG data home fallback log.
	UserLog string
	// RepoRoot is the nearest enclosing git repository, or "" when none.
	RepoRoot string
}

// Discover walks from dir to the filesystem root collecting project
// configuration and logs.
func Discover(dir string) (*Workspace, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	ws := &Workspace{UserLog: config.UserLogPath()}
	inRepo := true
	for d := abs; ; d = filepath.Dir(d) {
		l := Layer{Dir: d, LogPath: filepath.Join(d, config.LogFile)}
		cnf := filepath.Join(d, config.ProjectFile)
		if _, err := os.Stat(cnf); err == nil {
			p, err := config.LoadProject(cnf)
			if err != nil {
				return nil, err
			}
			l.Project = &p
		} else if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		if _, err := os.Stat(l.LogPath); err == nil {
			l.HasLog = true
		}
		if _, err := os.Lstat(filepath.Join(d, ".git")); err == nil {
			l.RepoRoot = true
		}
		l.InRepo = inRepo
		if l.RepoRoot && ws.RepoRoot == "" {
			ws.RepoRoot = d
			inRepo = false
		}
		ws.Layers = append(ws.Layers, l)
		if filepath.Dir(d) == d {
			break
		}
	}
	// Directories above the repo root are not in the repo; if there is no
	// repo at all, nothing is "in" one.
	if ws.RepoRoot == "" {
		for i := range ws.Layers {
			ws.Layers[i].InRepo = false
		}
	}
	return ws, nil
}

// Registry builds the subject registry from built-in shapes plus every
// project file, nearest directory winning.
func (ws *Workspace) Registry(builtin map[string][]axiom.ClockShape) (*axiom.Registry, error) {
	reg := axiom.NewRegistry()
	for name, shape := range builtin {
		if err := reg.Define(name, shape); err != nil {
			return nil, err
		}
	}
	// Apply outermost first so the nearest definition overrides.
	for i := len(ws.Layers) - 1; i >= 0; i-- {
		if p := ws.Layers[i].Project; p != nil {
			if err := p.Apply(reg); err != nil {
				return nil, err
			}
		}
	}
	return reg, nil
}

// DefaultSubjects returns the subjects declared by project files. Without
// global, only files inside the git repository (or, with no repository,
// only the working directory's own file) are consulted. The nearest
// declaration of a subject wins.
func (ws *Workspace) DefaultSubjects(global bool) []config.SubjectRef {
	var out []config.SubjectRef
	seen := map[string]bool{}
	for i, l := range ws.Layers {
		if l.Project == nil {
			continue
		}
		if !global {
			if ws.RepoRoot != "" && !l.InRepo {
				break
			}
			if ws.RepoRoot == "" && i > 0 {
				break
			}
		}
		for _, s := range l.Project.Subjects {
			if seen[s.Name] {
				continue
			}
			seen[s.Name] = true
			out = append(out, s)
		}
	}
	return out
}

// Load reads every discovered log plus the user log, outermost first so
// nearer logs shadow duplicates.
func (ws *Workspace) Load(builtin ...[]axiom.Axiom) ([]axiom.Axiom, error) {
	logs := append([][]axiom.Axiom{}, builtin...)
	if user, err := ReadFile(ws.UserLog); err != nil {
		return nil, err
	} else if len(user) > 0 {
		logs = append(logs, user)
	}
	for i := len(ws.Layers) - 1; i >= 0; i-- {
		l := ws.Layers[i]
		if !l.HasLog {
			continue
		}
		ax, err := ReadFile(l.LogPath)
		if err != nil {
			return nil, err
		}
		logs = append(logs, ax)
	}
	return Merge(logs...), nil
}

// WriteTarget picks the log that add and update should append to: the
// nearest writeable log or project file directory inside the repository,
// then the repository root, then the user log.
func (ws *Workspace) WriteTarget() (string, error) {
	for _, l := range ws.Layers {
		if ws.RepoRoot != "" && !l.InRepo {
			break
		}
		if ws.RepoRoot == "" && l.Dir != ws.Layers[0].Dir {
			break
		}
		if (l.HasLog || l.Project != nil) && writeable(l.LogPath) {
			return l.LogPath, nil
		}
	}
	if ws.RepoRoot != "" {
		p := filepath.Join(ws.RepoRoot, config.LogFile)
		if writeable(p) {
			return p, nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(ws.UserLog), 0o755); err != nil {
		return "", fmt.Errorf("create user log directory: %w", err)
	}
	return ws.UserLog, nil
}

// writeable reports whether path can be appended to or created.
func writeable(path string) bool {
	if f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0); err == nil {
		f.Close()
		return true
	} else if !errors.Is(err, fs.ErrNotExist) {
		return false
	}
	// The file does not exist; check the directory accepts new files.
	f, err := os.CreateTemp(filepath.Dir(path), ".axiomaton-probe-*")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return true
}
