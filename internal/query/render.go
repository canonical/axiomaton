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
	"time"

	"github.com/canonical/axiomaton/internal/axiom"
)

// Render produces deterministic Markdown for one or more results.
func Render(since time.Time, results []Result) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Axiom changes since %s\n\n", since.Format("2006-01-02"))
	fmt.Fprintf(&b, "Each item below is a statement that became true after the cutoff. "+
		"Where a newer statement supersedes an older one, the older statement should no longer be relied on.\n")
	for _, r := range results {
		b.WriteString("\n")
		renderResult(&b, r)
	}
	return b.String()
}

func renderResult(b *strings.Builder, r Result) {
	shape := r.Subject.Shape
	fmt.Fprintf(b, "## %s", r.Subject.Name)
	if len(r.Clock) > 0 {
		var dims []string
		for _, d := range shape {
			if v, ok := r.Clock[d.Name]; ok {
				dims = append(dims, fmt.Sprintf("%s <= %s", d.Name, v))
			}
		}
		if len(dims) > 0 {
			fmt.Fprintf(b, " (%s)", strings.Join(dims, ", "))
		}
	}
	b.WriteString("\n\n")

	if r.Empty() {
		fmt.Fprintf(b, "No changes. %d axiom(s) were already true at the cutoff.\n", r.Prior)
		for _, n := range r.Notes {
			fmt.Fprintf(b, "\n_%s_\n", n)
		}
		return
	}
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
				fmt.Fprintf(b, "  - Supersedes [%s]: %s\n", clockLabel(old, shape), oneLine(old.Statement))
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
	fmt.Fprintf(b, "- **[%s]** %s", clockLabel(e.Axiom, shape), oneLine(e.Axiom.Statement))
	if e.Axiom.Source != "" {
		fmt.Fprintf(b, " (source: %s)", e.Axiom.Source)
	}
	b.WriteString("\n")
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
