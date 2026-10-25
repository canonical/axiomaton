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

// Package gomod is the built-in subject family for Go module dependencies.
// Every module a project requires directly in its go.mod gets a subject
// named go:<module path>, e.g. go:golang.org/x/net, clocked
// by the module version in use.
package gomod

import (
	"fmt"
	"strings"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"

	"github.com/canonical/axiomaton/internal/axiom"
	"github.com/canonical/axiomaton/internal/subjects/golang"
)

// Prefix starts the name of every subject in the family.
const Prefix = "go:"

// Pattern describes the family's subject names in help text.
const Pattern = Prefix + "<module path>"

// Subject is the built-in subject for one Go module.
type Subject struct {
	// Module is the module path, e.g. golang.org/x/net.
	Module string
}

// Lookup returns the subject for a family name such as
// go:golang.org/x/net. Names outside the family, or whose module
// path is not valid, are rejected.
func Lookup(name string) (Subject, bool) {
	if !strings.HasPrefix(name, Prefix) {
		return Subject{}, false
	}
	mod := strings.TrimPrefix(name, Prefix)
	if module.CheckPath(mod) != nil {
		return Subject{}, false
	}
	return Subject{Module: mod}, true
}

// Member is a family subject detected in a project.
type Member struct {
	Subject Subject
	// Clock is the required version as written in go.mod, e.g. v0.42.0.
	Clock []string
	// Source is the go.mod the requirement was read from.
	Source string
}

// Detect lists a subject for every direct requirement of the go.mod that
// governs dir. Indirect requirements are left out: they are not the
// project's own knowledge boundary.
func Detect(dir string) []Member {
	path, f, ok := golang.ModFile(dir)
	if !ok {
		return nil
	}
	var out []Member
	for _, r := range f.Require {
		if r.Indirect || r.Mod.Path == "" || r.Mod.Version == "" {
			continue
		}
		out = append(out, Member{Subject: Subject{Module: r.Mod.Path}, Clock: []string{r.Mod.Version}, Source: path})
	}
	return out
}

// Name implements the built-in subject.
func (s Subject) Name() string { return Prefix + s.Module }

// Shape is the module's semantic version.
func (Subject) Shape() []axiom.ClockShape {
	return []axiom.ClockShape{{Name: "version", Kind: axiom.KindSemver}}
}

// Seed returns nothing: module logs are built by ax update in the project.
func (Subject) Seed() ([]axiom.Axiom, error) { return nil, nil }

// Detect reads the version the project requires for this module from its
// go.mod. Unlike the family detection it also accepts an indirect
// requirement, since the subject was named on purpose.
func (s Subject) Detect(dir string) (clock []string, source string, ok bool) {
	path, f, ok := golang.ModFile(dir)
	if !ok {
		return nil, "", false
	}
	if r := requirement(f, s.Module); r != nil {
		return []string{r.Mod.Version}, path, true
	}
	return nil, "", false
}

func requirement(f *modfile.File, path string) *modfile.Require {
	for _, r := range f.Require {
		if r.Mod.Path == path && r.Mod.Version != "" {
			return r
		}
	}
	return nil
}

func (s Subject) UpdateInstructions() string {
	return fmt.Sprintf(`You are a meticulous technical researcher maintaining an append-only log of
axioms about the Go module %[1]s. An axiom is a single, self-contained,
declarative statement that is true from a given release of the module onwards
and that a programmer or language model using the module needs to know: added,
removed or renamed exported identifiers and packages, changed signatures or
semantics, changed defaults, new or dropped minimum Go versions, deprecations,
and behaviour changes a caller can observe. Prefer statements about what IS
true now over narrative about what changed. Do not record internal refactors,
documentation or test-only changes, CI or dependency bumps that change no
behaviour, performance anecdotes or marketing. Name identifiers with their
import path, e.g. %[1]s/html.Parse, so each statement stands alone. For an API
change, you must demonstrate the new or changed usage with a short example.

You work inside a fresh Ubuntu container with root access via the shell tool.
The container starts with almost no tools installed; install what you need
(for example: apt-get update && apt-get install -y curl ca-certificates git,
and the Go toolchain from https://go.dev/dl/) before use. Network access is
available. Keep commands non-interactive.

Sources of truth, in order of preference:
  1. The module proxy, which is authoritative for releases and their dates:
       https://proxy.golang.org/%[2]s/@v/list          (every version)
       https://proxy.golang.org/%[2]s/@v/<version>.info (JSON with "Time")
     Upper-case letters in a module path are escaped as "!" followed by the
     lower-case letter in proxy URLs.
  2. The module's own release notes, changelog or tagged GitHub releases.
  3. The exported API of each version, compared mechanically: after
     "go mod download -json %[1]s@<version>" gives each version's directory,
     "go run golang.org/x/exp/cmd/apidiff@latest -m <old dir> <new dir>" lists
     the API changes between two versions; package documentation is at
     https://pkg.go.dev/%[1]s@<version>.
  4. The module source at the tag, and small Go programs compiled against a
     specific version to confirm behaviour.

Record each axiom with the propose_axiom tool. Use the proxy's "Time" for the
version (date part, UTC) as the time and the version without its leading "v"
(e.g. "0.42.0") as the clock value. Set the source to the URL you read it
from. Only propose axioms you have actually read in a source or verified by
running code; never propose from memory alone.

Hard limit: never propose an axiom dated on or before %[3]s, the global epoch.
Everything older is assumed to be prior knowledge of every model, and the
propose_axiom tool rejects it. Releases before that date are out of scope
even when the log is empty.`, s.Module, escapedPath(s.Module), axiom.GlobalEpoch.Format("2006-01-02"))
}

