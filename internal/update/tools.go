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

package update

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/microsoft/agent-framework-go/tool"
	"github.com/microsoft/agent-framework-go/tool/functool"

	"github.com/canonical/axiomaton/internal/axiom"
	"github.com/canonical/axiomaton/internal/subjects/gomod"
)

const maxToolOutput = 24 * 1024

// ShellInput is the argument to the shell tool.
type ShellInput struct {
	Command        string `json:"command" jsonschema:"The bash command line to run inside the container."`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty" jsonschema:"Optional timeout for the command in seconds."`
}

// ShellOutput is the result of the shell tool.
type ShellOutput struct {
	ExitCode  int    `json:"exit_code"`
	Stdout    string `json:"stdout"`
	Stderr    string `json:"stderr"`
	TimedOut  bool   `json:"timed_out"`
	Truncated bool   `json:"truncated"`
}

// ShellTool exposes a container to the agent.
func ShellTool(c Container, defaultTimeout time.Duration, logf func(string, ...any)) (tool.FuncTool, error) {
	return functool.New(functool.Config{
		Name: "shell",
		Description: "Run a bash command inside your Ubuntu container as root. The container persists " +
			"between calls, so installed packages and files remain. Output is truncated if very long.",
	}, func(ctx context.Context, in ShellInput) (ShellOutput, error) {
		timeout := defaultTimeout
		if in.TimeoutSeconds > 0 {
			timeout = time.Duration(in.TimeoutSeconds) * time.Second
		}
		if logf != nil {
			logf("$ %s", oneLine(in.Command, 200))
		}
		res, err := c.Exec(ctx, in.Command, timeout)
		if err != nil {
			return ShellOutput{}, err
		}
		out := ShellOutput{ExitCode: res.ExitCode, TimedOut: res.TimedOut}
		out.Stdout, out.Truncated = truncate(res.Stdout)
		var t bool
		out.Stderr, t = truncate(res.Stderr)
		out.Truncated = out.Truncated || t
		return out, nil
	})
}

// ProposeInput is the argument to the propose_axiom tool.
type ProposeInput struct {
	Date      string   `json:"date" jsonschema:"The date the statement became true, as YYYY-MM-DD (usually the release date)."`
	Clock     []string `json:"clock" jsonschema:"The subject's clock values in order, e.g. the release version such as 1.27.0."`
	Statement string   `json:"statement" jsonschema:"One self-contained declarative sentence or two stating the axiom."`
	Source    string   `json:"source" jsonschema:"URL of the primary source the statement was read from."`
}

// ProposeOutput is the result of the propose_axiom tool.
type ProposeOutput struct {
	Accepted bool   `json:"accepted"`
	ID       string `json:"id"`
	Message  string `json:"message"`
}

// Proposals collects axioms proposed by the update agent.
type Proposals struct {
	mu       sync.Mutex
	subject  axiom.Subject
	existing map[string]bool
	list     []axiom.Axiom
}

// NewProposals creates a collector that rejects axioms already in existing.
func NewProposals(subject axiom.Subject, existing []axiom.Axiom) *Proposals {
	p := &Proposals{subject: subject, existing: map[string]bool{}}
	for _, a := range existing {
		p.existing[a.ID] = true
	}
	return p
}

// Add validates and records a proposal.
func (p *Proposals) Add(in ProposeInput) ProposeOutput {
	t, err := time.Parse("2006-01-02", strings.TrimSpace(in.Date))
	if err != nil {
		return ProposeOutput{Message: fmt.Sprintf("invalid date %q: use YYYY-MM-DD", in.Date)}
	}
	if !t.After(axiom.GlobalEpoch) {
		return ProposeOutput{Message: fmt.Sprintf("%s is not after the global epoch %s: anything that old is assumed to be prior knowledge of every model and is never recorded; do not propose it", in.Date, axiom.GlobalEpoch.Format("2006-01-02"))}
	}
	clock, err := p.subject.Clock(in.Clock)
	if err != nil {
		return ProposeOutput{Message: err.Error()}
	}
	if len(p.subject.Shape) > 0 && len(clock) == 0 {
		return ProposeOutput{Message: fmt.Sprintf("clock is required: provide %d value(s)", len(p.subject.Shape))}
	}
	a := axiom.Axiom{
		Subject:   p.subject.Name,
		Time:      t.UTC(),
		Clock:     clock,
		Statement: in.Statement,
		Source:    strings.TrimSpace(in.Source),
		Added:     time.Now().UTC(),
		By:        "agent",
	}
	a.Normalise()
	if err := a.Validate(); err != nil {
		return ProposeOutput{Message: err.Error()}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.existing[a.ID] {
		return ProposeOutput{ID: a.ID, Message: "an identical axiom is already recorded; not added"}
	}
	p.existing[a.ID] = true
	p.list = append(p.list, a)
	return ProposeOutput{Accepted: true, ID: a.ID, Message: "recorded"}
}

// List returns the proposals in the order they were made.
func (p *Proposals) List() []axiom.Axiom {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]axiom.Axiom(nil), p.list...)
}

