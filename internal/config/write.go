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
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
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

// Render serialises the configuration as commented YAML for ax.cnf. Every
// section is written out so the file documents the tunables.
func Render(u User, now time.Time) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "# ax.cnf written by ax setup on %s.\n", now.UTC().Format("2006-01-02"))
	b.WriteString("# Reference: https://github.com/canonical/axiomaton#readme\n\n")

	b.WriteString("providers:\n")
	names := make([]string, 0, len(u.Providers))
	for n := range u.Providers {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		p := u.Providers[n]
		fmt.Fprintf(&b, "  %s:\n", yamlScalar(n))
		if p.BaseURL != "" {
			fmt.Fprintf(&b, "    base_url: %s\n", yamlScalar(p.BaseURL))
		}
		if p.APIKey != "" {
			fmt.Fprintf(&b, "    api_key: %s\n", yamlScalar(p.APIKey))
		}
		if p.APIKeyEnv != "" {
			fmt.Fprintf(&b, "    api_key_env: %s\n", yamlScalar(p.APIKeyEnv))
		}
	}

	b.WriteString("models:\n")
	for _, role := range Roles {
		m := u.Models[role]
		if !m.Configured() {
			fmt.Fprintf(&b, "  # %s: not configured (%s)\n", role, RoleNotes[role])
			continue
		}
		fields := []string{"model: " + yamlScalar(m.Model)}
		if m.Provider != "" {
			fields = append(fields, "provider: "+yamlScalar(m.Provider))
		}
		if m.BaseURL != "" {
			fields = append(fields, "base_url: "+yamlScalar(m.BaseURL))
		}
		if m.APIKey != "" {
			fields = append(fields, "api_key: "+yamlScalar(m.APIKey))
		}
		if m.APIKeyEnv != "" {
			fields = append(fields, "api_key_env: "+yamlScalar(m.APIKeyEnv))
		}
		fmt.Fprintf(&b, "  %s: {%s}  # %s\n", role, strings.Join(fields, ", "), RoleNotes[role])
	}

	fmt.Fprintf(&b, "summary_budget: %d  # tokens for ax since -s\n", u.SummaryBudget)
	b.WriteString("search:\n")
	fmt.Fprintf(&b, "  candidates: %d  # nearest neighbours per axiom\n", u.Search.Candidates)
	fmt.Fprintf(&b, "  min_similarity: %s  # cosine floor\n", yamlFloat(u.Search.MinSimilarity))
	fmt.Fprintf(&b, "  rerank_top_n: %d\n", u.Search.RerankTopN)
	fmt.Fprintf(&b, "  min_relevance: %s\n", yamlFloat(u.Search.MinRelevance))
	fmt.Fprintf(&b, "  decision_threshold: %s  # minimum probability to accept a verdict\n", yamlFloat(u.Search.DecisionThreshold))
	b.WriteString("retry:\n")
	fmt.Fprintf(&b, "  max_attempts: %d  # tries per model request\n", u.Retry.MaxAttempts)
	fmt.Fprintf(&b, "  initial_backoff: %s  # seconds; doubles each attempt with jitter\n", yamlFloat(u.Retry.InitialBackoff))
	fmt.Fprintf(&b, "  max_backoff: %s  # seconds\n", yamlFloat(u.Retry.MaxBackoff))
	b.WriteString("update:\n")
	fmt.Fprintf(&b, "  image: %s\n", yamlScalar(u.Update.Image))
	fmt.Fprintf(&b, "  max_iterations: %d  # tool calls per agent run\n", u.Update.MaxIterations)
	fmt.Fprintf(&b, "  exec_timeout: %d  # seconds per command in the container\n", u.Update.ExecTimeout)
	return b.Bytes()
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
	if err := os.WriteFile(path, Render(u, now), perm); err != nil {
		return err
	}
	// WriteFile leaves the mode of an existing file alone.
	return os.Chmod(path, perm)
}

var plainScalar = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/@+-]*$`)

// yamlScalar quotes s unless it is a simple plain scalar in both block and
// flow context.
func yamlScalar(s string) string {
	if plainScalar.MatchString(s) && !looksSpecial(s) {
		return s
	}
	return strconv.Quote(s)
}

// looksSpecial reports plain scalars YAML would not read back as strings.
func looksSpecial(s string) bool {
	switch strings.ToLower(s) {
	case "true", "false", "yes", "no", "on", "off", "null", "~":
		return true
	}
	if _, err := strconv.ParseFloat(s, 64); err == nil {
		return true
	}
	return false
}

func yamlFloat(f float64) string {
	return strconv.FormatFloat(f, 'g', -1, 64)
}
