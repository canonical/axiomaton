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

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/canonical/axiomaton/internal/config"
	"github.com/canonical/axiomaton/internal/cutoff"
	"github.com/canonical/axiomaton/internal/models/openrouter"
)

// modelCacheTTL bounds how long the OpenRouter model catalogue is reused.
const modelCacheTTL = 24 * time.Hour

// forCmd is `ax for <model> ...`: like since, with the cutoff taken from the
// model's published knowledge cutoff on OpenRouter.
func (a *app) forCmd(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("ax for", flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	summarise := fs.Bool("s", false, "summarise the output with the summary model")
	fs.BoolVar(summarise, "summarise", false, "alias for -s")
	refresh := fs.Bool("refresh", false, "fetch the model catalogue again instead of using the cached copy")
	fs.Usage = func() {
		fmt.Fprintln(a.stderr, "usage: ax for [-s] [-refresh] <model> [subject [clock values...]]")
		fs.PrintDefaults()
	}
	rest, err := parseInterleaved(fs, args)
	if err != nil {
		return err
	}
	if len(rest) == 0 {
		fs.Usage()
		return errors.New("for: a model name is required, e.g. openai/gpt-5-mini")
	}
	cfg, err := config.LoadUser(a.configPath)
	if err != nil {
		return err
	}
	catalogue, err := a.modelCatalogue(ctx, cfg, *refresh)
	if err != nil {
		return err
	}
	m, date, err := findCutoff(catalogue, rest[0])
	var missing *noCutoffError
	switch {
	case errors.As(err, &missing):
		date, err = a.resolveMissingCutoff(ctx, cfg, catalogue, m, *refresh)
		if err != nil {
			return err
		}
	case err != nil:
		return err
	default:
		fmt.Fprintf(a.stderr, "ax: %s (%s) knowledge cutoff %s\n", m.ID, m.Name, date.Format("2006-01-02"))
	}
	return a.report(ctx, date, rest[1:], *summarise)
}

// research looks a cutoff up with a containerised agent. It is a variable
// so tests can stand in for podman and the model.
var research = func(ctx context.Context, a *app, cfg config.User, m cutoff.Model) (cutoff.Record, bool, error) {
	ep, err := cfg.Resolve(config.RoleAgent)
	if err != nil {
		return cutoff.Record{}, false, err
	}
	rt, ag := a.agentParts(cfg, ep, "")
	r := cutoff.Researcher{Runtime: rt, Agent: ag, ExecTimeout: time.Duration(cfg.Update.ExecTimeout) * time.Second, Log: a.logf}
	return r.Find(ctx, m)
}

// resolveMissingCutoff handles a model without a published cutoff: a stored
// answer is reused, otherwise an agent searches for one, and failing that
// the release date minus nine months is used. Whatever is found is stored.
func (a *app) resolveMissingCutoff(ctx context.Context, cfg config.User, catalogue []openrouter.ModelInfo, m openrouter.ModelInfo, refresh bool) (time.Time, error) {
	store := cutoff.Store{Path: filepath.Join(config.DataDir(), "cutoffs.json")}
	if !refresh {
		rec, ok, err := store.Get(m.ID)
		if err != nil {
			return time.Time{}, err
		}
		if ok {
			date, err := rec.Date()
			if err != nil {
				return time.Time{}, fmt.Errorf("%s: stored cutoff %q: %w", store.Path, rec.Cutoff, err)
			}
			fmt.Fprintf(a.stderr, "ax: %s (%s) knowledge cutoff %s (%s)\n", m.ID, m.Name, rec.Cutoff, describeRecord(rec))
			return date, nil
		}
	}
	var released time.Time
	if m.Created > 0 {
		released = time.Unix(m.Created, 0).UTC()
	}
	a.warnf("OpenRouter publishes no knowledge cutoff for %s", m.ID)

	switch _, err := cfg.Resolve(config.RoleAgent); {
	case a.offline:
		a.warnf("--offline: not searching for the cutoff with an agent")
	case err != nil:
		a.warnf("%v; not searching for the cutoff with an agent", err)
	default:
		fmt.Fprintf(a.stderr, "ax: searching for the knowledge cutoff of %s with a containerised agent; this can take a few minutes\n", m.ID)
		rec, ok, err := research(ctx, a, cfg, cutoff.Model{ID: m.ID, Name: m.Name, Released: released})
		switch {
		case err != nil:
			a.warnf("cutoff search failed: %v", err)
		case ok:
			if err := store.Put(rec); err != nil {
				a.warnf("could not store the cutoff: %v", err)
			}
			date, _ := rec.Date()
			fmt.Fprintf(a.stderr, "ax: %s (%s) knowledge cutoff %s (%s)\n", m.ID, m.Name, rec.Cutoff, describeRecord(rec))
			return date, nil
		default:
			a.warnf("the agent could not find a published cutoff for %s", m.ID)
		}
	}

	var rec cutoff.Record
	if released.IsZero() {
		rec = cutoff.Record{Model: m.ID, Cutoff: cutoff.GlobalEpoch.Format("2006-01-02"), Method: cutoff.MethodEpoch,
			Source: "no release date on OpenRouter; global epoch", Found: time.Now().UTC()}
	} else {
		lead := cutoff.PercentileLead(observations(catalogue))
		rec = cutoff.Record{Model: m.ID, Cutoff: cutoff.Estimate(released, lead).Format("2006-01-02"), Method: cutoff.MethodEstimate,
			Source: fmt.Sprintf("OpenRouter release date %s minus %s", released.Format("2006-01-02"), lead.Describe()), Found: time.Now().UTC()}
	}
	if err := store.Put(rec); err != nil {
		a.warnf("could not store the cutoff: %v", err)
	}
	date, _ := rec.Date()
	a.warnf("%s (%s) knowledge cutoff %s as %s (%s); edit %s to correct it", m.ID, m.Name, verb(rec.Method), rec.Cutoff, rec.Source, store.Path)
	return date, nil
}

// observations collects every catalogue model with both a release date and
// a published cutoff.
func observations(catalogue []openrouter.ModelInfo) []cutoff.Observation {
	var out []cutoff.Observation
	for _, m := range catalogue {
		if m.Created <= 0 || m.KnowledgeCutoff == "" {
			continue
		}
		k, err := time.Parse("2006-01-02", m.KnowledgeCutoff)
		if err != nil {
			continue
		}
		out = append(out, cutoff.Observation{Model: m.ID, Released: time.Unix(m.Created, 0).UTC(), Cutoff: k})
	}
	return out
}

func verb(m cutoff.Method) string {
	if m == cutoff.MethodEpoch {
		return "defaulted"
	}
	return "estimated"
}

func describeRecord(r cutoff.Record) string {
	switch r.Method {
	case cutoff.MethodAgent:
		return "found by agent at " + r.Source
	case cutoff.MethodEstimate:
		return "estimated: " + r.Source
	case cutoff.MethodEpoch:
		return "global epoch: " + r.Source
	case cutoff.MethodManual:
		return "from cutoffs.json"
	}
	return string(r.Method)
}

// modelCatalogue returns the OpenRouter model list, cached on disk for a
// day per provider host.
func (a *app) modelCatalogue(ctx context.Context, cfg config.User, refresh bool) ([]openrouter.ModelInfo, error) {
	ep, err := cfg.ProviderEndpoint(defaultProvider)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(config.CacheDir(), "models", safeName(ep.BaseURL)+".json")
	type cached struct {
		Fetched time.Time              `json:"fetched"`
		Models  []openrouter.ModelInfo `json:"models"`
	}
	if !refresh && !a.offline {
		if data, err := os.ReadFile(path); err == nil {
			var c cached
			if json.Unmarshal(data, &c) == nil && time.Since(c.Fetched) < modelCacheTTL && len(c.Models) > 0 && hasReleaseDates(c.Models) {
				a.logf("using model catalogue cached %s ago", time.Since(c.Fetched).Round(time.Minute))
				return c.Models, nil
			}
		}
	}
	if a.offline {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("--offline: no cached model catalogue at %s; run ax for once online", path)
		}
		var c cached
		if err := json.Unmarshal(data, &c); err != nil || len(c.Models) == 0 {
			return nil, fmt.Errorf("--offline: cached model catalogue at %s is unreadable", path)
		}
		return c.Models, nil
	}
	a.logf("fetching model catalogue from %s", ep.BaseURL)
	ms, err := a.client(cfg, ep).ListModels(ctx)
	if err != nil {
		return nil, fmt.Errorf("list models: %w", err)
	}
	if data, err := json.Marshal(cached{Fetched: time.Now().UTC(), Models: ms}); err == nil {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err == nil {
			_ = os.WriteFile(path, data, 0o644)
		}
	}
	return ms, nil
}

