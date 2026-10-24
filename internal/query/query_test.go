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
	"context"
	"strings"
	"testing"
	"time"

	"github.com/canonical/axiomaton/internal/axiom"
	"github.com/canonical/axiomaton/internal/config"
	"github.com/canonical/axiomaton/internal/models"
)

var shape = []axiom.ClockShape{{Name: "version", Kind: axiom.KindGoVersion}}

func mk(date, ver, stmt string) axiom.Axiom {
	t, _ := time.Parse("2006-01-02", date)
	a := axiom.Axiom{Subject: "golang", Time: t, Clock: map[string]string{"version": ver}, Statement: stmt}
	a.Normalise()
	return a
}

func day(s string) time.Time {
	t, _ := time.Parse("2006-01-02", s)
	return t
}

// fakeEmbedder maps statements to hand picked vectors by keyword.
type fakeEmbedder struct{}

func (fakeEmbedder) Name() string { return "fake" }
func (fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		switch {
		case strings.Contains(t, "loop"):
			out[i] = []float32{1, 0, 0}
		case strings.Contains(t, "generic"):
			out[i] = []float32{0, 1, 0}
		default:
			out[i] = []float32{0, 0, 1}
		}
	}
	return out, nil
}

// fakeDecider says statements about loops contradict, generics restate.
type fakeDecider struct{ calls []models.Pair }

func (f *fakeDecider) Name() string { return "fake" }
func (f *fakeDecider) Decide(_ context.Context, p models.Pair) (models.Decision, error) {
	f.calls = append(f.calls, p)
	switch {
	case strings.Contains(p.Newer, "loop") && strings.Contains(p.Older, "loop"):
		return models.Decision{Relation: models.Contradicts, Probabilities: map[models.Relation]float64{models.Contradicts: 0.9}}, nil
	case strings.Contains(p.Newer, "generic") && strings.Contains(p.Older, "generic"):
		return models.Decision{Relation: models.Restates, Probabilities: map[models.Relation]float64{models.Restates: 0.8}}, nil
	}
	return models.Decision{Relation: models.Unrelated, Probabilities: map[models.Relation]float64{models.Unrelated: 0.95}}, nil
}

type fakeReranker struct{ called bool }

func (f *fakeReranker) Name() string { return "fake" }
func (f *fakeReranker) Rerank(_ context.Context, q string, docs []string, topN int) ([]models.Ranked, error) {
	f.called = true
	out := []models.Ranked{}
	for i := range docs {
		out = append(out, models.Ranked{Index: i, Score: 0.5})
	}
	return out, nil
}

func corpus() []axiom.Axiom {
	return []axiom.Axiom{
		mk("2012-03-28", "1.0", "a for loop variable is shared across iterations"),
		mk("2012-03-28", "1.0", "closures in a for loop body observe the loop variable's final value"),
		mk("2022-03-15", "1.18", "generic functions and types are supported"),
		mk("2024-02-06", "1.22", "each iteration of a for loop has its own loop variable"),
		mk("2025-08-12", "1.25", "testing/synctest is available"),
		mk("2026-02-10", "1.26", "generic types may refer to themselves in constraints and generic code is supported"),
		mk("2026-08-19", "1.27", "methods may declare their own type parameters"),
		{Subject: "rust", Time: day("2026-01-01"), Statement: "rust fact"},
	}
}

func TestDeterministicOnly(t *testing.T) {
	req := Request{Since: day("2023-01-01"), Subject: axiom.Subject{Name: "golang", Shape: shape}, Clock: map[string]string{"version": "1.26.1"}}
	r, err := Evaluate(context.Background(), corpus(), req, Resolver{})
	if err != nil {
		t.Fatal(err)
	}
	if r.Resolved || len(r.Added) != 3 || len(r.Changed) != 0 || r.Prior != 3 {
		t.Fatalf("got %+v", r)
	}
	if r.Added[0].Axiom.Clock["version"] != "1.22" || r.Added[2].Axiom.Clock["version"] != "1.26" {
		t.Fatalf("order/filter wrong: %+v", r.Added)
	}
	md := Render(req.Since, []Result{r})
	if !strings.Contains(md, "## golang (version <= 1.26.1)") || !strings.Contains(md, "### Added") || !strings.Contains(md, "resolution skipped") {
		t.Fatalf("markdown:\n%s", md)
	}
	if strings.Contains(md, "1.27") || strings.Contains(md, "rust") {
		t.Fatalf("leaked excluded axioms:\n%s", md)
	}
}

