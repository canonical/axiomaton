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

// Package python is the built-in subject for the Python programming language.
package python

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
const Name = "python"

//go:embed axiomlog
var seed string

// Subject implements the built-in subject.
type Subject struct{}

func (Subject) Name() string { return Name }

// Shape is the CPython release, e.g. 3.12.0. A two-part version such as
// 3.12 from requires-python is accepted and ordered as 3.12.0.
func (Subject) Shape() []axiom.ClockShape {
	return []axiom.ClockShape{{Name: "version", Kind: axiom.KindSemver}}
}

func (Subject) Seed() ([]axiom.Axiom, error) {
	return axiomlog.Read(strings.NewReader(seed), "builtin:python")
}

// manifests are the files whose presence marks a Python project.
var manifests = []string{"pyproject.toml", "setup.py", "setup.cfg", "Pipfile", "requirements.txt"}

// Detect reports whether dir is part of a Python project by looking for a
// project file in dir or its parents, stopping at the repository root. The
// clock is, in order of preference: the interpreter pinned in
// .python-version; the lower bound of requires-python in pyproject.toml;
// the lower bound of the python dependency in a Poetry project; or the
// python_version in a Pipfile. The lower bound fixes the language level the
// code is written against, as rust-version does for Rust. A project that
// declares none of these is detected without a clock.
func (Subject) Detect(dir string) (clock []string, source string, ok bool) {
	var found string
	for _, name := range manifests {
		if path, ok := detect.FindUp(dir, name); ok {
			found = path
			break
		}
	}
	if found == "" {
		return nil, "", false
	}
	if path, ok := detect.FindUp(dir, ".python-version"); ok {
		if v := pinnedVersion(path); v != "" {
			return []string{v}, path, true
		}
	}
	if path, ok := detect.FindUp(dir, "pyproject.toml"); ok {
		if data, err := os.ReadFile(path); err == nil {
			text := string(data)
			if v := lowerBound(detect.TOMLString(text, "project", "requires-python")); v != "" {
				return []string{v}, path, true
			}
			if v := lowerBound(detect.TOMLString(text, "tool.poetry.dependencies", "python")); v != "" {
				return []string{v}, path, true
			}
		}
	}
	if path, ok := detect.FindUp(dir, "Pipfile"); ok {
		if data, err := os.ReadFile(path); err == nil {
			for _, key := range []string{"python_full_version", "python_version"} {
				if v := detect.TOMLString(string(data), "requires", key); releaseVersion.MatchString(v) {
					return []string{v}, path, true
				}
			}
		}
	}
	return nil, found, true
}

// releaseVersion matches a final CPython release such as 3.12 or 3.12.4.
var releaseVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+(\.[0-9]+)?$`)

// pinnedVersion reads the first release named in a .python-version file.
// pyenv allows several lines and names such as pypy3.10 or system, which
// are skipped.
func pinnedVersion(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		if releaseVersion.MatchString(line) {
			return line
		}
	}
	return ""
}

var clause = regexp.MustCompile(`^(>=|~=|==|\^|~|>)?\s*v?([0-9]+\.[0-9]+(?:\.[0-9]+)?)(\.\*)?$`)

// lowerBound extracts the minimum version from a requires-python style
// specifier such as ">=3.11", ">=3.9, <4", "~=3.10.2", "==3.12.*" or a
// Poetry constraint such as "^3.11". A bare upper bound yields "".
func lowerBound(spec string) string {
	for part := range strings.SplitSeq(spec, ",") {
		part = strings.TrimSpace(part)
		if m := clause.FindStringSubmatch(part); m != nil {
			return m[2]
		}
	}
	return ""
}

func (Subject) UpdateInstructions() string {
	return `You are a meticulous technical researcher maintaining an append-only log of
axioms about the Python programming language (CPython). An axiom is a single,
self-contained, declarative statement that is true from a given Python
release onwards and that a programmer or language model working with Python
needs to know: language and syntax changes, added, deprecated or removed
standard library modules, functions and parameters, changed defaults and
behaviour, interpreter and typing changes, and tooling changes shipped with
the release (pip, venv, the REPL). Prefer statements about what IS true now
over narrative about what changed. Do not record performance anecdotes,
internal implementation details, platform build minutiae or marketing. For
language changes, you must demonstrate an example.

You work inside a fresh Ubuntu container with root access via the shell tool.
The container starts with almost no tools installed; install what you need
(for example: apt-get update && apt-get install -y curl ca-certificates, then
uv via curl -LsSf https://astral.sh/uv/install.sh | sh, after which
"uv python install 3.13" provides any specific CPython release and
"uv run --python 3.13 script.py" runs code with it) before use. Network
access is available. Keep commands non-interactive.

Sources of truth, in order of preference:
  1. https://docs.python.org/3/whatsnew/3.N.html   (What's New in Python 3.N)
  2. https://docs.python.org/3/whatsnew/changelog.html and the release
     schedule PEPs linked from https://peps.python.org/ (release dates)
  3. https://www.python.org/downloads/            (final release dates)
  4. https://docs.python.org/3/reference/ and https://docs.python.org/3/library/
     (the language reference and library documentation)

Record each axiom with the propose_axiom tool. Use the final release date of
the feature release as the time and its version (e.g. "3.13.0") as the clock
value; use a bugfix release (e.g. "3.12.4") only for a change that first
shipped in it. Set the source to the URL you read it from. Only propose
axioms you have actually read in a source; never propose from memory alone.

Hard limit: never propose an axiom dated on or before ` + axiom.GlobalEpoch.Format("2006-01-02") + `, the global epoch.
Everything older is assumed to be prior knowledge of every model, and the
propose_axiom tool rejects it. Releases before that date are out of scope
even when the log is empty.`
}

func (Subject) UpdateTask(existing []axiom.Axiom) string {
	var b strings.Builder
	latest := latestVersion(existing)
	fmt.Fprintf(&b, "The log currently holds %d axioms about Python.", len(existing))
	if latest != "" {
		fmt.Fprintf(&b, " The newest Python release it covers is %s.", latest)
	}
	b.WriteString(`

Your task:
1. Identify every final CPython feature release (3.N.0) that is newer than
   the newest release the log covers and was released
   after ` + axiom.GlobalEpoch.Format("2006-01-02") + `, the global epoch (ignore alphas, betas and
   release candidates). If there are none, say so and stop.
2. Work from the oldest release to the newest. For each one, read its
   What's New document in full. For each foundational change, propose one
   axiom. Aim for completeness over brevity: language changes and added,
   deprecated or removed standard library APIs matter most, then interpreter
   behaviour and tooling.
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
	return `You are an independent fact checker for statements about the Python
programming language (CPython). You work inside a fresh Ubuntu container with
root access via the shell tool; it starts with almost no tools installed, so
install what you need (for example curl and ca-certificates, then uv from
https://astral.sh/uv/install.sh, with "uv python install <version>" to get an
exact CPython release) before use. Network access is available.

Verify the statement against primary sources: the What's New document for
the release (https://docs.python.org/3/whatsnew/3.N.html), the changelog, the
language reference or library documentation, or by running a small Python
program with exactly the stated release (uv run --python <version>).
Confirm only if the statement, exactly as written, is accurate for the stated
release and is not misleading or materially incomplete. If you cannot verify
it, do not confirm it.

When you do not confirm, distinguish two cases. If the statement is close but
inaccurate (wrong release, wrong module or name, overstated, missing a
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

Python release: %s (released %s)
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
