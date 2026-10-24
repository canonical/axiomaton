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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Cache is a content addressed on-disk store of model results.
type Cache struct {
	Dir string
}

func (c Cache) path(kind, model, key string) string {
	safe := strings.NewReplacer("/", "_", ":", "_", " ", "_").Replace(model)
	return filepath.Join(c.Dir, kind, safe, key[:2], key+".json")
}

func (c Cache) get(kind, model, key string, v any) bool {
	if c.Dir == "" {
		return false
	}
	data, err := os.ReadFile(c.path(kind, model, key))
	if err != nil {
		return false
	}
	return json.Unmarshal(data, v) == nil
}

func (c Cache) put(kind, model, key string, v any) error {
	if c.Dir == "" {
		return nil
	}
	p := c.path(kind, model, key)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".tmp-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	tmp.Close()
	return os.Rename(tmp.Name(), p)
}

// Clear removes the entire cache directory.
func (c Cache) Clear() error {
	if c.Dir == "" {
		return nil
	}
	err := os.RemoveAll(c.Dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

func hashKey(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// CachedEmbedder wraps an Embedder with the cache.
type CachedEmbedder struct {
	Embedder
	Cache Cache
}

// Embed looks each text up in the cache and only sends misses to the model.
func (c CachedEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	var missIdx []int
	var miss []string
	for i, t := range texts {
		var v []float32
		if c.Cache.get("embeddings", c.Name(), hashKey(t), &v) && len(v) > 0 {
			out[i] = v
			continue
		}
		missIdx = append(missIdx, i)
		miss = append(miss, t)
	}
	if len(miss) == 0 {
		return out, nil
	}
	vecs, err := c.Embedder.Embed(ctx, miss)
	if err != nil {
		return nil, err
	}
	if len(vecs) != len(miss) {
		return nil, fmt.Errorf("embedder returned %d vectors for %d inputs", len(vecs), len(miss))
	}
	for j, i := range missIdx {
		out[i] = vecs[j]
		if err := c.Cache.put("embeddings", c.Name(), hashKey(texts[i]), vecs[j]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// CachedDecider wraps a Decider with the cache.
type CachedDecider struct {
	Decider
	Cache Cache
}

// Decide returns a cached verdict for the pair when one exists.
func (c CachedDecider) Decide(ctx context.Context, p Pair) (Decision, error) {
	key := hashKey(p.Subject, p.Newer, p.NewerClock, p.Older, p.OlderClock)
	var d Decision
	if c.Cache.get("decisions", c.Name(), key, &d) && d.Relation != "" {
		return d, nil
	}
	d, err := c.Decider.Decide(ctx, p)
	if err != nil {
		return d, err
	}
	return d, c.Cache.put("decisions", c.Name(), key, d)
}

// CachedReranker wraps a Reranker with the cache.
type CachedReranker struct {
	Reranker
	Cache Cache
}

// Rerank caches by the full query and document set.
func (c CachedReranker) Rerank(ctx context.Context, query string, docs []string, topN int) ([]Ranked, error) {
	key := hashKey(append([]string{query, fmt.Sprint(topN)}, docs...)...)
	var r []Ranked
	if c.Cache.get("rerank", c.Name(), key, &r) && r != nil {
		return r, nil
	}
	r, err := c.Reranker.Rerank(ctx, query, docs, topN)
	if err != nil {
		return nil, err
	}
	if r == nil {
		r = []Ranked{}
	}
	return r, c.Cache.put("rerank", c.Name(), key, r)
}
