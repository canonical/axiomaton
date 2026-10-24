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

// Package golang is the built-in subject for the Go programming language.
package golang

import (
	_ "embed"
	"fmt"
	"strings"

	"github.com/canonical/axiomaton/internal/axiom"
	"github.com/canonical/axiomaton/internal/axiomlog"
)

// Name is the subject name.
const Name = "golang"

//go:embed axiomlog
var seed string

// Subject implements the built-in subject.
type Subject struct{}

func (Subject) Name() string { return Name }

func (Subject) Shape() []axiom.ClockShape {
	return []axiom.ClockShape{{Name: "version", Kind: axiom.KindGoVersion}}
}

func (Subject) Seed() ([]axiom.Axiom, error) {
	return axiomlog.Read(strings.NewReader(seed), "builtin:golang")
}

func (Subject) UpdateInstructions() string {
	return `You are a meticulous technical researcher maintaining an append-only log of
axioms about the Go programming language. An axiom is a single, self-contained,
declarative statement that is true from a given Go release onwards and that a
programmer or language model working with Go needs to know: language changes,
removed or added standard library APIs and packages, changed defaults, tooling
behaviour changes (go command, vet, modules), runtime guarantees, and
deprecations. Prefer statements about what IS true now over narrative about
what changed. Do not record performance anecdotes, internal implementation
details, platform port minutiae or marketing.

You work inside a fresh Ubuntu container with root access via the shell tool.
The container starts with almost no tools installed; install what you need
(for example: apt-get update && apt-get install -y curl ca-certificates) before
use. Network access is available. Keep commands non-interactive.

Sources of truth, in order of preference:
  1. https://go.dev/doc/devel/release  (release history and dates)
  2. https://go.dev/doc/go1.N           (release notes for Go 1.N)
  3. https://go.dev/ref/spec            (the language specification)
  4. https://pkg.go.dev                 (package documentation)

Record each axiom with the propose_axiom tool. Use the release date from the
release history as the time and the release version (e.g. "1.27.0") as the
clock value. Set the source to the URL you read it from. Only propose axioms
you have actually read in a source; never propose from memory alone.`
}

func (Subject) UpdateTask(existing []axiom.Axiom) string {
	var b strings.Builder
	latest := latestVersion(existing)
	fmt.Fprintf(&b, "The log currently holds %d axioms about Go.", len(existing))
	if latest != "" {
		fmt.Fprintf(&b, " The newest Go release it covers is %s.", latest)
	}
	b.WriteString(`

Your task:
1. Fetch the Go release history and identify every Go release that is newer
   than the newest release the log covers (ignore release candidates and
   betas). If there are none, say so and stop.
2. For each such release, read its release notes in full. For each
   foundational change, propose one axiom. Aim for completeness over brevity:
   language changes and new or removed APIs matter most, then tool and
   runtime behaviour.
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
			fmt.Fprintf(&b, "- [%s %s] %s\n", a.Time.Format("2006-01-02"), a.Clock["version"], a.Statement)
		}
	}
	return b.String()
}

func (Subject) ConfirmInstructions() string {
	return `You are an independent fact checker for statements about the Go programming
language. You work inside a fresh Ubuntu container with root access via the
shell tool; it starts with almost no tools installed, so install what you need
(for example curl, ca-certificates, and if useful the Go toolchain from
https://go.dev/dl/) before use. Network access is available.

Verify the statement against primary sources: the official release notes
(https://go.dev/doc/go1.N), the language specification, package documentation
on pkg.go.dev, or by compiling and running a small Go program with the stated
Go version. Confirm only if the statement is accurate for the stated release
and is not misleading or materially incomplete. If you cannot verify it, do
not confirm it.`
}

func (Subject) ConfirmTask(a axiom.Axiom) string {
	return fmt.Sprintf(`Verify this proposed axiom.

Go release: %s (released %s)
Statement: %s
Claimed source: %s

Investigate, then answer with the structured verdict: confirmed true or false,
and a one or two sentence reason citing what you checked.`,
		a.Clock["version"], a.Time.Format("2006-01-02"), a.Statement, a.Source)
}

func latestVersion(existing []axiom.Axiom) string {
	d := axiom.ClockShape{Name: "version", Kind: axiom.KindGoVersion}
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
