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

// Package rust is the built-in subject for the Rust programming language.
package rust

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/canonical/axiomaton/internal/axiom"
	"github.com/canonical/axiomaton/internal/axiomlog"
	"github.com/canonical/axiomaton/internal/subjects/detect"
)

// Name is the subject name.
const Name = "rust"

//go:embed axiomlog
var seed string

// Subject implements the built-in subject.
type Subject struct{}

func (Subject) Name() string { return Name }

// Shape is the Rust release, e.g. 1.82.0. A two-part minimum such as 1.70
// from Cargo.toml is accepted and ordered as 1.70.0.
func (Subject) Shape() []axiom.ClockShape {
	return []axiom.ClockShape{{Name: "version", Kind: axiom.KindSemver}}
}

func (Subject) Seed() ([]axiom.Axiom, error) {
	return axiomlog.Read(strings.NewReader(seed), "builtin:rust")
}

// Detect reports whether dir is part of a Cargo package by looking for a
// Cargo.toml in dir or its parents, stopping at the repository root. The
// clock is the toolchain the project pins in rust-toolchain.toml (or the
// older rust-toolchain file) when it names a release, otherwise the
// package's rust-version, which fixes the language level the code is
// written against. A project that pins neither is detected without a clock.
func (Subject) Detect(dir string) (clock []string, source string, ok bool) {
	manifest, ok := detect.FindUp(dir, "Cargo.toml")
	if !ok {
		return nil, "", false
	}
	for _, name := range []string{"rust-toolchain.toml", "rust-toolchain"} {
		path, ok := detect.FindUp(dir, name)
		if !ok {
			continue
		}
		if v := toolchainVersion(path); v != "" {
			return []string{v}, path, true
		}
	}
	if v, path := rustVersion(manifest); v != "" {
		return []string{v}, path, true
	}
	return nil, manifest, true
}

// releaseVersion matches a concrete Rust release such as 1.82 or 1.82.0.
var releaseVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+(\.[0-9]+)?$`)

// toolchainVersion reads the channel from a rust-toolchain.toml
// ([toolchain] channel = "1.82.0") or a legacy rust-toolchain file holding
// the channel alone. Channels such as stable or nightly-2026-01-01 are not
// releases and yield "".
func toolchainVersion(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var channel string
	if strings.HasSuffix(path, ".toml") {
		channel = detect.TOMLString(string(data), "toolchain", "channel")
	} else {
		channel = strings.TrimSpace(string(data))
	}
	if releaseVersion.MatchString(channel) {
		return channel
	}
	return ""
}

// rustVersion reads [package] rust-version from the manifest, following a
// workspace inheritance (rust-version.workspace = true) to the nearest
// enclosing Cargo.toml with a [workspace.package] table.
func rustVersion(manifest string) (version, source string) {
	data, err := os.ReadFile(manifest)
	if err != nil {
		return "", ""
	}
	text := string(data)
	if v := detect.TOMLString(text, "package", "rust-version"); releaseVersion.MatchString(v) {
		return v, manifest
	}
	if v := detect.TOMLString(text, "workspace.package", "rust-version"); releaseVersion.MatchString(v) {
		return v, manifest
	}
	if !detect.TOMLInherits(text, "package", "rust-version") {
		return "", ""
	}
	dir := filepath.Dir(manifest)
	for !detect.RepoRoot(dir) {
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
		root := filepath.Join(dir, "Cargo.toml")
		rootData, err := os.ReadFile(root)
		if err != nil {
			continue
		}
		if v := detect.TOMLString(string(rootData), "workspace.package", "rust-version"); releaseVersion.MatchString(v) {
			return v, root
		}
	}
	return "", ""
}

func (Subject) UpdateInstructions() string {
	return `You are a meticulous technical researcher maintaining an append-only log of
axioms about the Rust programming language. An axiom is a single,
self-contained, declarative statement that is true from a given stable Rust
release onwards and that a programmer or language model working with Rust
needs to know: language changes (syntax, the type system, editions),
stabilised or deprecated standard library APIs, changed defaults, lints that
became warn or deny by default, Cargo and rustup behaviour changes, and
compatibility notes. Prefer statements about what IS true now over narrative
about what changed. Do not record performance anecdotes, internal compiler
details, platform target minutiae or marketing. For language changes, you
must demonstrate an example.

You work inside a fresh Ubuntu container with root access via the shell tool.
The container starts with almost no tools installed; install what you need
(for example: apt-get update && apt-get install -y curl ca-certificates
build-essential, then rustup via
curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh -s -- -y)
before use. Network access is available. Keep commands non-interactive.

Sources of truth, in order of preference:
  1. https://github.com/rust-lang/rust/blob/master/RELEASES.md
     (the release notes for every version, with release dates in the
     "Version 1.N.0 (YYYY-MM-DD)" headings; raw text at
     https://raw.githubusercontent.com/rust-lang/rust/master/RELEASES.md)
  2. https://blog.rust-lang.org/ (the announcement post for each release)
  3. https://doc.rust-lang.org/reference/ and
     https://doc.rust-lang.org/edition-guide/ (the language and editions)
  4. https://doc.rust-lang.org/std/ (standard library documentation)

Record each axiom with the propose_axiom tool. Use the release date from the
RELEASES.md heading as the time and the release version (e.g. "1.82.0") as
the clock value. Set the source to the URL you read it from. Only propose
axioms you have actually read in a source; never propose from memory alone.

Hard limit: never propose an axiom dated on or before ` + axiom.GlobalEpoch.Format("2006-01-02") + `, the global epoch.
Everything older is assumed to be prior knowledge of every model, and the
propose_axiom tool rejects it. Releases before that date are out of scope
even when the log is empty.`
}

func (Subject) UpdateTask(existing []axiom.Axiom) string {
	var b strings.Builder
	latest := latestVersion(existing)
	fmt.Fprintf(&b, "The log currently holds %d axioms about Rust.", len(existing))
	if latest != "" {
		fmt.Fprintf(&b, " The newest Rust release it covers is %s.", latest)
	}
	b.WriteString(`

Your task:
1. Read the release notes and identify every stable Rust release that is
   newer than the newest release the log covers and was released
   after ` + axiom.GlobalEpoch.Format("2006-01-02") + `, the global epoch (ignore beta and nightly,
   and treat point releases such as 1.82.1 as part of their minor
   release unless they change behaviour). If there are none, say so and
   stop.
2. Work from the oldest release to the newest. For each one, read its
   release notes in full. For each foundational change, propose one axiom.
   Aim for completeness over brevity: language changes and stabilised or
   deprecated APIs matter most, then Cargo, lints and compatibility notes.
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
	return `You are an independent fact checker for statements about the Rust
programming language. You work inside a fresh Ubuntu container with root
access via the shell tool; it starts with almost no tools installed, so
install what you need (for example curl, ca-certificates, build-essential
and, if useful, rustup from https://sh.rustup.rs followed by
"rustup toolchain install <version>") before use. Network access is
available.

Verify the statement against primary sources: the official release notes
(RELEASES.md in rust-lang/rust, or the release blog post), the Rust
reference, the standard library documentation, or by compiling and running a
small Rust program with exactly the stated toolchain (cargo +<version>).
Confirm only if the statement, exactly as written, is accurate for the stated
release and is not misleading or materially incomplete. If you cannot verify
it, do not confirm it.

When you do not confirm, distinguish two cases. If the statement is close but
inaccurate (wrong release, wrong identifier or module, overstated, missing a
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

Rust release: %s (released %s)
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
