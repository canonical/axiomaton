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
