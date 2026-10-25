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

// Package axiom defines the core data model: axioms, subjects and the
// vector clocks that order them.
package axiom

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"
)

// GlobalEpoch is the earliest cutoff ax ever reports from: the cutoff of last
// resort when nothing is known about a model, and the point before which no
// axiom can appear in any answer. Update agents need not record anything
// older.
var GlobalEpoch = time.Date(2023, time.August, 1, 0, 0, 0, 0, time.UTC)

// Axiom is a statement considered true for a subject at a given clock.
type Axiom struct {
	// ID is a content hash of the subject, clock and statement.
	ID string `json:"id"`
	// Subject names the subject this axiom belongs to, e.g. "go".
	Subject string `json:"subject"`
	// Time is the primary clock: the UTC instant from which the statement holds.
	Time time.Time `json:"time"`
	// Clock holds the subject specific sub-clock values keyed by dimension name.
	Clock map[string]string `json:"clock,omitempty"`
	// Statement is the axiom itself.
	Statement string `json:"statement"`
	// Source optionally records where the statement came from.
	Source string `json:"source,omitempty"`
	// Added records when the axiom was appended to a log.
	Added time.Time `json:"added,omitzero"`
	// By records who appended the axiom, e.g. "user" or "agent".
	By string `json:"by,omitempty"`

	// Origin is the log file the axiom was read from. It is not serialised.
	Origin string `json:"-"`
	// Seq is the position of the axiom in the merged logs. It breaks ties
	// between axioms with equal clocks so output follows log order.
	Seq int `json:"-"`
}

// ComputeID derives the content hash identifier of the axiom.
func (a Axiom) ComputeID() string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\n%s\n", a.Subject, a.Time.UTC().Format(time.RFC3339))
	keys := make([]string, 0, len(a.Clock))
	for k := range a.Clock {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(h, "%s=%s\n", k, a.Clock[k])
	}
	fmt.Fprintf(h, "%s", strings.TrimSpace(a.Statement))
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// Normalise fills in derived fields and canonicalises values.
func (a *Axiom) Normalise() {
	a.Subject = strings.TrimSpace(a.Subject)
	a.Statement = strings.TrimSpace(a.Statement)
	a.Time = a.Time.UTC()
	if len(a.Clock) == 0 {
		a.Clock = nil
	}
	a.ID = a.ComputeID()
}

// Validate reports whether the axiom is well formed.
func (a Axiom) Validate() error {
	if a.Subject == "" {
		return fmt.Errorf("axiom has no subject")
	}
	if a.Time.IsZero() {
		return fmt.Errorf("axiom %q has no time", a.Statement)
	}
	if strings.TrimSpace(a.Statement) == "" {
		return fmt.Errorf("axiom for %s at %s has an empty statement", a.Subject, a.Time.Format(time.RFC3339))
	}
	return nil
}

// ClockString renders the sub-clock values in shape order for display.
func (a Axiom) ClockString(shape []ClockShape) string {
	vals := ClockValues(a.Clock, shape)
	return strings.Join(vals, " ")
}

// ClockValues returns the sub-clock values in shape order. Dimensions not
// present in the clock are omitted. Dimensions not in the shape are appended
// in name order so nothing is silently dropped.
func ClockValues(clock map[string]string, shape []ClockShape) []string {
	var vals []string
	seen := map[string]bool{}
	for _, d := range shape {
		if v, ok := clock[d.Name]; ok {
			vals = append(vals, v)
			seen[d.Name] = true
		}
	}
	var rest []string
	for k := range clock {
		if !seen[k] {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	for _, k := range rest {
		vals = append(vals, clock[k])
	}
	return vals
}

// Newer reports whether a is strictly newer than b. The primary time clock
// wins; sub-clocks in shape order break ties. Incomparable sub-clock values
// are treated as equal.
func Newer(a, b Axiom, shape []ClockShape) bool {
	switch {
	case a.Time.After(b.Time):
		return true
	case a.Time.Before(b.Time):
		return false
	}
	for _, d := range shape {
		av, aok := a.Clock[d.Name]
		bv, bok := b.Clock[d.Name]
		if !aok || !bok {
			continue
		}
		switch d.Compare(av, bv) {
		case Greater:
			return true
		case Less:
			return false
		}
	}
	return false
}

// Before is a total order suitable for sorting: oldest first, with the ID as
// a final tie break so output is deterministic.
func Before(a, b Axiom, shape []ClockShape) bool {
	if Newer(a, b, shape) {
		return false
	}
	if Newer(b, a, shape) {
		return true
	}
	if a.Seq != b.Seq {
		return a.Seq < b.Seq
	}
	return a.ID < b.ID
}

// Sort orders axioms oldest first.
func Sort(axioms []Axiom, shape []ClockShape) {
	sort.SliceStable(axioms, func(i, j int) bool { return Before(axioms[i], axioms[j], shape) })
}

// Applies reports whether the axiom's sub-clock is at or before the given
// query clock in every dimension the query constrains. A dimension missing
// from either side is unconstrained. Incomparable values do not apply.
func Applies(a Axiom, query map[string]string, shape []ClockShape) bool {
	for _, d := range shape {
		qv, qok := query[d.Name]
		av, aok := a.Clock[d.Name]
		if !qok || !aok {
			continue
		}
		switch d.Compare(av, qv) {
		case Less, Equal:
		default:
			return false
		}
	}
	return true
}
