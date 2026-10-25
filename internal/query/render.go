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

package query

import (
	"fmt"
	"strings"

	"github.com/canonical/axiomaton/internal/axiom"
)

// Render produces deterministic Markdown for one or more results. Subjects
// with nothing to report are left out; when that is every subject, a single
// line says so. Dates and sources are deliberately absent: the output is
// meant for a model's context, where the cutoff and provenance only add
// noise, and the clock value alone places each statement.
func Render(results []Result) string {
	var b strings.Builder
	b.WriteString("# Axiom changes\n\n")
	var shown, quiet []Result
	for _, r := range results {
		if r.Reportable() {
			shown = append(shown, r)
		} else {
			quiet = append(quiet, r)
		}
	}
	if len(shown) == 0 {
		names := make([]string, len(quiet))
		for i, r := range quiet {
			names[i] = r.Subject.Name + clockSuffix(r)
		}
		fmt.Fprintf(&b, "No changes for %s.\n", strings.Join(names, ", "))
		return b.String()
	}
	fmt.Fprintf(&b, "Each item below is a statement that became true after the cutoff. "+
		"Where a newer statement supersedes an older one, the older statement should no longer be relied on.\n")
	for _, r := range shown {
		b.WriteString("\n")
		renderResult(&b, r)
	}
	return b.String()
}

// clockSuffix is the " (version <= 1.26.1)" part of a subject heading, or
// "" when the request had no clock.
func clockSuffix(r Result) string {
	if len(r.Clock) == 0 {
		return ""
	}
	var dims []string
	for _, d := range r.Subject.Shape {
		if v, ok := r.Clock[d.Name]; ok {
			dims = append(dims, fmt.Sprintf("%s <= %s", d.Name, v))
		}
	}
	if len(dims) == 0 {
		return ""
	}
	return " (" + strings.Join(dims, ", ") + ")"
}

func renderResult(b *strings.Builder, r Result) {
	shape := r.Subject.Shape
	fmt.Fprintf(b, "## %s%s\n\n", r.Subject.Name, clockSuffix(r))
	fmt.Fprintf(b, "%d changed, %d added, %d restated; %d axiom(s) were already true at the cutoff.\n",
		len(r.Changed), len(r.Added), len(r.Restated), r.Prior)
	for _, n := range r.Notes {
		fmt.Fprintf(b, "\n_%s_\n", n)
	}

	if len(r.Changed) > 0 {
		b.WriteString("\n### Changed\n\n")
		for _, e := range r.Changed {
			renderEntry(b, e, shape)
			for _, old := range e.Supersedes {
				fmt.Fprintf(b, "  - Supersedes%s %s\n", label(old, shape), oneLine(old.Statement))
			}
		}
	}
	if len(r.Added) > 0 {
		b.WriteString("\n### Added\n\n")
		for _, e := range r.Added {
			renderEntry(b, e, shape)
		}
	}
	if len(r.Restated) > 0 {
		fmt.Fprintf(b, "\n### Restated\n\n%d newer axiom(s) only repeat statements that were already true and are omitted.\n", len(r.Restated))
	}
}

func renderEntry(b *strings.Builder, e Entry, shape []axiom.ClockShape) {
	if l := label(e.Axiom, shape); l != "" {
		fmt.Fprintf(b, "- **%s** %s\n", strings.TrimSpace(l), oneLine(e.Axiom.Statement))
		return
	}
	fmt.Fprintf(b, "- %s\n", oneLine(e.Axiom.Statement))
}

// label is the axiom's clock in brackets with a leading space, e.g.
// " [1.26.0]", or "" for an axiom without a clock.
func label(a axiom.Axiom, shape []axiom.ClockShape) string {
	values := axiom.ClockValues(a.Clock, shape)
	if len(values) == 0 {
		return ""
	}
	return " [" + strings.Join(values, " ") + "]"
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
