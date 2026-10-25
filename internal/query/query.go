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

// Package query evaluates "what changed since" questions over axioms.
package query

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/canonical/axiomaton/internal/axiom"
	"github.com/canonical/axiomaton/internal/axiomlog"
	"github.com/canonical/axiomaton/internal/config"
	"github.com/canonical/axiomaton/internal/models"
)

// Request asks what changed for a subject after Since, restricted to axioms
// whose sub-clock is at or before Clock.
type Request struct {
	Since   time.Time
	Subject axiom.Subject
	Clock   map[string]string
}

// Resolver holds the optional models used to detect contradictions. With a
// nil Embedder or Decider the evaluation is purely deterministic.
type Resolver struct {
	Embedder models.Embedder
	Reranker models.Reranker
	Decider  models.Decider
	Search   config.Search
	// Log receives progress messages; nil discards them.
	Log func(format string, args ...any)
}

func (r Resolver) logf(format string, args ...any) {
	if r.Log != nil {
		r.Log(format, args...)
	}
}

// Entry is one newer axiom together with what it displaces.
type Entry struct {
	Axiom axiom.Axiom
	// Supersedes lists older axioms this one contradicts, oldest first.
	Supersedes []axiom.Axiom
	// Restates lists prior axioms this one merely repeats.
	Restates []axiom.Axiom
}

// Result is the evaluated answer for one subject.
type Result struct {
	Request
	// Changed are newer axioms that supersede something.
	Changed []Entry
	// Added are newer axioms with no detected conflict.
	Added []Entry
	// Restated are newer axioms that only repeat prior knowledge.
	Restated []Entry
	// Prior is the number of axioms already true at Since.
	Prior int
	// Fresh is the number of axioms newer than Since that were considered.
	Fresh int
	// Resolved reports whether model based contradiction resolution ran.
	Resolved bool
	// Notes carries caveats for the reader.
	Notes []string
}

// Reportable reports whether the result has something for a reader: at
// least one changed or added axiom. Restated axioms alone are not reported.
func (r Result) Reportable() bool {
	return len(r.Changed) > 0 || len(r.Added) > 0
}

// Empty reports whether nothing changed.
func (r Result) Empty() bool {
	return len(r.Changed) == 0 && len(r.Added) == 0 && len(r.Restated) == 0
}

type poolItem struct {
	axiom axiom.Axiom
	vec   []float32
	entry *Entry // nil for prior axioms
}

