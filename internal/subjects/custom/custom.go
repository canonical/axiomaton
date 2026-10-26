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

// Package custom turns a subject declared in a project file, together with
// its update brief, into a subject the update agent can extend. The prompts
// are the same shape as the built-in ones; the brief supplies what the
// subject is, where its sources are and how to prepare the sandbox.
package custom

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	"github.com/canonical/axiomaton/internal/axiom"
	"github.com/canonical/axiomaton/internal/config"
	"github.com/canonical/axiomaton/internal/update"
)

// ShipDir is where local sources are placed inside the sandbox.
const ShipDir = "/work/src"

// Subject is a custom subject with an update brief.
type Subject struct {
	name  string
	shape []axiom.ClockShape
	def   config.UpdateDef
	// bundles maps each local source to its shipment once Bundle has run.
	bundles map[string]update.Shipment
}

// New builds the subject. The shape must be declared (custom subjects have
// no built-in one) and every local source must exist.
func New(name string, shape []axiom.ClockShape, def config.UpdateDef) (*Subject, error) {
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("custom subject has no name")
	}
	if len(shape) == 0 {
		return nil, fmt.Errorf("subject %s has no clock shape; declare one (inline or under shapes:) before updating it", name)
	}
	if err := def.Validate(); err != nil {
		return nil, fmt.Errorf("subject %s: %w", name, err)
	}
	for _, src := range def.Sources {
		if !config.IsLocalSource(src) {
			continue
		}
		if st, err := os.Stat(src); err != nil {
			return nil, fmt.Errorf("subject %s: source %s: %w", name, src, err)
		} else if !st.IsDir() {
			return nil, fmt.Errorf("subject %s: source %s is not a directory", name, src)
		}
	}
	return &Subject{name: name, shape: shape, def: def}, nil
}

// Name implements subjects.Builtin.
func (s *Subject) Name() string { return s.name }

// Shape implements subjects.Builtin.
func (s *Subject) Shape() []axiom.ClockShape { return s.shape }

// Seed implements subjects.Builtin; custom subjects ship no log.
func (s *Subject) Seed() ([]axiom.Axiom, error) { return nil, nil }

// Detect implements subjects.Builtin; a custom subject's clock is declared
// in the project file, never detected.
func (s *Subject) Detect(string) ([]string, string, bool) { return nil, "", false }

// RequiresVM implements subjects.VMRequirer.
func (s *Subject) RequiresVM() bool { return s.def.VM }

// Setup implements update.Prepared.
func (s *Subject) Setup() string { return s.def.Setup }

// Shipments implements update.Prepared. It is empty until Bundle has run.
func (s *Subject) Shipments() []update.Shipment {
	var out []update.Shipment
	for _, src := range s.def.Sources {
		if b, ok := s.bundles[src]; ok {
			out = append(out, b)
		}
	}
	return out
}

// LocalSources lists the sources that are paths on this machine.
func (s *Subject) LocalSources() []string {
	var out []string
	for _, src := range s.def.Sources {
		if config.IsLocalSource(src) {
			out = append(out, src)
		}
	}
	return out
}

// Bundle writes a git bundle of every local source into dir so that the
// sandbox, which has no access to the host filesystem, can clone it. Call
// it before running the updater and remove dir afterwards.
func (s *Subject) Bundle(ctx context.Context, dir string) error {
	s.bundles = map[string]update.Shipment{}
	for _, src := range s.LocalSources() {
		remote := s.remoteFor(src)
		local := filepath.Join(dir, path.Base(remote))
		cmd := exec.CommandContext(ctx, "git", "-C", src, "bundle", "create", local, "--all")
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("bundling %s: %w: %s", src, err, strings.TrimSpace(string(out)))
		}
		s.bundles[src] = update.Shipment{Local: local, Remote: remote}
	}
	return nil
}

// remoteFor names the bundle for a local source inside the sandbox: the
// checkout's directory name, suffixed with its position when two sources
// share one.
func (s *Subject) remoteFor(src string) string {
	base := filepath.Base(filepath.Clean(src))
	if base == "" || base == "." || base == string(filepath.Separator) {
		base = "repo"
	}
	n := 0
	for _, other := range s.LocalSources() {
		if other == src {
			break
		}
		if filepath.Base(filepath.Clean(other)) == base {
			n++
		}
	}
	if n > 0 {
		base = fmt.Sprintf("%s-%d", base, n+1)
	}
	return path.Join(ShipDir, base+".bundle")
}

// cloneName is the directory a shipped bundle is cloned into.
func cloneName(remote string) string {
	return "/work/" + strings.TrimSuffix(path.Base(remote), ".bundle")
}

