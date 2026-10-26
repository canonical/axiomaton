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

// Package config parses the user configuration (ax.cnf) and project
// configuration (axiomaton.cnf) files.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"runtime"
	"strings"
	"time"
)

// Model roles configured in ax.cnf.
const (
	RoleEmbedding = "embedding"
	RoleRerank    = "rerank"
	RoleDecision  = "decision"
	RoleSummary   = "summary"
	RoleAgent     = "agent"
)

// Provider holds connection defaults shared by models using that provider.
type Provider struct {
	BaseURL   string `json:"base-url"`
	APIKey    string `json:"api-key"`
	APIKeyEnv string `json:"api-key-env"`
}

// Model configures one model role.
type Model struct {
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	BaseURL   string `json:"base-url"`
	APIKey    string `json:"api-key"`
	APIKeyEnv string `json:"api-key-env"`
}

// Configured reports whether the role has a model set.
func (m Model) Configured() bool { return m.Model != "" }

// Search tunes similarity search and contradiction resolution.
type Search struct {
	// Candidates is how many nearest neighbours to consider per axiom.
	Candidates int `json:"candidates"`
	// MinSimilarity drops neighbours below this cosine similarity.
	MinSimilarity float64 `json:"min-similarity"`
	// RerankTopN keeps this many candidates after reranking.
	RerankTopN int `json:"rerank-top-n"`
	// MinRelevance drops reranked candidates below this relevance score.
	MinRelevance float64 `json:"min-relevance"`
	// DecisionThreshold is the minimum probability for a contradiction or
	// restatement verdict to be accepted; below it the pair is unrelated.
	DecisionThreshold float64 `json:"decision-threshold"`
}

// Update tunes the containerised update agents.
type Update struct {
	// Backend runs the agents: "auto" (podman if installed, else lxd),
	// "podman", "lxd" (system containers) or "lxd-vm" (virtual machines).
	Backend string `json:"backend"`
	// Image is the podman image.
	Image string `json:"image"`
	// LXDImage is the LXD image for containers and virtual machines, e.g.
	// ubuntu:26.04.
	LXDImage      string `json:"lxd-image"`
	MaxIterations int    `json:"max-iterations"`
	// ExecTimeout bounds each tool command inside the container, in seconds.
	ExecTimeout int `json:"exec-timeout"`
	// Confirmations is how many confirmation agents (each in its own
	// container) run at once.
	Confirmations int `json:"confirmations"`
}

// Proxy configures the caching proxy that agent containers download
// through.
type Proxy struct {
	// Disabled turns the proxy off; containers then get podman's default
	// network, with nothing cached and no restriction on what leaves.
	Disabled bool `json:"disabled"`
	// MaxObjectMB is the largest download kept in the cache.
	MaxObjectMB int `json:"max-object-mb"`
}

// Retry tunes how model requests are retried after rate limits and
// transient failures. Backoff is exponential with full jitter, capped at
// MaxBackoff, and a Retry-After header from the server is honoured.
type Retry struct {
	// MaxAttempts is the total number of tries per request.
	MaxAttempts int `json:"max-attempts"`
	// InitialBackoff is the first delay ceiling, in seconds.
	InitialBackoff float64 `json:"initial-backoff"`
	// MaxBackoff caps the delay between attempts, in seconds.
	MaxBackoff float64 `json:"max-backoff"`
}

// Initial and Max return the backoff bounds as durations.
func (r Retry) Initial() time.Duration { return time.Duration(r.InitialBackoff * float64(time.Second)) }
func (r Retry) Max() time.Duration     { return time.Duration(r.MaxBackoff * float64(time.Second)) }

// Sync configures ax sync, which keeps an agent context file in a project
// up to date with the output of ax since or ax for.
type Sync struct {
	// File is the context file to write in a project that has no entry
	// under Projects yet, relative to the project directory, for example
	// CLAUDE.local.md.
	File string `json:"file,omitempty"`
	// Model is the OpenRouter model whose knowledge cutoff is used for
	// projects that have no entry yet.
	Model string `json:"model,omitempty"`
	// Since is a fixed cutoff date (YYYY-MM-DD) used instead of Model.
	Since string `json:"since,omitempty"`
	// Projects remembers each project's answers, keyed by its absolute
	// directory, so later runs need no arguments.
	Projects map[string]SyncProject `json:"projects,omitempty"`
}

// SyncProject is what ax sync remembered for one project.
type SyncProject struct {
	File  string `json:"file,omitempty"`
	Model string `json:"model,omitempty"`
	Since string `json:"since,omitempty"`
}

