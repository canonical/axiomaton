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

package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/canonical/go-cnf/cnfast"
)

// Roles lists the model roles in documentation order.
var Roles = []string{RoleEmbedding, RoleRerank, RoleDecision, RoleSummary, RoleAgent}

// RoleNotes describes what each role is used for.
var RoleNotes = map[string]string{
	RoleEmbedding: "similarity search over axioms",
	RoleRerank:    "optional; refines the nearest neighbours",
	RoleDecision:  "OpenRouter Decisions API, e.g. typesafe/jev",
	RoleSummary:   "ax since -s",
	RoleAgent:     "tool using model for ax update",
}

// Render serialises the configuration as commented cnf for ax.cnf. Every
// section is written out so the file documents the tunables.
func Render(u User, now time.Time) ([]byte, error) {
	header := Lines(
		fmt.Sprintf("ax.cnf written by ax setup on %s.", now.UTC().Format("2006-01-02")),
		"Reference: https://github.com/canonical/axiomaton#readme",
	)
	root := BlockMap()
	add := func(item *cnfast.Item) { root.Items = append(root.Items, item) }

	names := make([]string, 0, len(u.Providers))
	for n := range u.Providers {
		names = append(names, n)
	}
	sort.Strings(names)
	providers := BlockMap()
	for _, n := range names {
		p := u.Providers[n]
		m := InlineMap()
		if p.BaseURL != "" {
			m.Items = append(m.Items, Pair("base-url", Str(p.BaseURL), ""))
		}
		if p.APIKey != "" {
			m.Items = append(m.Items, Pair("api-key", Str(p.APIKey), ""))
		}
		if p.APIKeyEnv != "" {
			m.Items = append(m.Items, Pair("api-key-env", Str(p.APIKeyEnv), ""))
		}
		providers.Items = append(providers.Items, Pair(n, m, ""))
	}
	add(Pair("providers", providers, ""))

	// Roles without a model are noted in comments. Inside the block they
	// sit above the next configured role; a block holding only comments is
	// not valid cnf, so with no model at all the map is inline and the
	// notes go above the item that follows it.
	models := BlockMap()
	var notes []string
	for _, role := range Roles {
		m := u.Models[role]
		if !m.Configured() {
			notes = append(notes, fmt.Sprintf("%s: not configured (%s)", role, RoleNotes[role]))
			continue
		}
		fields := InlineMap(Pair("model", Str(m.Model), ""))
		if m.Provider != "" {
			fields.Items = append(fields.Items, Pair("provider", Str(m.Provider), ""))
		}
		if m.BaseURL != "" {
			fields.Items = append(fields.Items, Pair("base-url", Str(m.BaseURL), ""))
		}
		if m.APIKey != "" {
			fields.Items = append(fields.Items, Pair("api-key", Str(m.APIKey), ""))
		}
		if m.APIKeyEnv != "" {
			fields.Items = append(fields.Items, Pair("api-key-env", Str(m.APIKeyEnv), ""))
		}
		item := Pair(role, fields, RoleNotes[role])
		item.PreCmt = Lines(notes...)
		notes = nil
		models.Items = append(models.Items, item)
	}
	modelsItem := Pair("models", models, "")
	if models.Block {
		modelsItem.FootCmt = Lines(notes...)
		notes = nil
	}
	add(modelsItem)

	budget := Pair("summary-budget", Int(u.SummaryBudget), "tokens for ax since -s")
	budget.PreCmt = Lines(notes...)
	add(budget)
	add(Pair("search", BlockMap(
		Pair("candidates", Int(u.Search.Candidates), "nearest neighbours per axiom"),
		Pair("min-similarity", Float(u.Search.MinSimilarity), "cosine floor"),
		Pair("rerank-top-n", Int(u.Search.RerankTopN), ""),
		Pair("min-relevance", Float(u.Search.MinRelevance), ""),
		Pair("decision-threshold", Float(u.Search.DecisionThreshold), "minimum probability to accept a verdict"),
	), ""))
	add(Pair("retry", BlockMap(
		Pair("max-attempts", Int(u.Retry.MaxAttempts), "tries per model request"),
		Pair("initial-backoff", Float(u.Retry.InitialBackoff), "seconds; doubles each attempt with jitter"),
		Pair("max-backoff", Float(u.Retry.MaxBackoff), "seconds"),
	), ""))
	add(Pair("update", BlockMap(
		Pair("backend", Str(u.Update.Backend), "auto (podman if installed, else lxd), podman, lxd or lxd-vm"),
		Pair("image", Str(u.Update.Image), "podman image"),
		Pair("lxd-image", Str(u.Update.LXDImage), "LXD image for containers and virtual machines"),
		Pair("max-iterations", Int(u.Update.MaxIterations), "tool calls per agent run"),
		Pair("exec-timeout", Int(u.Update.ExecTimeout), "seconds per command in the container"),
		Pair("confirmations", Int(u.Update.Confirmations), "confirmation agents run at once (default: half the CPUs)"),
	), ""))
	disabled := Pair("disabled", Bool(u.Proxy.Disabled), "")
	disabled.PreCmt = Lines(
		"Containers have no network of their own; everything goes through a",
		"caching proxy on this host that intercepts HTTPS with its own CA",
		"(installed in each container) and allows only HTTP and HTTPS out, so",
		"apt, Go modules, PyPI, git and toolchain tarballs are fetched once and",
		"SSH cannot leave. Disabling it gives containers podman's default network.",
	)
	add(Pair("proxy", BlockMap(
		disabled,
		Pair("max-object-mb", Int(u.Proxy.MaxObjectMB), ""),
	), ""))
	file := Pair("file", Str(u.Sync.File), "context file relative to the project, e.g. CLAUDE.local.md")
	file.PreCmt = Lines(
		"ax sync writes the changes since a cutoff into an agent context file",
		"in the project. Defaults for projects it has not seen; each project's",
		"own answers are remembered under projects.",
	)
	sync := BlockMap(
		file,
		Pair("model", Str(u.Sync.Model), "report since this model's knowledge cutoff, or"),
		Pair("since", Str(u.Sync.Since), "since this date (YYYY-MM-DD)"),
	)
	if len(u.Sync.Projects) > 0 {
		dirs := make([]string, 0, len(u.Sync.Projects))
		for d := range u.Sync.Projects {
			dirs = append(dirs, d)
		}
		sort.Strings(dirs)
		projects := BlockMap()
		for _, d := range dirs {
			p := u.Sync.Projects[d]
			m := InlineMap()
			if p.File != "" {
				m.Items = append(m.Items, Pair("file", Str(p.File), ""))
			}
			if p.Model != "" {
				m.Items = append(m.Items, Pair("model", Str(p.Model), ""))
			}
			if p.Since != "" {
				m.Items = append(m.Items, Pair("since", Str(p.Since), ""))
			}
			projects.Items = append(projects.Items, Pair(d, m, ""))
		}
		sync.Items = append(sync.Items, Pair("projects", projects, ""))
	}
	add(Pair("sync", sync, ""))
	consent := BlockMap()
	for _, key := range ConsentKeys {
		consent.Items = append(consent.Items, Pair(key, Bool(u.Consent[key]), ConsentNotes[key]))
	}
	consentItem := Pair("consent", consent, "")
	consentItem.PreCmt = Lines(
		"ax asks before it first runs podman, launches LXD instances or creates",
		"the isolated LXD network; answering \"always\" records true here.",
	)
	add(consentItem)
	return Document(header, root)
}

// InlineKey reports whether an API key is stored in the configuration itself
// rather than read from the environment.
func (u User) InlineKey() bool {
	for _, p := range u.Providers {
		if p.APIKey != "" {
			return true
		}
	}
	for _, m := range u.Models {
		if m.APIKey != "" {
			return true
		}
	}
	return false
}

// WriteUser renders u to path, creating parent directories. Files holding an
// inline API key are made readable by the owner only.
func WriteUser(path string, u User, now time.Time) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	perm := os.FileMode(0o644)
	if u.InlineKey() {
		perm = 0o600
	}
	data, err := Render(u, now)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, data, perm); err != nil {
		return err
	}
	// WriteFile leaves the mode of an existing file alone.
	return os.Chmod(path, perm)
}