func (s *Subject) sourceList() string {
	var b strings.Builder
	for i, src := range s.def.Sources {
		if config.IsLocalSource(src) {
			remote := s.remoteFor(src)
			fmt.Fprintf(&b, "  %d. The git repository at %s on the user's machine, shipped into this\n     sandbox as the bundle %s. Clone it with\n     git clone %s %s\n     and read its history, tags and release notes there.\n",
				i+1, src, remote, remote, cloneName(remote))
			continue
		}
		fmt.Fprintf(&b, "  %d. %s\n", i+1, src)
	}
	return b.String()
}

// hasGit reports whether a clock dimension is git ordered.
func (s *Subject) hasGit() bool {
	for _, d := range s.shape {
		if d.Kind == axiom.KindGit {
			return true
		}
	}
	return false
}

func (s *Subject) clockDescription() string {
	var b strings.Builder
	fmt.Fprintf(&b, "The clock has %d value(s), in this order:\n", len(s.shape))
	for i, d := range s.shape {
		fmt.Fprintf(&b, "  %d. %s: %s\n", i+1, d.Name, describeKind(d.Kind))
	}
	if c := strings.TrimSpace(s.def.Clock); c != "" {
		b.WriteString(c)
		if !strings.HasSuffix(c, ".") {
			b.WriteString(".")
		}
		b.WriteString("\n")
	}
	return b.String()
}

func describeKind(k axiom.Kind) string {
	switch k {
	case axiom.KindSemver:
		return "a semantic version such as 1.2.3, without a leading v"
	case axiom.KindGoVersion:
		return "a Go toolchain version such as 1.24 or 1.25rc1"
	case axiom.KindNumber:
		return "a decimal number"
	case axiom.KindGit:
		return "the full hash of the last commit of the release, exactly as the batch_commits tool names it"
	}
	return "a string; values are ordered lexically, so write them in a form that sorts in release order"
}

func (s *Subject) sandbox() string {
	if s.def.VM {
		return "a fresh Ubuntu virtual machine"
	}
	return "a fresh Ubuntu container"
}

func (s *Subject) setupNote() string {
	if strings.TrimSpace(s.def.Setup) == "" {
		return ""
	}
	return "This command has already been run in the sandbox for you:\n  " + strings.TrimSpace(s.def.Setup) + "\n\n"
}

// UpdateInstructions implements subjects.Builtin.
func (s *Subject) UpdateInstructions() string {
	var b strings.Builder
	fmt.Fprintf(&b, `You are a meticulous technical researcher maintaining an append-only log of
axioms about %[1]s. %[2]s

An axiom is a single, self-contained, declarative statement that is true
from a given release onwards and that a programmer or language model working
with %[1]s needs to know: added, changed, deprecated or removed features,
commands, options, keys, APIs or formats, changed defaults and semantics,
new or dropped requirements, and behaviour a user can observe. Prefer
statements about what IS true now over narrative about what changed. Do not
record internal refactors, documentation or test-only changes, CI or
dependency bumps that change no behaviour, performance anecdotes or
marketing. Name things precisely (full command, key path, identifier) so
each statement stands alone. For a change to an API, a command, a file
format or a configuration key, demonstrate the new usage with a short
example.

You work inside %[3]s with root access via the shell tool.
The sandbox starts with almost no tools installed; install what you need
(for example: apt-get update && apt-get install -y curl ca-certificates git)
before use. Network access is available through a caching proxy; https and
git over https work, ssh does not. Keep commands non-interactive.

%[4]sSources of truth, in order of preference:
%[5]s
Release notes, changelogs, tags and the commit history in these sources are
authoritative for what changed and when; the reference documentation and the
tool itself confirm what is true now.

Record each axiom with the propose_axiom tool. %[6]sUse the release's
publication date (UTC, YYYY-MM-DD) as the time. Set the source to the URL or
path you read it from. Only propose axioms you have actually read in a source
or verified by running the tool; never propose from memory alone.
`, s.name, strings.TrimSpace(s.def.About), s.sandbox(), s.setupNote(), s.sourceList(), s.clockDescription())
	if s.hasGit() {
		b.WriteString(`
The git clock names releases by commit. To divide the history into releases
the same way every time, list the commits in scope on the main branch
oldest first, for example
  git log --reverse --format='%H %cI' --since=<date>
and pass them to the batch_commits tool. The tool is deterministic: a
release opens after a gap of more than a month between commits or at a new
calendar month, and is named by the full hash of its last commit. Treat each
release like a tagged one: review the changes between the previous
release's last commit and this one's, propose axioms with that hash as the
git clock value and the release's date as the time, and never invent other
names for commits.
`)
	}
	fmt.Fprintf(&b, `
Hard limit: never propose an axiom dated on or before %s, the global epoch.
Everything older is assumed to be prior knowledge of every model, and the
propose_axiom tool rejects it. Releases before that date are out of scope
even when the log is empty.`, axiom.GlobalEpoch.Format("2006-01-02"))
	return b.String()
}

