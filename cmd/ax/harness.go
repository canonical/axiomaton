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
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/canonical/axiomaton/internal/config"
	"github.com/canonical/axiomaton/internal/cutoff"
	"github.com/canonical/axiomaton/internal/models/openrouter"
)

var (
	//go:embed harness/pi.ts
	piExtension string
	//go:embed harness/opencode.ts
	opencodePlugin string
)

// harness is an agent that ax sync serves with an extension instead of a
// context file. The extension runs ax sync -print when the agent starts
// and puts the report in the system prompt, so it never goes stale.
type harness struct {
	name string
	// kind names what the harness calls its extensions, for messages.
	kind string
	// project is where the extension goes, relative to the project.
	project string
	// global returns where the extension goes for every project.
	global func() (string, error)
	source string
}

var harnesses = []harness{
	{
		name:    "pi",
		kind:    "extension",
		project: filepath.Join(".pi", "extensions", "axiomaton.ts"),
		global: func() (string, error) {
			dir := os.Getenv("PI_CODING_AGENT_DIR")
			if dir == "" {
				home, err := os.UserHomeDir()
				if err != nil {
					return "", err
				}
				dir = filepath.Join(home, ".pi", "agent")
			}
			return filepath.Join(dir, "extensions", "axiomaton.ts"), nil
		},
		source: piExtension,
	},
	{
		name:    "opencode",
		kind:    "plugin",
		project: filepath.Join(".opencode", "plugins", "axiomaton.ts"),
		global: func() (string, error) {
			// opencode reads $XDG_CONFIG_HOME/opencode, as ax reads its own.
			return filepath.Join(filepath.Dir(config.ConfigDir()), "opencode", "plugins", "axiomaton.ts"), nil
		},
		source: opencodePlugin,
	},
}

// findHarness looks a harness up by name, ignoring case.
func findHarness(name string) (harness, bool) {
	for _, h := range harnesses {
		if strings.EqualFold(name, h.name) {
			return h, true
		}
	}
	return harness{}, false
}

// path is where the extension goes for the project, or for the user.
func (h harness) path(project string, global bool) (string, error) {
	if global {
		return h.global()
	}
	return filepath.Join(project, h.project), nil
}

// render fills the ax command into the extension source: plain ax when it
// is on PATH, otherwise the path of this executable.
func (h harness) render(ax string) string {
	quoted, _ := json.Marshal(ax)
	return strings.Replace(h.source, "__AX__", string(quoted), 1)
}

// axCommand is how an installed extension should run ax, with a warning
// when that is not plain ax from PATH.
func axCommand() (cmd, warning string) {
	if _, err := exec.LookPath("ax"); err == nil {
		return "ax", ""
	}
	self, err := os.Executable()
	if err != nil {
		return "ax", "ax is not on PATH; set AXIOMATON_AX to its path in the agent's environment"
	}
	if resolved, err := filepath.EvalSymlinks(self); err == nil {
		self = resolved
	}
	return self, "ax is not on PATH, so the extension runs " + self + "; set AXIOMATON_AX to change it"
}

// installState says whether the extension at path is missing, current or
// an older version.
type installState int

const (
	notInstalled installState = iota
	installed
	outdated
)

func extensionState(path, want string) (installState, error) {
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return notInstalled, nil
	case err != nil:
		return notInstalled, err
	case bytes.Equal(data, []byte(want)):
		return installed, nil
	}
	return outdated, nil
}

// installHarness puts the harness extension in place after the user
// confirms, or with yes already given. It reports false when the user
// declined.
func (a *app) installHarness(ctx context.Context, h harness, path string, global, yes bool, p prompter) (bool, error) {
	ax, warning := axCommand()
	want := h.render(ax)
	state, err := extensionState(path, want)
	if err != nil {
		return false, err
	}
	shown := shortenFrom(a.dir, path)
	if state == installed {
		fmt.Fprintf(a.stderr, "ax: the %s %s at %s is up to date\n", h.name, h.kind, shown)
	} else {
		verb := "Install"
		if state == outdated {
			verb = "Replace"
		}
		scope := "this project"
		if global {
			scope = "every project"
		}
		question := fmt.Sprintf("%s the %s %s at %s? In %s it runs `%s sync -print` as %s starts and adds the axiom changes to the system prompt", verb, h.name, h.kind, shown, scope, ax, h.name)
		switch {
		case yes:
		case !p.ask:
			return false, fmt.Errorf("sync: installing the %s %s at %s needs confirmation; run ax sync interactively or pass -y", h.name, h.kind, path)
		default:
			ok, err := p.yesNo(question, true)
			if err != nil {
				return false, err
			}
			if !ok {
				fmt.Fprintf(a.stderr, "ax: the %s %s was not installed\n", h.name, h.kind)
				return false, nil
			}
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return false, err
		}
		if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
			return false, err
		}
		verb = "installed"
		if state == outdated {
			verb = "updated"
		}
		fmt.Fprintf(a.stderr, "ax: %s the %s %s at %s\n", verb, h.name, h.kind, shown)
	}
	if warning != "" {
		a.warnf("%s", warning)
	}
	if !global {
		a.warnIfCommitted(ctx, path)
		if h.name == "pi" {
			fmt.Fprintln(a.stderr, "ax: pi loads project extensions only once the project is trusted")
		}
	}
	return true, nil
}