// hasReleaseDates guards against catalogues cached by older builds that did
// not keep the created field.
func hasReleaseDates(ms []openrouter.ModelInfo) bool {
	for _, m := range ms {
		if m.Created > 0 {
			return true
		}
	}
	return false
}

func safeName(s string) string {
	s = strings.TrimPrefix(strings.TrimPrefix(s, "https://"), "http://")
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-':
			return r
		}
		return '_'
	}, s)
}

// findCutoff locates name in the catalogue and returns the model and its
// knowledge cutoff. Matching tries, in order: exact id, canonical slug, a
// case-insensitive id or display name, a bare slug without the vendor
// prefix, and the id without a ":variant" suffix. Rolling aliases are
// followed to their target, and a variant without a published cutoff falls
// back to its base model.
func findCutoff(catalogue []openrouter.ModelInfo, name string) (openrouter.ModelInfo, time.Time, error) {
	m, ok := matchModel(catalogue, name)
	if !ok {
		return openrouter.ModelInfo{}, time.Time{}, fmt.Errorf("model %q not found on OpenRouter%s", name, suggest(catalogue, name))
	}
	for hops := 0; m.AliasTarget != nil && m.AliasTarget.Slug != "" && hops < 3; hops++ {
		target, ok := matchModel(catalogue, m.AliasTarget.Slug)
		if !ok {
			break
		}
		m = target
	}
	if m.KnowledgeCutoff == "" {
		if base, _, ok := strings.Cut(m.ID, ":"); ok {
			if bm, found := matchModel(catalogue, base); found && bm.KnowledgeCutoff != "" {
				m = bm
			}
		}
	}
	if m.KnowledgeCutoff == "" {
		return m, time.Time{}, &noCutoffError{Model: m.ID}
	}
	t, err := time.Parse("2006-01-02", m.KnowledgeCutoff)
	if err != nil {
		return m, time.Time{}, fmt.Errorf("%s: unexpected knowledge cutoff %q", m.ID, m.KnowledgeCutoff)
	}
	return m, t, nil
}

