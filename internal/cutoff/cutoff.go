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

// Package cutoff finds and remembers knowledge cutoff dates for models that
// OpenRouter does not publish one for.
package cutoff

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/microsoft/agent-framework-go/tool"

	"github.com/canonical/axiomaton/internal/update"
)

// Method records how a cutoff was obtained.
type Method string

const (
	// MethodAgent means a containerised agent found the date in a source.
	MethodAgent Method = "agent"
	// MethodEstimate means the date was derived from the release date.
	MethodEstimate Method = "estimate"
	// MethodManual means the user wrote the record by hand.
	MethodManual Method = "manual"
	// MethodEpoch means nothing was known and GlobalEpoch was used.
	MethodEpoch Method = "epoch"
)

// DefaultLeadDays is used when the catalogue offers no model with both a
// release date and a cutoff to measure a lead from (roughly nine months).
const DefaultLeadDays = 274

// GlobalEpoch is the cutoff of last resort, used when a model has neither a
// published cutoff nor a release date to estimate from. It predates every
// axiom in the built-in logs, so everything is reported as new.
var GlobalEpoch = time.Date(2023, time.August, 1, 0, 0, 0, 0, time.UTC)

// Record is a remembered cutoff for one model.
type Record struct {
	Model  string    `json:"model"`
	Cutoff string    `json:"cutoff"` // YYYY-MM-DD
	Method Method    `json:"method"`
	Source string    `json:"source,omitempty"`
	Found  time.Time `json:"found"`
}

// Date parses the record's cutoff.
func (r Record) Date() (time.Time, error) {
	return time.Parse("2006-01-02", r.Cutoff)
}

// Store is a JSON file of records keyed by model id.
type Store struct {
	Path string
}

func (s Store) load() (map[string]Record, error) {
	out := map[string]Record{}
	data, err := os.ReadFile(s.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("parse %s: %w", s.Path, err)
	}
	return out, nil
}

// Get returns the stored record for model, if any.
func (s Store) Get(model string) (Record, bool, error) {
	all, err := s.load()
	if err != nil {
		return Record{}, false, err
	}
	r, ok := all[model]
	return r, ok, nil
}

// Put stores r, replacing any record for the same model.
func (s Store) Put(r Record) error {
	all, err := s.load()
	if err != nil {
		return err
	}
	all[r.Model] = r
	data, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(s.Path, append(data, '\n'), 0o644)
}

// Observation is a model for which both dates are known.
type Observation struct {
	Model    string
	Released time.Time
	Cutoff   time.Time
}

// LeadPercentile is the percentile of release-to-cutoff gaps used as the
// lead: high enough to be conservative, low enough to ignore outliers such
// as models listed years after their cutoff.
const LeadPercentile = 90

// Lead is how far a model's knowledge is assumed to end before its release
// when nothing better is known.
type Lead struct {
	Days int
	// Model is the observation the lead came from, or empty for the default.
	Model string
	// Sample is how many observations the lead was drawn from.
	Sample int
}

// PercentileLead returns the LeadPercentile-th percentile of the
// release-to-cutoff gaps among obs (nearest rank method). Observations whose
// cutoff is after the release are ignored. With no usable observation the
// default lead is returned.
func PercentileLead(obs []Observation) Lead {
	type gap struct {
		days  int
		model string
	}
	var gaps []gap
	for _, o := range obs {
		if o.Released.IsZero() || o.Cutoff.IsZero() {
			continue
		}
		days := int(o.Released.Sub(o.Cutoff).Hours() / 24)
		if days < 0 {
			continue
		}
		gaps = append(gaps, gap{days, o.Model})
	}
	if len(gaps) == 0 {
		return Lead{Days: DefaultLeadDays}
	}
	sort.Slice(gaps, func(i, j int) bool { return gaps[i].days < gaps[j].days })
	rank := (LeadPercentile*len(gaps) + 99) / 100 // ceil(p/100 * n), 1-based
	if rank < 1 {
		rank = 1
	}
	g := gaps[rank-1]
	return Lead{Days: g.days, Model: g.model, Sample: len(gaps)}
}