func TestResolution(t *testing.T) {
	dec := &fakeDecider{}
	rr := &fakeReranker{}
	res := Resolver{Embedder: fakeEmbedder{}, Decider: dec, Reranker: rr, Search: config.Search{Candidates: 4, MinSimilarity: 0.5, DecisionThreshold: 0.5}}
	req := Request{Since: day("2023-01-01"), Subject: axiom.Subject{Name: "golang", Shape: shape}}
	r, err := Evaluate(context.Background(), corpus(), req, res)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Resolved {
		t.Fatal("should have resolved")
	}
	if len(r.Changed) != 1 || !strings.Contains(r.Changed[0].Axiom.Statement, "own loop variable") || len(r.Changed[0].Supersedes) != 2 {
		t.Fatalf("changed: %+v", r.Changed)
	}
	if len(r.Restated) != 1 || !strings.Contains(r.Restated[0].Axiom.Statement, "refer to themselves") {
		t.Fatalf("restated: %+v", r.Restated)
	}
	// 1.25 synctest and 1.27 generic methods are added. The 1.27 statement
	// does not contain "generic" so it is unrelated to the others.
	if len(r.Added) != 2 {
		t.Fatalf("added: %+v", r.Added)
	}
	if !rr.called {
		t.Fatal("reranker not used")
	}
	// Dissimilar vectors (cosine 0) must never reach the decider.
	for _, c := range dec.calls {
		if strings.Contains(c.Newer, "synctest") {
			t.Fatalf("synctest should have had no candidates: %+v", c)
		}
	}
	md := Render(req.Since, []Result{r})
	for _, want := range []string{"### Changed", "Supersedes [2012-03-28 1.0]", "### Added", "### Restated", "1 changed, 2 added, 1 restated"} {
		if !strings.Contains(md, want) {
			t.Fatalf("missing %q in:\n%s", want, md)
		}
	}
}

func TestFreshFolding(t *testing.T) {
	// Two fresh axioms about loops: the second contradicts the first, which
	// itself contradicted a prior one. The chain should fold into one entry.
	ax := []axiom.Axiom{
		mk("2020-01-01", "1.14", "a for loop variable is shared across iterations"),
		mk("2024-02-06", "1.22", "each iteration of a for loop has its own loop variable"),
		mk("2026-02-10", "1.26", "loop variables are per iteration and loop ranges may be functions"),
	}
	dec := &fakeDecider{}
	res := Resolver{Embedder: fakeEmbedder{}, Decider: dec, Search: config.Search{Candidates: 4, DecisionThreshold: 0.5}}
	req := Request{Since: day("2023-01-01"), Subject: axiom.Subject{Name: "golang", Shape: shape}}
	r, err := Evaluate(context.Background(), ax, req, res)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Changed) != 1 || len(r.Added) != 0 {
		t.Fatalf("expected one folded change, got %+v / %+v", r.Changed, r.Added)
	}
	if len(r.Changed[0].Supersedes) != 2 || r.Changed[0].Supersedes[0].Clock["version"] != "1.14" {
		t.Fatalf("supersedes chain: %+v", r.Changed[0].Supersedes)
	}
}

func TestThreshold(t *testing.T) {
	d := models.Decision{Relation: models.Contradicts, Probabilities: map[models.Relation]float64{models.Contradicts: 0.4}}
	if accepted(d, 0.5) != models.Unrelated {
		t.Fatal("low probability should be unrelated")
	}
	d = models.Decision{Relation: models.Restates, Confidence: 0.7}
	if accepted(d, 0.5) != models.Restates {
		t.Fatal("confidence fallback")
	}
}

func TestNoFresh(t *testing.T) {
	req := Request{Since: day("2030-01-01"), Subject: axiom.Subject{Name: "golang", Shape: shape}}
	r, _ := Evaluate(context.Background(), corpus(), req, Resolver{})
	if !r.Empty() || r.Prior != 7 {
		t.Fatalf("%+v", r)
	}
	if md := Render(req.Since, []Result{r}); !strings.Contains(md, "No changes") {
		t.Fatal(md)
	}
}
