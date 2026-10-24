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

// Package models defines the model roles ax depends on. Implementations live
// in sub-packages; the query engine only sees these interfaces.
package models

import (
	"context"
	"math"
)

// Relation is the verdict of the decision model for a pair of axioms.
type Relation string

const (
	// Contradicts means the newer axiom supersedes the older one.
	Contradicts Relation = "contradicts"
	// Restates means both axioms assert the same thing.
	Restates Relation = "restates"
	// Unrelated means the axioms can both stand.
	Unrelated Relation = "unrelated"
)

// Decision is a typed verdict with the probability mass behind it.
type Decision struct {
	Relation      Relation             `json:"relation"`
	Confidence    float64              `json:"confidence"`
	Probabilities map[Relation]float64 `json:"probabilities,omitempty"`
}

// Pair is the input to a decision: the newer and the older statement for a
// subject, with their display clocks.
type Pair struct {
	Subject    string
	Newer      string
	NewerClock string
	Older      string
	OlderClock string
}

// Embedder turns statements into vectors.
type Embedder interface {
	// Embed returns one vector per input, in order.
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	// Name identifies the model so caches are not shared across models.
	Name() string
}

// Ranked is a reranked document.
type Ranked struct {
	Index int
	Score float64
}

// Reranker scores documents against a query.
type Reranker interface {
	Rerank(ctx context.Context, query string, docs []string, topN int) ([]Ranked, error)
	Name() string
}

// Decider classifies the relationship between two axioms.
type Decider interface {
	Decide(ctx context.Context, p Pair) (Decision, error)
	Name() string
}

// Summariser condenses markdown to roughly budget tokens.
type Summariser interface {
	Summarise(ctx context.Context, markdown string, budget int) (string, error)
}

// Cosine returns the cosine similarity of two vectors.
func Cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