// Estimate derives a cutoff from a release date by subtracting the lead,
// truncated to the day.
func Estimate(released time.Time, lead Lead) time.Time {
	t := released.UTC().AddDate(0, 0, -lead.Days)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// Describe explains where the lead came from.
func (l Lead) Describe() string {
	if l.Model == "" {
		return fmt.Sprintf("%d days, the default", l.Days)
	}
	return fmt.Sprintf("%d days, the %dth percentile release-to-cutoff gap across %d OpenRouter models, seen for %s", l.Days, LeadPercentile, l.Sample, l.Model)
}

// Finding is the structured answer the research agent returns.
type Finding struct {
	Found  bool   `json:"found" jsonschema:"True only if a source states the model's training data or knowledge cutoff."`
	Cutoff string `json:"cutoff" jsonschema:"The cutoff as YYYY-MM-DD, or YYYY-MM when only the month is known. Empty if not found."`
	Source string `json:"source" jsonschema:"URL of the page that states the cutoff. Empty if not found."`
	Notes  string `json:"notes" jsonschema:"One sentence on what was checked."`
}

// Model describes what the agent is asked about.
type Model struct {
	ID       string
	Name     string
	Released time.Time
}

// Researcher runs a containerised agent to look up a model's cutoff.
type Researcher struct {
	Runtime     update.Runtime
	Agent       update.Agent
	ExecTimeout time.Duration
	Log         func(format string, args ...any)
}

func (r Researcher) logf(format string, args ...any) {
	if r.Log != nil {
		r.Log(format, args...)
	}
}

// Instructions is the research agent's system prompt.
const Instructions = `You are a careful researcher running inside a fresh Ubuntu container with
network access via the "shell" tool. Nothing is preinstalled; apt-get install
what you need (curl, ca-certificates, python3, jq are usually enough).

Your job is to find the knowledge cutoff (also called training data cutoff)
of one language model. Trust only primary or authoritative sources: the
vendor's model documentation or model card, the vendor's release announcement,
the Hugging Face model card, or a reputable reference that cites one of
those. Marketing pages that do not state a date do not count. Do not guess
from the release date. If sources disagree, prefer the vendor's documentation
and say so in notes.

Finish by answering with the structured output: found, cutoff (YYYY-MM-DD, or
YYYY-MM if only the month is published), source URL and notes.`

// Task is the research agent's user prompt for m.
func Task(m Model) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Find the knowledge cutoff of the model with OpenRouter id %q", m.ID)
	if m.Name != "" {
		fmt.Fprintf(&b, " (display name %q)", m.Name)
	}
	b.WriteString(".\n")
	if !m.Released.IsZero() {
		fmt.Fprintf(&b, "OpenRouter lists it as released on %s; the cutoff must be before that.\n", m.Released.Format("2006-01-02"))
	}
	vendor, slug, _ := strings.Cut(m.ID, "/")
	fmt.Fprintf(&b, "The vendor segment is %q and the model slug is %q. ", vendor, slug)
	b.WriteString("Good starting points: the vendor's model documentation, huggingface.co/<vendor>/<slug>, " +
		"and the vendor's blog or changelog. Fetch pages with curl -L and search their text for " +
		"\"cutoff\", \"knowledge\", \"training data\".\n")
	return b.String()
}

// Find starts a container and asks the agent for m's cutoff. It returns a
// MethodAgent record when a dated source is found, or ok=false when the
// agent gives up. Errors are infrastructure failures, not negative results.
func (r Researcher) Find(ctx context.Context, m Model) (Record, bool, error) {
	c, err := r.Runtime.Start(ctx)
	if err != nil {
		return Record{}, false, err
	}
	defer c.Close(ctx)
	shell, err := update.ShellTool(c, r.ExecTimeout, r.Log)
	if err != nil {
		return Record{}, false, err
	}
	var f Finding
	text, err := r.Agent.Run(ctx, Instructions, Task(m), []tool.Tool{shell}, &f)
	if err != nil {
		return Record{}, false, fmt.Errorf("research agent: %w", err)
	}
	if f.Notes == "" {
		f.Notes = text
	}
	r.logf("research agent: found=%v cutoff=%q source=%q notes=%s", f.Found, f.Cutoff, f.Source, f.Notes)
	if !f.Found {
		return Record{}, false, nil
	}
	date, err := ParseDate(f.Cutoff)
	if err != nil {
		r.logf("research agent returned an unusable date %q; ignoring", f.Cutoff)
		return Record{}, false, nil
	}
	if !m.Released.IsZero() && date.After(m.Released) {
		r.logf("research agent returned %s, after the release date %s; ignoring", date.Format("2006-01-02"), m.Released.Format("2006-01-02"))
		return Record{}, false, nil
	}
	return Record{Model: m.ID, Cutoff: date.Format("2006-01-02"), Method: MethodAgent, Source: f.Source, Found: time.Now().UTC()}, true, nil
}

var monthOnly = regexp.MustCompile(`^(\d{4})-(\d{2})$`)

// ParseDate accepts YYYY-MM-DD, or YYYY-MM which is read as the last day of
// that month, matching how OpenRouter states month level cutoffs.
func ParseDate(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t, nil
	}
	if m := monthOnly.FindStringSubmatch(s); m != nil {
		t, err := time.Parse("2006-01", s)
		if err == nil {
			return t.AddDate(0, 1, -1), nil
		}
	}
	return time.Time{}, fmt.Errorf("cannot parse cutoff %q", s)
}
