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

// Package snapcraft is the built-in subject for snapcraft, the tool that
// builds snaps. Its agents run in a virtual machine: verifying snapcraft
// behaviour means installing snapd and snapcraft and running real builds,
// which need a full system with LXD inside.
package snapcraft

import (
	_ "embed"
	"fmt"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/canonical/axiomaton/internal/axiom"
	"github.com/canonical/axiomaton/internal/axiomlog"
	"github.com/canonical/axiomaton/internal/subjects/detect"
)

// Name is the subject name.
const Name = "snapcraft"

//go:embed axiomlog
var seed string

// Subject implements the built-in subject.
type Subject struct{}

func (Subject) Name() string { return Name }

// RequiresVM marks the subject's agents as needing a virtual machine.
func (Subject) RequiresVM() bool { return true }

// Shape is two-dimensional. The base is the dominant axis of snapcraft
// behaviour (core22 and core24 projects are built by different code paths
// with different schemas), given as its number: 22, 24. The second
// dimension is the snapcraft release, e.g. 8.3.0, for behaviour that
// arrived in a release rather than with a base. A project usually pins
// only its base; an axiom carries the base and, when it matters, the
// release too.
func (Subject) Shape() []axiom.ClockShape {
	return []axiom.ClockShape{
		{Name: "base", Kind: axiom.KindNumber},
		{Name: "version", Kind: axiom.KindSemver},
	}
}

func (Subject) Seed() ([]axiom.Axiom, error) {
	return axiomlog.Read(strings.NewReader(seed), "builtin:snapcraft")
}

// manifests are where a project's snapcraft.yaml may live, in the order
// snapcraft itself looks.
var manifests = []string{"snap/snapcraft.yaml", "snapcraft.yaml", ".snapcraft.yaml", "build-aux/snap/snapcraft.yaml"}

// Detect reports whether dir is part of a snap project by looking for a
// snapcraft.yaml in dir or its parents, stopping at the repository root.
// The clock is the base number taken from build-base when present
// (it decides which snapcraft code path builds the project), else base. A
// project with neither, or with bare and no build-base, is detected
// without a clock.
func (Subject) Detect(dir string) (clock []string, source string, ok bool) {
	var path string
	for _, m := range manifests {
		if p, ok := detect.FindUp(dir, m); ok {
			path = p
			break
		}
	}
	if path == "" {
		return nil, "", false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, path, true
	}
	var y struct {
		Base      string `yaml:"base"`
		BuildBase string `yaml:"build-base"`
	}
	if err := yaml.Unmarshal(data, &y); err != nil {
		return nil, path, true
	}
	for _, b := range []string{y.BuildBase, y.Base} {
		if n := baseNumber(b); n != "" {
			return []string{n}, path, true
		}
	}
	return nil, path, true
}

var coreBase = regexp.MustCompile(`^core([0-9]+)$`)

// baseNumber turns a base name into its clock value: core22 is 22, the
// original core is 16, anything else (bare, devel) is "".
func baseNumber(base string) string {
	base = strings.TrimSpace(base)
	if base == "core" {
		return "16"
	}
	if m := coreBase.FindStringSubmatch(base); m != nil {
		return m[1]
	}
	return ""
}

// NormaliseClock implements subjects.ClockNormaliser: a base given as core24 or core
// becomes its number. Other values are left for validation.
func (Subject) NormaliseClock(values []string) []string {
	if len(values) == 0 {
		return values
	}
	if n := baseNumber(values[0]); n != "" {
		out := append([]string(nil), values...)
		out[0] = n
		return out
	}
	return values
}

func (Subject) UpdateInstructions() string {
	return `You are a meticulous technical researcher maintaining an append-only log of
axioms about snapcraft, the tool that builds snaps. An axiom is a single,
self-contained, declarative statement that is true from a given base and
snapcraft release onwards and that a programmer or language model writing a
snapcraft.yaml or driving snapcraft needs to know: schema changes
(top-level keys, part, app and hook keys, platforms versus architectures),
added, changed, deprecated or removed plugins and extensions, build
behaviour (craftctl versus snapcraftctl, build environments, remote build,
lifecycle steps, overrides), command-line changes, store and channel
behaviour exposed through snapcraft, and base-specific differences. Prefer
statements about what IS true now over narrative about what changed. Do not
record performance anecdotes, internal implementation details or marketing.
For schema or plugin changes, you must demonstrate an example snippet of
snapcraft.yaml.

You work inside a fresh Ubuntu virtual machine with root access via the
shell tool. It starts with few tools installed; install what you need (for
example: apt-get update && apt-get install -y curl ca-certificates git,
then snap install snapcraft --classic, and snap install lxd && lxd init
--auto so that snapcraft can build in LXD containers inside the machine)
before use. Network access is available. Keep commands non-interactive.

Sources of truth, in order of preference:
  1. https://documentation.ubuntu.com/snapcraft/stable/release-notes/
     (release notes per minor version, with their dates)
  2. https://github.com/canonical/snapcraft/releases
     (every release with its date and notes; the API at
     https://api.github.com/repos/canonical/snapcraft/releases lists them
     with "published_at")
  3. https://documentation.ubuntu.com/snapcraft/stable/reference/
     (the snapcraft.yaml schema, plugins, extensions and commands)
  4. The installed snapcraft itself: snapcraft --version, snapcraft help,
     and a real build of a small project on the base in question.

Record each axiom with the propose_axiom tool. The clock has two values in
order: the base number the statement holds from (22 for core22, 24 for
core24) and, as the second value, the snapcraft release that introduced the
behaviour (e.g. "8.3.0"); give only the base when the statement is true of
every snapcraft release that supports that base. Use the release date from
the release notes as the time. Set the source to the URL you read it from.
Only propose axioms you have actually read in a source or verified by
running snapcraft; never propose from memory alone.

Hard limit: never propose an axiom dated on or before ` + axiom.GlobalEpoch.Format("2006-01-02") + `, the global epoch.
Everything older is assumed to be prior knowledge of every model, and the
propose_axiom tool rejects it. Releases before that date are out of scope
even when the log is empty.`
}