// Evaluate answers the request over the given axioms (any subject; they
// are filtered here).
func Evaluate(ctx context.Context, axioms []axiom.Axiom, req Request, res Resolver) (Result, error) {
	out := Result{Request: req}
	shape := req.Subject.Shape
	var prior, fresh []axiom.Axiom
	for _, a := range axiomlog.Filter(axioms, req.Subject.Name) {
		if !axiom.Applies(a, req.Clock, shape) {
			continue
		}
		if a.Time.After(req.Since) {
			fresh = append(fresh, a)
		} else {
			prior = append(prior, a)
		}
	}
	axiom.Sort(prior, shape)
	axiom.Sort(fresh, shape)
	out.Prior, out.Fresh = len(prior), len(fresh)
	if len(fresh) == 0 {
		return out, nil
	}

	entries := make([]*Entry, 0, len(fresh))
	folded := map[*Entry]bool{}

	if res.Embedder == nil || res.Decider == nil {
		out.Notes = append(out.Notes, "Contradiction resolution skipped: no embedding and decision model configured, so superseded axioms are not detected.")
		for _, f := range fresh {
			out.Added = append(out.Added, Entry{Axiom: f})
		}
		return out, nil
	}
	out.Resolved = true

	texts := make([]string, 0, len(prior)+len(fresh))
	for _, a := range prior {
		texts = append(texts, a.Statement)
	}
	for _, a := range fresh {
		texts = append(texts, a.Statement)
	}
	res.logf("embedding %d axioms for %s", len(texts), req.Subject.Name)
	vecs, err := res.Embedder.Embed(ctx, texts)
	if err != nil {
		return out, fmt.Errorf("embed: %w", err)
	}
	pool := make([]poolItem, 0, len(texts))
	for i, a := range prior {
		pool = append(pool, poolItem{axiom: a, vec: vecs[i]})
	}

	search := res.Search
	if search.Candidates <= 0 {
		search.Candidates = 8
	}
	if search.DecisionThreshold <= 0 {
		search.DecisionThreshold = 0.5
	}

	for i, f := range fresh {
		e := &Entry{Axiom: f}
		fv := vecs[len(prior)+i]
		cands := nearest(pool, fv, search.Candidates, search.MinSimilarity)
		if res.Reranker != nil && len(cands) > 1 {
			cands, err = rerank(ctx, res, f.Statement, pool, cands, search)
			if err != nil {
				return out, fmt.Errorf("rerank: %w", err)
			}
		}
		remove := map[int]bool{}
		for _, ci := range cands {
			c := pool[ci]
			pair := models.Pair{
				Subject:    req.Subject.Name,
				Newer:      f.Statement,
				NewerClock: clockLabel(f, shape),
				Older:      c.axiom.Statement,
				OlderClock: clockLabel(c.axiom, shape),
			}
			res.logf("deciding %s vs %s", f.ID, c.axiom.ID)
			d, err := res.Decider.Decide(ctx, pair)
			if err != nil {
				return out, fmt.Errorf("decide: %w", err)
			}
			rel := accepted(d, search.DecisionThreshold)
			switch rel {
			case models.Contradicts:
				e.Supersedes = append(e.Supersedes, c.axiom)
				if c.entry != nil {
					e.Supersedes = append(e.Supersedes, c.entry.Supersedes...)
					folded[c.entry] = true
				}
				remove[ci] = true
			case models.Restates:
				if c.entry == nil {
					e.Restates = append(e.Restates, c.axiom)
				} else {
					// Two fresh axioms saying the same thing: keep the newer,
					// carry over what the older one displaced.
					e.Supersedes = append(e.Supersedes, c.entry.Supersedes...)
					e.Restates = append(e.Restates, c.entry.Restates...)
					folded[c.entry] = true
					remove[ci] = true
				}
			}
		}
		if len(remove) > 0 {
			kept := pool[:0]
			for j, p := range pool {
				if !remove[j] {
					kept = append(kept, p)
				}
			}
			pool = kept
		}
		pool = append(pool, poolItem{axiom: f, vec: fv, entry: e})
		entries = append(entries, e)
	}

	for _, e := range entries {
		if folded[e] {
			continue
		}
		axiom.Sort(e.Supersedes, shape)
		axiom.Sort(e.Restates, shape)
		switch {
		case len(e.Supersedes) > 0:
			out.Changed = append(out.Changed, *e)
		case len(e.Restates) > 0:
			out.Restated = append(out.Restated, *e)
		default:
			out.Added = append(out.Added, *e)
		}
	}
	return out, nil
}

// accepted applies the probability threshold to a decision.
func accepted(d models.Decision, threshold float64) models.Relation {
	p, ok := d.Probabilities[d.Relation]
	if !ok {
		p = d.Confidence
		if p == 0 {
			p = 1
		}
	}
	if d.Relation == models.Unrelated || p < threshold {
		return models.Unrelated
	}
	return d.Relation
}

// nearest returns indexes into pool of the k most similar items.
func nearest(pool []poolItem, v []float32, k int, minSim float64) []int {
	type scored struct {
		idx int
		sim float64
	}
	var s []scored
	for i, p := range pool {
		sim := models.Cosine(v, p.vec)
		if sim >= minSim {
			s = append(s, scored{i, sim})
		}
	}
	sort.SliceStable(s, func(i, j int) bool { return s[i].sim > s[j].sim })
	if len(s) > k {
		s = s[:k]
	}
	out := make([]int, len(s))
	for i, x := range s {
		out[i] = x.idx
	}
	return out
}

// rerank re-scores the candidate pool entries against the query statement
// and keeps the top entries above the relevance floor, best first.
func rerank(ctx context.Context, res Resolver, query string, pool []poolItem, cands []int, search config.Search) ([]int, error) {
	docs := make([]string, len(cands))
	for i, ci := range cands {
		docs[i] = pool[ci].axiom.Statement
	}
	topN := search.RerankTopN
	if topN <= 0 || topN > len(docs) {
		topN = len(docs)
	}
	ranked, err := res.Reranker.Rerank(ctx, query, docs, topN)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].Score > ranked[j].Score })
	out := make([]int, 0, len(ranked))
	for _, r := range ranked {
		if r.Index < 0 || r.Index >= len(cands) || r.Score < search.MinRelevance {
			continue
		}
		out = append(out, cands[r.Index])
	}
	return out, nil
}

func clockLabel(a axiom.Axiom, shape []axiom.ClockShape) string {
	parts := []string{a.Time.Format("2006-01-02")}
	parts = append(parts, axiom.ClockValues(a.Clock, shape)...)
	return strings.Join(parts, " ")
}