// escapedPath is the module path as it appears in proxy URLs.
func escapedPath(path string) string {
	esc, err := module.EscapePath(path)
	if err != nil {
		return path
	}
	return esc
}

func (s Subject) UpdateTask(existing []axiom.Axiom) string {
	var b strings.Builder
	latest := latestVersion(existing)
	fmt.Fprintf(&b, "The log currently holds %d axioms about %s.", len(existing), s.Module)
	if latest != "" {
		fmt.Fprintf(&b, " The newest release it covers is v%s.", latest)
	}
	b.WriteString(`

Your task:
1. List the module's versions from the proxy and identify every tagged
   release that is newer than the newest release the log covers and was
   published after ` + axiom.GlobalEpoch.Format("2006-01-02") + `, the global epoch. Ignore
   pre-releases (such as -rc or -beta) and pseudo-versions unless the module
   has no tagged releases at all, in which case say so and stop.`)
	if latest == "" {
		b.WriteString(`
   The log is empty; if that leaves more than about twenty releases, cover
   the newest twenty.`)
	}
	b.WriteString(`
   If there is nothing to cover, say so and stop.
2. Work from the oldest release to the newest. For each one, establish what
   changed for callers (release notes if any, otherwise an API comparison
   with the previous release plus the commit log between the tags) and
   propose one axiom per foundational change. Exported API additions,
   removals and signature changes matter most, then changed behaviour and
   defaults, then deprecations and minimum Go version changes. A release
   with no caller-visible change needs no axiom.
3. Do not repeat an axiom that is already in the log. The existing axioms
   for the most recent releases are listed below for reference.
4. When finished, reply with a short plain-text summary of which releases
   you covered and how many axioms you proposed.
`)
	recent := existing
	if len(recent) > 60 {
		recent = recent[len(recent)-60:]
	}
	if len(recent) > 0 {
		b.WriteString("\nExisting axioms (most recent):\n")
		for _, a := range recent {
			fmt.Fprintf(&b, "- [%s v%s] %s\n", a.Time.Format("2006-01-02"), a.Clock["version"], a.Statement)
		}
	}
	return b.String()
}

func (s Subject) ConfirmInstructions() string {
	return fmt.Sprintf(`You are an independent fact checker for statements about the Go module
%[1]s. You work inside a fresh Ubuntu container with root access via the
shell tool; it starts with almost no tools installed, so install what you need
(for example curl, ca-certificates, git and the Go toolchain from
https://go.dev/dl/) before use. Network access is available.

Verify the statement against primary sources: the module's release notes or
changelog, the package documentation for the stated version on pkg.go.dev
(https://pkg.go.dev/%[1]s@<version>), the module source at that version
("go mod download -json %[1]s@<version>" prints where it is), an API
comparison with the previous version (golang.org/x/exp/cmd/apidiff), or by
compiling and running a small Go program that requires exactly the stated
version. Confirm only if the statement, exactly as written, is accurate for
the stated release and is not misleading or materially incomplete. If you
cannot verify it, do not confirm it.

When you do not confirm, distinguish two cases. If the statement is close but
inaccurate (wrong release, wrong identifier or package, overstated, missing a
condition that matters) and you have verified what is actually true, provide
the corrected statement as the correction: one or two self-contained
sentences in the same style, true for the stated release, that you would
confirm yourself. If the statement is unfounded, that is nothing like it is
true or you could not establish what is true, leave the correction empty and
say so in the reason.`, s.Module)
}

func (s Subject) ConfirmTask(a axiom.Axiom) string {
	return fmt.Sprintf(`Verify this proposed axiom.

Module: %s
Release: v%s (released %s)
Statement: %s
Claimed source: %s

Investigate, then call the submit_answer tool with confirmed true or false,
a one or two sentence reason citing what you checked, and, only when the
statement is inaccurate rather than unfounded, a correction giving the
verified statement that should replace it.`,
		s.Module, a.Clock["version"], a.Time.Format("2006-01-02"), a.Statement, a.Source)
}

func latestVersion(existing []axiom.Axiom) string {
	d := axiom.ClockShape{Name: "version", Kind: axiom.KindSemver}
	latest := ""
	for _, a := range existing {
		v := a.Clock["version"]
		if v == "" {
			continue
		}
		if latest == "" || d.Compare(v, latest) == axiom.Greater {
			latest = v
		}
	}
	return latest
}