func (Subject) UpdateTask(existing []axiom.Axiom) string {
	var b strings.Builder
	latest := latestVersion(existing)
	fmt.Fprintf(&b, "The log currently holds %d axioms about snapcraft.", len(existing))
	if latest != "" {
		fmt.Fprintf(&b, " The newest snapcraft release it covers is %s.", latest)
	}
	b.WriteString(`

Your task:
1. From the release notes, identify every snapcraft release that is newer
   than the newest release the log covers and was released
   after ` + axiom.GlobalEpoch.Format("2006-01-02") + `, the global epoch (count minor releases; fold
   point releases into them unless one changes behaviour). If there are
   none, say so and stop.
2. Work from the oldest release to the newest. For each one, read its
   release notes in full and, for behaviour that depends on the base, note
   which bases it applies to. For each foundational change, propose one
   axiom with the right base number and release as its clock. Aim for
   completeness over brevity: schema, plugin and extension changes matter
   most, then build behaviour and commands.
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
			fmt.Fprintf(&b, "- [%s %s] %s\n", a.Time.Format("2006-01-02"), clockLabel(a), a.Statement)
		}
	}
	return b.String()
}

func clockLabel(a axiom.Axiom) string {
	parts := []string{"core" + a.Clock["base"]}
	if v := a.Clock["version"]; v != "" {
		parts = append(parts, v)
	}
	return strings.Join(parts, " ")
}

func (Subject) ConfirmInstructions() string {
	return `You are an independent fact checker for statements about snapcraft, the
tool that builds snaps. You work inside a fresh Ubuntu virtual machine with
root access via the shell tool; it starts with few tools installed, so
install what you need (for example curl, ca-certificates and git, then
snap install snapcraft --classic and snap install lxd && lxd init --auto)
before use. Network access is available.

Verify the statement against primary sources: the release notes
(https://documentation.ubuntu.com/snapcraft/stable/release-notes/ or the
GitHub release), the reference documentation for the schema, plugins and
extensions, or by building a small snap with the stated base using the
installed snapcraft (snap refresh snapcraft --channel=<track>/stable
selects a release). Confirm only if the statement, exactly as written, is
accurate for the stated base and release and is not misleading or
materially incomplete. If you cannot verify it, do not confirm it.

When you do not confirm, distinguish two cases. If the statement is close but
inaccurate (wrong base or release, wrong key or plugin name, overstated,
missing a condition that matters) and you have verified what is actually
true, provide the corrected statement as the correction: one or two
self-contained sentences in the same style, true for the stated base and
release, that you would confirm yourself. If the statement is unfounded,
that is nothing like it is true or you could not establish what is true,
leave every correction empty and say so in the reason. When the fault is
the base, release or the date rather than the wording, keep the statement
as it is and give the correct date (YYYY-MM-DD) and clock (the base
number, 24 for core24, then the release) instead; both may be combined with a corrected statement. If
the statement bundles several distinct facts, or is right in one part and
wrong in another, give a split instead: the separate axioms that should
replace it, each verified, each with its own date and clock only where they
differ from the original.`
}

func (Subject) ConfirmTask(a axiom.Axiom) string {
	return fmt.Sprintf(`Verify this proposed axiom.

Base: core%s (clock value %s)
Snapcraft release: %s (released %s)
Statement: %s
Claimed source: %s

Investigate, then call the submit_answer tool with confirmed true or false,
a one or two sentence reason citing what you checked, and, only when the
statement is inaccurate rather than unfounded, a correction giving the
verified statement that should replace it and/or the correct date and clock
when the release or date is what is wrong, or a split into several axioms
when it bundles distinct facts. Clock values are the base number and the
release, for example ["24", "8.3.0"].`,
		a.Clock["base"], a.Clock["base"], orAny(a.Clock["version"]), a.Time.Format("2006-01-02"), a.Statement, a.Source)
}

func orAny(v string) string {
	if v == "" {
		return "any supporting the base"
	}
	return v
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