// syncPrint writes the report an installed extension asks for to standard
// output. It never prompts or writes: the cutoff is that of the harness's
// active model when OpenRouter knows it, else what ax sync remembered for
// the nearest synced directory, else the sync defaults. With no cutoff it
// prints nothing.
func (a *app) syncPrint(ctx context.Context, cfg config.User, project, model string, summarise, refresh bool) error {
	var since time.Time
	if model != "" {
		if t, id, ok := a.harnessCutoff(ctx, cfg, model, refresh); ok {
			a.logf("using the knowledge cutoff of %s (%s) for %s", id, t.Format("2006-01-02"), model)
			since = t
		}
	}
	if since.IsZero() {
		entry, _ := nearestSyncEntry(cfg, project)
		if _, _, ok := entry.Cutoff(); !ok {
			entry = config.SyncProject{Model: cfg.Sync.Model, Since: cfg.Sync.Since}
		}
		v, isDate, ok := entry.Cutoff()
		if !ok {
			fmt.Fprintf(a.stderr, "ax: no cutoff for %s; run ax sync there once, or set sync.model or sync.since in %s\n", project, a.configPath)
			return nil
		}
		var err error
		if isDate {
			since, err = parseDate(v)
		} else {
			since, err = a.modelCutoff(ctx, cfg, v, refresh)
		}
		if err != nil {
			return err
		}
	}
	return a.report(ctx, since, nil, summarise)
}

// nearestSyncEntry finds what ax sync remembered for dir or the closest
// directory above it, so an agent started in a subdirectory gets the
// project's settings.
func nearestSyncEntry(cfg config.User, dir string) (config.SyncProject, bool) {
	for {
		if e, ok := cfg.Sync.Projects[dir]; ok {
			return e, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return config.SyncProject{}, false
		}
		dir = parent
	}
}

// harnessCutoff finds the published or stored knowledge cutoff of the
// model a harness reports as provider/id. It never estimates and never
// starts an agent: a model it cannot place gives ok false and the caller
// falls back to the remembered cutoff.
func (a *app) harnessCutoff(ctx context.Context, cfg config.User, ref string, refresh bool) (time.Time, string, bool) {
	catalogue, err := a.modelCatalogue(ctx, cfg, refresh)
	if err != nil {
		a.logf("model catalogue: %v", err)
		return time.Time{}, "", false
	}
	m, ok := mapHarnessModel(catalogue, ref)
	if !ok {
		a.logf("%s is not an OpenRouter model ax can place", ref)
		return time.Time{}, "", false
	}
	_, date, err := findCutoff(catalogue, m.ID)
	if err == nil {
		return date, m.ID, true
	}
	store := cutoff.Store{Path: filepath.Join(config.DataDir(), "cutoffs.json")}
	if rec, ok, _ := store.Get(m.ID); ok {
		if date, err := rec.Date(); err == nil {
			return date, m.ID, true
		}
	}
	a.logf("%v", err)
	return time.Time{}, "", false
}

// harnessProviders maps a harness's provider id to the OpenRouter
// namespace of its models. An empty namespace means the provider resells
// other vendors' models, so only the model id is matched.
var harnessProviders = map[string]string{
	"google-vertex":           "google",
	"google-vertex-anthropic": "anthropic",
	"github-copilot":          "",
	"opencode":                "",
}

var dateSuffix = regexp.MustCompile(`(-\d{8}|-\d{4}-\d{2}-\d{2}|-latest)$`)

// normModel reduces a model slug to a form in which harness ids and
// OpenRouter ids agree: lower case, no variant, date or -latest suffix,
// and dots as dashes (claude-sonnet-4-5-20250929, claude-sonnet-4-5@20250929
// and claude-sonnet-4.5 all become claude-sonnet-4-5).
func normModel(s string) string {
	s = strings.ToLower(s)
	s, _, _ = strings.Cut(s, ":")
	s, _, _ = strings.Cut(s, "@")
	s = dateSuffix.ReplaceAllString(s, "")
	return strings.ReplaceAll(s, ".", "-")
}

// mapHarnessModel places a harness model reference, provider/id, in the
// OpenRouter catalogue: directly for the openrouter provider, as
// provider/id, or by matching normalised slugs.
func mapHarnessModel(catalogue []openrouter.ModelInfo, ref string) (openrouter.ModelInfo, bool) {
	provider, id, ok := strings.Cut(ref, "/")
	if !ok || id == "" {
		return openrouter.ModelInfo{}, false
	}
	provider = strings.ToLower(provider)
	if provider == "openrouter" {
		return matchModel(catalogue, id)
	}
	namespace, known := harnessProviders[provider]
	if !known {
		namespace = provider
	}
	if namespace != "" {
		if m, ok := matchModel(catalogue, namespace+"/"+id); ok {
			return m, true
		}
	}
	slug := id
	if i := strings.LastIndex(slug, "/"); i >= 0 {
		slug = slug[i+1:]
	}
	want := normModel(slug)
	var hits []openrouter.ModelInfo
	for _, m := range catalogue {
		for _, cand := range []string{m.ID, m.CanonicalSlug} {
			ns, s, ok := strings.Cut(cand, "/")
			if !ok || (namespace != "" && !strings.EqualFold(ns, namespace)) {
				continue
			}
			if normModel(s) == want {
				hits = append(hits, m)
				break
			}
		}
	}
	if len(hits) == 0 {
		return openrouter.ModelInfo{}, false
	}
	// Prefer the plain id over variants such as :free.
	best := hits[0]
	for _, m := range hits[1:] {
		if len(m.ID) < len(best.ID) {
			best = m
		}
	}
	for _, m := range hits {
		if m.ID != best.ID && normModel(m.ID) != normModel(best.ID) {
			return openrouter.ModelInfo{}, false // ambiguous across vendors
		}
	}
	return best, true
}
