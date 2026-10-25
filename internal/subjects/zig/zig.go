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

// Package zig is the built-in subject for the Zig programming language.
package zig

import (
	_ "embed"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/canonical/axiomaton/internal/axiom"
	"github.com/canonical/axiomaton/internal/axiomlog"
	"github.com/canonical/axiomaton/internal/subjects/detect"
)

// Name is the subject name.
const Name = "zig"

//go:embed axiomlog
var seed string

// Subject implements the built-in subject.
type Subject struct{}

func (Subject) Name() string { return Name }

// Shape is the Zig release, e.g. 0.14.0. A development build such as
// 0.15.0-dev.1234+abcdef12 is valid too and orders before its release.
func (Subject) Shape() []axiom.ClockShape {
	return []axiom.ClockShape{{Name: "version", Kind: axiom.KindSemver}}
}

func (Subject) Seed() ([]axiom.Axiom, error) {
	return axiomlog.Read(strings.NewReader(seed), "builtin:zig")
}

// Detect reports whether dir is part of a Zig project by looking for a
// build.zig or build.zig.zon in dir or its parents, stopping at the
// repository root. The clock is the compiler pinned in a .zigversion file
// when there is one, otherwise the minimum_zig_version declared in
// build.zig.zon, which fixes the language level the code is written
// against. A project that declares neither is detected without a clock.
func (Subject) Detect(dir string) (clock []string, source string, ok bool) {
	var found string
	for _, name := range []string{"build.zig", "build.zig.zon"} {
		if path, ok := detect.FindUp(dir, name); ok {
			found = path
			break
		}
	}
	if found == "" {
		return nil, "", false
	}
	if path, ok := detect.FindUp(dir, ".zigversion"); ok {
		if data, err := os.ReadFile(path); err == nil {
			if v := strings.TrimSpace(string(data)); zigVersion.MatchString(v) {
				return []string{v}, path, true
			}
		}
	}
	if path, ok := detect.FindUp(dir, "build.zig.zon"); ok {
		if data, err := os.ReadFile(path); err == nil {
			if v := minimumVersion(string(data)); v != "" {
				return []string{v}, path, true
			}
		}
	}
	return nil, found, true
}

// zigVersion matches a Zig release or development build.
var zigVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(-dev\.[0-9]+\+[0-9a-f]+)?$`)

var minimumField = regexp.MustCompile(`\.minimum_zig_version\s*=\s*"([^"]*)"`)

// minimumVersion reads .minimum_zig_version from build.zig.zon, ignoring
// // comments.
func minimumVersion(zon string) string {
	var lines []string
	for _, line := range strings.Split(zon, "\n") {
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		lines = append(lines, line)
	}
	m := minimumField.FindStringSubmatch(strings.Join(lines, "\n"))
	if m == nil || !zigVersion.MatchString(m[1]) {
		return ""
	}
	return m[1]
}

