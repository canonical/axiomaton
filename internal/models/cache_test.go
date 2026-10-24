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

package models

import (
	"context"
	"testing"
)

type countingEmbedder struct{ calls int }

func (c *countingEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	c.calls++
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = []float32{float32(len(t)), 1}
	}
	return out, nil
}
func (c *countingEmbedder) Name() string { return "test/model" }

type countingDecider struct{ calls int }

func (c *countingDecider) Decide(context.Context, Pair) (Decision, error) {
	c.calls++
	return Decision{Relation: Restates, Confidence: 1}, nil
}
func (c *countingDecider) Name() string { return "jev" }

func TestCachedEmbedder(t *testing.T) {
	inner := &countingEmbedder{}
	ce := CachedEmbedder{Embedder: inner, Cache: Cache{Dir: t.TempDir()}}
	ctx := context.Background()
	v1, err := ce.Embed(ctx, []string{"aa", "bbb"})
	if err != nil || inner.calls != 1 || v1[1][0] != 3 {
		t.Fatalf("first: %v %v calls=%d", v1, err, inner.calls)
	}
	v2, err := ce.Embed(ctx, []string{"bbb", "cccc", "aa"})
	if err != nil || inner.calls != 2 || v2[0][0] != 3 || v2[1][0] != 4 || v2[2][0] != 2 {
		t.Fatalf("second: %v %v calls=%d", v2, err, inner.calls)
	}
	ce.Embed(ctx, []string{"cccc"})
	if inner.calls != 2 {
		t.Fatal("third call should be fully cached")
	}
	if Cosine([]float32{1, 0}, []float32{1, 0}) < 0.999 || Cosine([]float32{1, 0}, []float32{0, 1}) != 0 {
		t.Fatal("cosine")
	}
}

func TestCachedDecider(t *testing.T) {
	inner := &countingDecider{}
	cd := CachedDecider{Decider: inner, Cache: Cache{Dir: t.TempDir()}}
	p := Pair{Subject: "s", Newer: "a", Older: "b"}
	cd.Decide(context.Background(), p)
	cd.Decide(context.Background(), p)
	if inner.calls != 1 {
		t.Fatalf("calls = %d", inner.calls)
	}
	cd.Decide(context.Background(), Pair{Subject: "s", Newer: "b", Older: "a"})
	if inner.calls != 2 {
		t.Fatal("swapped pair should miss")
	}
	if err := cd.Cache.Clear(); err != nil {
		t.Fatal(err)
	}
	cd.Decide(context.Background(), p)
	if inner.calls != 3 {
		t.Fatal("cleared cache should miss")
	}
}
