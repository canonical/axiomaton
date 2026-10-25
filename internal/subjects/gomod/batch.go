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

package gomod

import (
	"sort"
	"strings"
	"time"

	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"
)

// Commit is one commit of a module's history, as the update agent reads it
// from git.
type Commit struct {
	Hash string
	Time time.Time
	// GoVersion is the go directive of the module's go.mod at this commit,
	// e.g. "1.24". It may be empty when unknown.
	GoVersion string
}

// Release is a batch of commits treated as one version of a module that
// has no tagged releases. Its Version is the pseudo-version of the last
// commit, which is what a go.mod requiring that commit records, so
// project clocks and axiom clocks compare directly.
type Release struct {
	Version string
	Time    time.Time
	// First and Last are the commits the batch spans, oldest first.
	First, Last string
	Commits     int
	GoVersion   string
	// Reason says which rule opened the batch.
	Reason string
}

// MaxGap is the pause between commits after which the next commit opens a
// new release regardless of anything else.
const MaxGap = 31 * 24 * time.Hour

// Batch groups the commit history of a module without tagged releases into
// discrete releases. Commits are sorted by time and a new release opens
// when any of these holds:
//
//   - the go directive in go.mod changed since the previous commit
//     (the module moved to a new language level);
//   - more than MaxGap passed since the previous commit;
//   - the previous commit is in a different calendar month (UTC), so there
//     is at most one release per month unless the go directive changed.
//
// base is the newest tagged version that precedes the commits, or "" when
// the module has never been tagged, and shapes the pseudo-versions the way
// the go command would (v0.0.0-..., or v1.2.4-0.... after v1.2.3).
func Batch(commits []Commit, base string) []Release {
	sorted := append([]Commit(nil), commits...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Time.Before(sorted[j].Time) })
	major := "v0"
	if base != "" && semver.IsValid(base) {
		major = semver.Major(base)
	} else {
		base = ""
	}
	var out []Release
	var prev *Commit
	for i := range sorted {
		c := sorted[i]
		c.Time = c.Time.UTC()
		reason := ""
		switch {
		case prev == nil:
			reason = "first commit"
		case c.GoVersion != prev.GoVersion && (c.GoVersion != "" || prev.GoVersion != ""):
			reason = "go directive changed to " + c.GoVersion
		case c.Time.Sub(prev.Time) > MaxGap:
			reason = "more than a month since the previous commit"
		case c.Time.Year() != prev.Time.Year() || c.Time.Month() != prev.Time.Month():
			reason = "new calendar month"
		}
		if reason != "" {
			out = append(out, Release{First: c.Hash, GoVersion: c.GoVersion, Reason: reason})
		}
		r := &out[len(out)-1]
		r.Last = c.Hash
		r.Time = c.Time
		r.Commits++
		r.Version = module.PseudoVersion(major, base, c.Time, shortHash(c.Hash))
		prev = &sorted[i]
	}
	return out
}

func shortHash(h string) string {
	h = strings.TrimSpace(h)
	if len(h) > 12 {
		return h[:12]
	}
	return h
}