func (Subject) UpdateInstructions() string {
	return `You are a meticulous technical researcher maintaining an append-only log of
axioms about the Zig programming language. An axiom is a single,
self-contained, declarative statement that is true from a given Zig release
onwards and that a programmer or language model working with Zig needs to
know: language and syntax changes, added, renamed, deprecated or removed
standard library declarations, changed defaults and semantics, build system
(build.zig, build.zig.zon, the package manager) changes, compiler
command-line changes and compatibility notes. Zig is pre-1.0 and every
release breaks things, so removals and renames matter as much as additions.
Prefer statements about what IS true now over narrative about what changed.
Do not record performance anecdotes, internal compiler details, platform
target minutiae or marketing. For language changes, you must demonstrate an
example.

You work inside a fresh Ubuntu container with root access via the shell tool.
The container starts with almost no tools installed; install what you need
(for example: apt-get update && apt-get install -y curl ca-certificates xz-utils,
then a specific compiler from the tarball listed for that version and your
architecture in https://ziglang.org/download/index.json, e.g.
curl -L https://ziglang.org/download/0.14.0/zig-linux-x86_64-0.14.0.tar.xz | tar xJ)
before use. Network access is available. Keep commands non-interactive.

Sources of truth, in order of preference:
  1. https://ziglang.org/download/<version>/release-notes.html
     (the release notes for each version)
  2. https://ziglang.org/download/index.json
     (every release with its "date", the authoritative release date)
  3. https://ziglang.org/documentation/<version>/        (language reference)
  4. https://ziglang.org/documentation/<version>/std/    (standard library)

Record each axiom with the propose_axiom tool. Use the "date" from the
download index as the time and the release version (e.g. "0.14.0") as the
clock value. Set the source to the URL you read it from. Only propose
axioms you have actually read in a source; never propose from memory alone.

Hard limit: never propose an axiom dated on or before ` + axiom.GlobalEpoch.Format("2006-01-02") + `, the global epoch.
Everything older is assumed to be prior knowledge of every model, and the
propose_axiom tool rejects it. Releases before that date are out of scope
even when the log is empty.`
}

func (Subject) UpdateTask(existing []axiom.Axiom) string {
	var b strings.Builder
	latest := latestVersion(existing)
	fmt.Fprintf(&b, "The log currently holds %d axioms about Zig.", len(existing))
	if latest != "" {
		fmt.Fprintf(&b, " The newest Zig release it covers is %s.", latest)
	}
	b.WriteString(`

Your task:
1. Read the download index and identify every tagged Zig release that is
   newer than the newest release the log covers and was released
   after ` + axiom.GlobalEpoch.Format("2006-01-02") + `, the global epoch (ignore the master
   development builds; include patch releases such as 0.14.1 only when
   their notes record a change for users). If there are none, say so and
   stop.
2. Work from the oldest release to the newest. For each one, read its
   release notes in full. For each foundational change, propose one axiom.
   Aim for completeness over brevity: language changes and standard library
   or build system changes matter most, then compiler behaviour.
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
	return `You are an independent fact checker for statements about the Zig
programming language. You work inside a fresh Ubuntu container with root
access via the shell tool; it starts with almost no tools installed, so
install what you need (for example curl, ca-certificates and xz-utils, then
the exact compiler tarball for the stated version from
https://ziglang.org/download/index.json) before use. Network access is
available.

Verify the statement against primary sources: the release notes for the
version (https://ziglang.org/download/<version>/release-notes.html), the
language reference or standard library documentation for that version, or
by compiling and running a small Zig program with exactly the stated
compiler. Confirm only if the statement, exactly as written, is accurate for
the stated release and is not misleading or materially incomplete. If you
cannot verify it, do not confirm it.

When you do not confirm, distinguish two cases. If the statement is close but
inaccurate (wrong release, wrong declaration or namespace, overstated,
missing a condition that matters) and you have verified what is actually
true, provide the corrected statement as the correction: one or two
self-contained sentences in the same style, true for the stated release, that
you would confirm yourself. If the statement is unfounded, that is nothing
like it is true or you could not establish what is true, leave every
correction empty and say so in the reason. When the fault is the release or
the date rather than the wording, keep the statement as it is and give the
correct date (YYYY-MM-DD) and clock (the release) instead; both may be
combined with a corrected statement.`
}

func (Subject) ConfirmTask(a axiom.Axiom) string {
	return fmt.Sprintf(`Verify this proposed axiom.

Zig release: %s (released %s)
Statement: %s
Claimed source: %s

Investigate, then call the submit_answer tool with confirmed true or false,
a one or two sentence reason citing what you checked, and, only when the
statement is inaccurate rather than unfounded, a correction giving the
verified statement that should replace it and/or the correct date and clock
when the release or date is what is wrong.`,
		a.Clock["version"], a.Time.Format("2006-01-02"), a.Statement, a.Source)
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
