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
	"os"
	"strings"

	"golang.org/x/mod/modfile"

	"github.com/canonical/axiomaton/internal/axiom"
	"github.com/canonical/axiomaton/internal/axiomlog"
	"github.com/canonical/axiomaton/internal/subjects/detect"
)

// Name is the subject name.
const Name = "go"

//go:embed axiomlog
var seed string

// Subject implements the built-in subject.
type Subject struct{}

func (Subject) Name() string { return Name }

func (Subject) Shape() []axiom.ClockShape {
	return []axiom.ClockShape{{Name: "version", Kind: axiom.KindGoVersion}}
}

func (Subject) Seed() ([]axiom.Axiom, error) {
	return axiomlog.Read(strings.NewReader(seed), "builtin:go")
}

// Detect reports whether dir is part of a Go module by looking for a go.mod
// in dir or its parents, stopping at the repository root. The clock is the
// module's go directive, which fixes the language version the code is
// written against.
func (Subject) Detect(dir string) (clock []string, source string, ok bool) {
	path, f, ok := ModFile(dir)
	if !ok || f.Go == nil || f.Go.Version == "" {
		return nil, "", false
	}
	return []string{f.Go.Version}, path, true
}

// ModFile finds and parses the go.mod that governs dir: the nearest one in
// dir or its parents, not looking above the repository root. Unknown
// directives are ignored so a go.mod from a newer toolchain still parses.
func ModFile(dir string) (path string, f *modfile.File, ok bool) {
	path, ok = detect.FindUp(dir, "go.mod")
	if !ok {
		return "", nil, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", nil, false
	}
	f, err = modfile.ParseLax(path, data, nil)
	if err != nil {
		return "", nil, false
	}
	return path, f, true
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
details, platform port minutiae or marketing. For language changes, you must
demonstrate an example.

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
you have actually read in a source; never propose from memory alone.

Hard limit: never propose an axiom dated on or before ` + axiom.GlobalEpoch.Format("2006-01-02") + `, the global epoch.
Everything older is assumed to be prior knowledge of every model, and the
propose_axiom tool rejects it. Releases before that date are out of scope
even when the log is empty.`
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
   than the newest release the log covers and was released after
   ` + axiom.GlobalEpoch.Format("2006-01-02") + `, the global epoch (ignore release candidates and
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
Go version. Confirm only if the statement, exactly as written, is accurate for
the stated release and is not misleading or materially incomplete. If you
cannot verify it, do not confirm it.

When you do not confirm, distinguish two cases. If the statement is close but
inaccurate (wrong release, wrong identifier or package, overstated, missing a
condition that matters) and you have verified what is actually true, provide
the corrected statement as the correction: one or two self-contained
sentences in the same style, true for the stated release, that you would
confirm yourself. If the statement is unfounded, that is nothing like it is
true or you could not establish what is true, leave every correction empty
and say so in the reason. When the fault is the release or the date rather
than the wording, keep the statement as it is and give the correct date
(YYYY-MM-DD) and clock (the release) instead; both may be combined with a
corrected statement. If the statement bundles several distinct facts, or is
right in one part and wrong in another, give a split instead: the separate
axioms that should replace it, each verified, each with its own date and
clock only where they differ from the original.`
}

func (Subject) ConfirmTask(a axiom.Axiom) string {
	return fmt.Sprintf(`Verify this proposed axiom.

Go release: %s (released %s)
Statement: %s
Claimed source: %s

Investigate, then call the submit_answer tool with confirmed true or false,
a one or two sentence reason citing what you checked, and, only when the
statement is inaccurate rather than unfounded, a correction giving the
verified statement that should replace it and/or the correct date and clock
when the release or date is what is wrong, or a split into several axioms
when it bundles distinct facts.`,
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