// Cutoff returns the model or the date the project reports from, with
// which one it is. ok is false when neither is set.
func (s SyncProject) Cutoff() (value string, isDate, ok bool) {
	switch {
	case s.Since != "":
		return s.Since, true, true
	case s.Model != "":
		return s.Model, false, true
	}
	return "", false, false
}

// User is the parsed ax.cnf.
type User struct {
	Providers     map[string]Provider `json:"providers"`
	Models        map[string]Model    `json:"models"`
	SummaryBudget int                 `json:"summary-budget"`
	Search        Search              `json:"search"`
	Retry         Retry               `json:"retry"`
	Update        Update              `json:"update"`
	Proxy         Proxy               `json:"proxy"`
	Sync          Sync                `json:"sync"`

	// Path is where the config was read from, or empty if defaults are in use.
	Path string `json:"-"`
}

// DefaultConfirmations is half the CPUs, at least one: each confirmation
// runs a container and an agent, and the model endpoint is the other limit.
func DefaultConfirmations() int {
	return max(1, runtime.NumCPU()/2)
}

// DefaultUser returns the built in defaults.
func DefaultUser() User {
	return User{
		Providers: map[string]Provider{
			"openrouter": {BaseURL: "https://openrouter.ai/api", APIKeyEnv: "OPENROUTER_API_KEY"},
		},
		Models:        map[string]Model{},
		SummaryBudget: 1500,
		Search: Search{
			Candidates:        8,
			MinSimilarity:     0.35,
			RerankTopN:        5,
			MinRelevance:      0.1,
			DecisionThreshold: 0.5,
		},
		Retry: Retry{
			MaxAttempts:    8,
			InitialBackoff: 1,
			MaxBackoff:     60,
		},
		Update: Update{
			Backend:       "auto",
			Image:         "public.ecr.aws/ubuntu/ubuntu:latest",
			LXDImage:      "ubuntu:26.04",
			MaxIterations: 80,
			ExecTimeout:   300,
			Confirmations: DefaultConfirmations(),
		},
		Proxy: Proxy{
			MaxObjectMB: 1024,
		},
	}
}

// LoadUser reads ax.cnf from path. A missing file yields the defaults.
func LoadUser(path string) (User, error) {
	cfg := DefaultUser()
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	var file User
	if err := Decode(data, &file); err != nil {
		return cfg, fmt.Errorf("parse %s: %w", path, err)
	}
	cfg.merge(file)
	cfg.Path = path
	return cfg, cfg.validate()
}

func (u *User) merge(o User) {
	for name, p := range o.Providers {
		base := u.Providers[name]
		if p.BaseURL != "" {
			base.BaseURL = p.BaseURL
		}
		if p.APIKey != "" {
			base.APIKey = p.APIKey
		}
		if p.APIKeyEnv != "" {
			base.APIKeyEnv = p.APIKeyEnv
		}
		u.Providers[name] = base
	}
	maps.Copy(u.Models, o.Models)
	if o.SummaryBudget > 0 {
		u.SummaryBudget = o.SummaryBudget
	}
	if o.Search.Candidates > 0 {
		u.Search.Candidates = o.Search.Candidates
	}
	if o.Search.MinSimilarity > 0 {
		u.Search.MinSimilarity = o.Search.MinSimilarity
	}
	if o.Search.RerankTopN > 0 {
		u.Search.RerankTopN = o.Search.RerankTopN
	}
	if o.Search.MinRelevance > 0 {
		u.Search.MinRelevance = o.Search.MinRelevance
	}
	if o.Search.DecisionThreshold > 0 {
		u.Search.DecisionThreshold = o.Search.DecisionThreshold
	}
	if o.Retry.MaxAttempts > 0 {
		u.Retry.MaxAttempts = o.Retry.MaxAttempts
	}
	if o.Retry.InitialBackoff > 0 {
		u.Retry.InitialBackoff = o.Retry.InitialBackoff
	}
	if o.Retry.MaxBackoff > 0 {
		u.Retry.MaxBackoff = o.Retry.MaxBackoff
	}
	if o.Update.Backend != "" {
		u.Update.Backend = o.Update.Backend
	}
	if o.Update.Image != "" {
		u.Update.Image = o.Update.Image
	}
	if o.Update.LXDImage != "" {
		u.Update.LXDImage = o.Update.LXDImage
	}
	if o.Update.MaxIterations > 0 {
		u.Update.MaxIterations = o.Update.MaxIterations
	}
	if o.Update.Confirmations > 0 {
		u.Update.Confirmations = o.Update.Confirmations
	}
	u.Proxy.Disabled = u.Proxy.Disabled || o.Proxy.Disabled
	if o.Proxy.MaxObjectMB > 0 {
		u.Proxy.MaxObjectMB = o.Proxy.MaxObjectMB
	}
	if o.Update.ExecTimeout > 0 {
		u.Update.ExecTimeout = o.Update.ExecTimeout
	}
	if o.Sync.File != "" {
		u.Sync.File = o.Sync.File
	}
	if o.Sync.Model != "" {
		u.Sync.Model = o.Sync.Model
	}
	if o.Sync.Since != "" {
		u.Sync.Since = o.Sync.Since
	}
	if len(o.Sync.Projects) > 0 {
		if u.Sync.Projects == nil {
			u.Sync.Projects = map[string]SyncProject{}
		}
		maps.Copy(u.Sync.Projects, o.Sync.Projects)
	}
}