// noCutoffError reports a model that exists but has no published cutoff.
type noCutoffError struct{ Model string }

func (e *noCutoffError) Error() string {
	return fmt.Sprintf("OpenRouter publishes no knowledge cutoff for %s", e.Model)
}

func matchModel(catalogue []openrouter.ModelInfo, name string) (openrouter.ModelInfo, bool) {
	name = strings.TrimSpace(name)
	lower := strings.ToLower(name)
	passes := []func(m openrouter.ModelInfo) bool{
		func(m openrouter.ModelInfo) bool { return m.ID == name },
		func(m openrouter.ModelInfo) bool { return m.CanonicalSlug == name },
		func(m openrouter.ModelInfo) bool {
			return strings.ToLower(m.ID) == lower || strings.ToLower(m.Name) == lower || strings.ToLower(m.CanonicalSlug) == lower
		},
		func(m openrouter.ModelInfo) bool {
			_, slug, _ := strings.Cut(m.ID, "/")
			return !strings.Contains(name, "/") && strings.ToLower(slug) == lower
		},
		func(m openrouter.ModelInfo) bool {
			base, _, _ := strings.Cut(strings.ToLower(m.ID), ":")
			return base == lower
		},
	}
	for _, match := range passes {
		var hits []openrouter.ModelInfo
		for _, m := range catalogue {
			if match(m) {
				hits = append(hits, m)
			}
		}
		if len(hits) == 1 {
			return hits[0], true
		}
		if len(hits) > 1 {
			// Prefer the plain id over variants when several match.
			sort.Slice(hits, func(i, j int) bool { return len(hits[i].ID) < len(hits[j].ID) })
			return hits[0], true
		}
	}
	// A variant the catalogue does not list separately, e.g. model:thinking,
	// resolves to its base model.
	if base, _, ok := strings.Cut(name, ":"); ok && base != "" {
		return matchModel(catalogue, base)
	}
	return openrouter.ModelInfo{}, false
}

// suggest lists a few ids containing the last path element of name.
func suggest(catalogue []openrouter.ModelInfo, name string) string {
	needle := strings.ToLower(name)
	if i := strings.LastIndex(needle, "/"); i >= 0 {
		needle = needle[i+1:]
	}
	needle, _, _ = strings.Cut(needle, ":")
	if len(needle) < 3 {
		return ""
	}
	var ids []string
	for _, m := range catalogue {
		if strings.Contains(strings.ToLower(m.ID), needle) && !strings.Contains(m.ID, ":") {
			ids = append(ids, m.ID)
		}
	}
	if len(ids) == 0 {
		return ""
	}
	sort.Strings(ids)
	if len(ids) > 6 {
		ids = append(ids[:6], "...")
	}
	return "; did you mean " + strings.Join(ids, ", ") + "?"
}