// UpdateTask implements subjects.Builtin. existing must be sorted by clock.
func (s *Subject) UpdateTask(existing []axiom.Axiom) string {
	var b strings.Builder
	fmt.Fprintf(&b, "The log currently holds %d axioms about %s.", len(existing), s.name)
	if n := len(existing); n > 0 {
		if latest := existing[n-1].ClockString(s.shape); latest != "" {
			fmt.Fprintf(&b, " The newest clock it covers is %s.", latest)
		}
	}
	fmt.Fprintf(&b, `

Your task:
1. From the sources, identify every release of %s that is newer than the
   newest clock the log covers and was released after %s, the global epoch
   (count minor releases; fold point releases into them unless one changes
   behaviour). If there are none, say so and stop.
2. Work from the oldest release to the newest. For each one, read its
   release notes or changes in full and, for each foundational change,
   propose one axiom with that release as its clock. Aim for completeness
   over brevity.
3. Do not repeat an axiom that is already in the log. The existing axioms
   for the most recent releases are listed below for reference.
4. When finished, reply with a short plain-text summary of which releases
   you covered and how many axioms you proposed.
`, s.name, axiom.GlobalEpoch.Format("2006-01-02"))
	recent := existing
	if len(recent) > 60 {
		recent = recent[len(recent)-60:]
	}
	if len(recent) > 0 {
		b.WriteString("\nExisting axioms (newest last):\n")
		for _, a := range recent {
			fmt.Fprintf(&b, "- [%s %s] %s\n", a.Time.Format("2006-01-02"), a.ClockString(s.shape), a.Statement)
		}
	}
	return b.String()
}

// ConfirmInstructions implements subjects.Builtin.
func (s *Subject) ConfirmInstructions() string {
	verify := ""
	if v := strings.TrimSpace(s.def.Verify); v != "" {
		verify = v + "\n\n"
	}
	return fmt.Sprintf(`You are an independent fact checker for an append-only log of axioms about
%[1]s. %[2]s

You work inside %[3]s with root access via the shell tool.
The sandbox starts with almost no tools installed; install what you need
before use. Network access is available through a caching proxy; https and
git over https work, ssh does not. Keep commands non-interactive.

%[4]sSources of truth, in order of preference:
%[5]s
%[6]sVerify the statement against these sources: the release notes or
changes for the stated release, the reference documentation, or the tool
itself at that release. Confirm only if the statement, exactly as written,
is accurate for the stated clock and is not misleading or materially
incomplete. If you cannot verify it, do not confirm it.

When you do not confirm, distinguish two cases. If the statement is close but
inaccurate (wrong release, wrong name or scope, overstated, missing a
condition that matters) and you have verified what is actually true, provide
the corrected statement as the correction: one or two self-contained
sentences in the same style, true for the stated clock, that you would
confirm yourself. If the statement is unfounded, that is nothing like it is
true or you could not establish what is true, leave every correction empty
and say so in the reason. When the fault is the release or the date rather
than the wording, keep the statement as it is and give the correct date
(YYYY-MM-DD) and clock instead; both may be combined with a corrected
statement. %[7]sIf the statement bundles several distinct facts, or is right
in one part and wrong in another, give a split instead: the separate axioms
that should replace it, each verified, each with its own date and clock only
where they differ from the original.`,
		s.name, strings.TrimSpace(s.def.About), s.sandbox(), s.setupNote(), s.sourceList(), verify, s.clockDescription())
}

// ConfirmTask implements subjects.Builtin.
func (s *Subject) ConfirmTask(a axiom.Axiom) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Verify this proposed axiom about %s.\n\n", s.name)
	for _, d := range s.shape {
		v := a.Clock[d.Name]
		if v == "" {
			v = "any (the statement is claimed for every release)"
		}
		fmt.Fprintf(&b, "Clock %s: %s\n", d.Name, v)
	}
	fmt.Fprintf(&b, "Date: %s\nStatement: %s\nClaimed source: %s\n", a.Time.Format("2006-01-02"), a.Statement, a.Source)
	b.WriteString(`
Investigate, then call the submit_answer tool with confirmed true or false,
a one or two sentence reason citing what you checked, and, only when the
statement is inaccurate rather than unfounded, a correction giving the
verified statement that should replace it and/or the correct date and clock
when the release or date is what is wrong, or a split into several axioms
when it bundles distinct facts. Clock values are given in shape order, for
example [`)
	for i, d := range s.shape {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%q", exampleValue(d.Kind))
	}
	b.WriteString("].")
	return b.String()
}

func exampleValue(k axiom.Kind) string {
	switch k {
	case axiom.KindSemver:
		return "1.2.3"
	case axiom.KindGoVersion:
		return "1.24"
	case axiom.KindNumber:
		return "24"
	case axiom.KindGit:
		return "3f9c2a1d8e7b6c5a4f3e2d1c0b9a8f7e6d5c4b3a"
	}
	return "value"
}