// ProposeTool exposes the collector to the agent.
func ProposeTool(p *Proposals, logf func(string, ...any)) (tool.FuncTool, error) {
	return functool.New(functool.Config{
		Name: "propose_axiom",
		Description: "Record one axiom you have verified in a primary source. Call it once per " +
			"distinct fact. Returns whether it was accepted and why not if it was rejected.",
	}, func(_ context.Context, in ProposeInput) (ProposeOutput, error) {
		out := p.Add(in)
		if logf != nil {
			if out.Accepted {
				logf("proposed [%s %s] %s", in.Date, strings.Join(in.Clock, " "), oneLine(in.Statement, 120))
			} else {
				logf("rejected proposal: %s", out.Message)
			}
		}
		return out, nil
	})
}

// BatchInput is the argument to the batch_commits tool.
type BatchInput struct {
	Commits []BatchCommit `json:"commits" jsonschema:"Every commit on the module's main branch in the range to cover, in any order."`
	// BaseVersion is the newest tag before the commits, shaping the
	// pseudo-versions as the go command would.
	BaseVersion string `json:"base_version,omitempty" jsonschema:"The newest tagged version that precedes these commits, e.g. v0.5.0, or empty if the module has never been tagged."`
}

// BatchCommit is one commit in BatchInput.
type BatchCommit struct {
	Hash string `json:"hash" jsonschema:"The full commit hash."`
	Time string `json:"time" jsonschema:"The commit time as RFC 3339, e.g. from git log --format=%cI."`
	Go   string `json:"go,omitempty" jsonschema:"The go directive of go.mod at this commit, e.g. 1.24; omit if unknown."`
}

// BatchRelease is one release in BatchOutput.
type BatchRelease struct {
	Version   string `json:"version"`
	Date      string `json:"date"`
	First     string `json:"first_commit"`
	Last      string `json:"last_commit"`
	Commits   int    `json:"commits"`
	GoVersion string `json:"go,omitempty"`
	Reason    string `json:"opened_because"`
}

// BatchOutput is the result of the batch_commits tool.
type BatchOutput struct {
	Releases []BatchRelease `json:"releases"`
	Message  string         `json:"message"`
}

// BatchTool groups an untagged module's commits into releases for the
// update agent, so that every run of ax divides the same history the same
// way. See gomod.Batch for the rules.
func BatchTool(logf func(string, ...any)) (tool.FuncTool, error) {
	return batchTool(logf, false)
}

// CommitBatchTool is BatchTool for subjects whose clock is a git commit
// rather than a module version: releases are named by the full hash of
// their last commit, which is what such a clock holds.
func CommitBatchTool(logf func(string, ...any)) (tool.FuncTool, error) {
	return batchTool(logf, true)
}

func batchTool(logf func(string, ...any), byCommit bool) (tool.FuncTool, error) {
	desc := "For a Go module with no tagged releases: group its commit history into discrete " +
		"releases. A new release starts when the go directive changes, after a gap of more than a month, " +
		"or at a new calendar month. Each release gets the pseudo-version of its last commit; use that " +
		"as the clock and its date as the date when proposing axioms for changes in that release."
	if byCommit {
		desc = "Group a repository's commit history into discrete releases so that every run divides it " +
			"the same way. A new release starts after a gap of more than a month or at a new calendar month " +
			"(and when the go field changes, if you give it). Each release is named by the full hash of its " +
			"last commit; use that hash as the git clock value and the release's date as the date when " +
			"proposing axioms for changes in that release."
	}
	return functool.New(functool.Config{
		Name:        "batch_commits",
		Description: desc,
	}, func(_ context.Context, in BatchInput) (BatchOutput, error) {
		commits := make([]gomod.Commit, 0, len(in.Commits))
		for _, c := range in.Commits {
			t, err := time.Parse(time.RFC3339, strings.TrimSpace(c.Time))
			if err != nil {
				return BatchOutput{Message: fmt.Sprintf("commit %s: time %q is not RFC 3339 (use git log --format=%%cI)", c.Hash, c.Time)}, nil
			}
			if strings.TrimSpace(c.Hash) == "" {
				return BatchOutput{Message: "every commit needs a hash"}, nil
			}
			commits = append(commits, gomod.Commit{Hash: strings.TrimSpace(c.Hash), Time: t, GoVersion: strings.TrimPrefix(strings.TrimSpace(c.Go), "go")})
		}
		var out BatchOutput
		for _, r := range gomod.Batch(commits, strings.TrimSpace(in.BaseVersion)) {
			version := r.Version
			if byCommit {
				version = r.Last
			}
			out.Releases = append(out.Releases, BatchRelease{
				Version: version, Date: r.Time.Format("2006-01-02"), First: r.First, Last: r.Last,
				Commits: r.Commits, GoVersion: r.GoVersion, Reason: r.Reason,
			})
		}
		what := "its version"
		if byCommit {
			what = "its last_commit hash"
		}
		out.Message = fmt.Sprintf("%d commit(s) grouped into %d release(s); review each release's changes (git diff <previous last_commit>..<last_commit>) and propose axioms with %s as the clock and its date as the date", len(commits), len(out.Releases), what)
		if logf != nil {
			logf("batched %d commit(s) into %d release(s)", len(commits), len(out.Releases))
		}
		return out, nil
	})
}

func truncate(s string) (string, bool) {
	if len(s) <= maxToolOutput {
		return s, false
	}
	half := maxToolOutput / 2
	return s[:half] + "\n...[output truncated]...\n" + s[len(s)-half:], true
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		s = s[:n] + "..."
	}
	return s
}