func (u User) validate() error {
	if err := validateCutoff("sync", u.Sync.Model, u.Sync.Since); err != nil {
		return err
	}
	for dir, p := range u.Sync.Projects {
		if err := validateCutoff("sync.projects "+dir, p.Model, p.Since); err != nil {
			return err
		}
	}
	for role := range u.Models {
		switch role {
		case RoleEmbedding, RoleRerank, RoleDecision, RoleSummary, RoleAgent:
		default:
			return fmt.Errorf("%s: unknown model role %q", u.Path, role)
		}
	}
	return nil
}

// Endpoint is a fully resolved connection for one model role.
type Endpoint struct {
	Role     string
	Provider string
	Model    string
	BaseURL  string
	APIKey   string
}

// ErrNotConfigured is returned by Resolve when the role has no model.
var ErrNotConfigured = errors.New("model role not configured")

// Resolve merges a model role with its provider defaults and environment.
func (u User) Resolve(role string) (Endpoint, error) {
	m, ok := u.Models[role]
	if !ok || !m.Configured() {
		return Endpoint{}, fmt.Errorf("%w: %s", ErrNotConfigured, role)
	}
	provName := m.Provider
	if provName == "" {
		provName = "openrouter"
	}
	prov := u.Providers[provName]
	ep := Endpoint{Role: role, Provider: provName, Model: m.Model}
	ep.BaseURL = firstNonEmpty(m.BaseURL, prov.BaseURL)
	if ep.BaseURL == "" {
		return ep, fmt.Errorf("model %s: provider %q has no base-url", role, provName)
	}
	ep.BaseURL = strings.TrimRight(ep.BaseURL, "/")
	ep.APIKey = firstNonEmpty(m.APIKey, prov.APIKey)
	if ep.APIKey == "" {
		env := firstNonEmpty(m.APIKeyEnv, prov.APIKeyEnv)
		if env != "" {
			ep.APIKey = os.Getenv(env)
		}
		if ep.APIKey == "" {
			return ep, fmt.Errorf("model %s: no API key (set api-key or export %s)", role, firstNonEmpty(env, "an api-key-env"))
		}
	}
	return ep, nil
}

// ProviderEndpoint resolves a provider's connection details for calls that
// are not tied to a model role, such as listing models. A missing API key
// is not an error; the endpoint is returned without one.
func (u User) ProviderEndpoint(name string) (Endpoint, error) {
	prov, ok := u.Providers[name]
	if !ok {
		return Endpoint{}, fmt.Errorf("provider %q is not configured", name)
	}
	if prov.BaseURL == "" {
		return Endpoint{}, fmt.Errorf("provider %q has no base-url", name)
	}
	ep := Endpoint{Provider: name, BaseURL: strings.TrimRight(prov.BaseURL, "/"), APIKey: prov.APIKey}
	if ep.APIKey == "" && prov.APIKeyEnv != "" {
		ep.APIKey = os.Getenv(prov.APIKeyEnv)
	}
	return ep, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// validateCutoff checks that a sync entry names a model or a date, not
// both, and that the date parses.
func validateCutoff(where, model, since string) error {
	if model != "" && since != "" {
		return fmt.Errorf("%s: give model or since, not both", where)
	}
	if since != "" {
		if _, err := time.Parse("2006-01-02", since); err != nil {
			return fmt.Errorf("%s: since %q is not a date (use YYYY-MM-DD)", where, since)
		}
	}
	return nil
}
