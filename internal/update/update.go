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
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/microsoft/agent-framework-go/tool"

	"github.com/canonical/axiomaton/internal/axiom"
	"github.com/canonical/axiomaton/internal/subjects"
)

// Verdict is the structured answer of a confirmation agent.
type Verdict struct {
	Confirmed bool   `json:"confirmed" jsonschema:"True only if the statement was verified against a primary source."`
	Reason    string `json:"reason" jsonschema:"One or two sentences explaining what was checked and the outcome."`
}

// Proposal is an axiom proposed by the update agent plus its verdict.
type Proposal struct {
	Axiom   axiom.Axiom `json:"axiom"`
	Verdict Verdict     `json:"verdict"`
	// Error records a confirmation failure (the agent could not run).
	Error string `json:"error,omitempty"`
}

// Updater orchestrates the update and confirmation agents.
type Updater struct {
	Subject     subjects.Builtin
	Runtime     Runtime
	Agent       Agent
	ExecTimeout time.Duration
	// Log receives progress; nil discards it.
	Log func(format string, args ...any)
}

func (u *Updater) logf(format string, args ...any) {
	if u.Log != nil {
		u.Log(format, args...)
	}
}

func (u *Updater) subject() axiom.Subject {
	return axiom.Subject{Name: u.Subject.Name(), Shape: u.Subject.Shape()}
}

// Propose runs the update agent in a fresh container and returns the axioms
// it proposed along with its closing summary.
func (u *Updater) Propose(ctx context.Context, existing []axiom.Axiom) ([]axiom.Axiom, string, error) {
	c, err := u.Runtime.Start(ctx)
	if err != nil {
		return nil, "", err
	}
	defer c.Close(ctx)

	props := NewProposals(u.subject(), existing)
	shell, err := ShellTool(c, u.ExecTimeout, u.Log)
	if err != nil {
		return nil, "", err
	}
	propose, err := ProposeTool(props, u.Log)
	if err != nil {
		return nil, "", err
	}
	u.logf("running update agent for %s", u.Subject.Name())
	summary, err := u.Agent.Run(ctx, u.Subject.UpdateInstructions(), u.Subject.UpdateTask(existing), []tool.Tool{shell, propose}, nil)
	if err != nil {
		return props.List(), "", fmt.Errorf("update agent: %w", err)
	}
	return props.List(), summary, nil
}

// Confirm asks an independent agent in a fresh container to verify one axiom.
func (u *Updater) Confirm(ctx context.Context, a axiom.Axiom) (Verdict, error) {
	c, err := u.Runtime.Start(ctx)
	if err != nil {
		return Verdict{}, err
	}
	defer c.Close(ctx)
	shell, err := ShellTool(c, u.ExecTimeout, u.Log)
	if err != nil {
		return Verdict{}, err
	}
	var v Verdict
	text, err := u.Agent.Run(ctx, u.Subject.ConfirmInstructions(), u.Subject.ConfirmTask(a), []tool.Tool{shell}, &v)
	if err != nil {
		return Verdict{}, fmt.Errorf("confirmation agent: %w", err)
	}
	if v.Reason == "" {
		v.Reason = text
	}
	return v, nil
}

// Run proposes and then confirms each proposal in its own container.
func (u *Updater) Run(ctx context.Context, existing []axiom.Axiom) ([]Proposal, string, error) {
	proposed, summary, err := u.Propose(ctx, existing)
	if err != nil {
		return nil, summary, err
	}
	u.logf("update agent proposed %d axiom(s)", len(proposed))
	out := make([]Proposal, 0, len(proposed))
	for i, a := range proposed {
		u.logf("confirming %d/%d: %s", i+1, len(proposed), oneLine(a.Statement, 100))
		p := Proposal{Axiom: a}
		v, err := u.Confirm(ctx, a)
		if err != nil {
			if ctx.Err() != nil {
				return out, summary, err
			}
			p.Error = err.Error()
			u.logf("confirmation failed: %v", err)
		} else {
			p.Verdict = v
			if v.Confirmed {
				u.logf("confirmed: %s", oneLine(v.Reason, 160))
			} else {
				u.logf("not confirmed: %s", oneLine(v.Reason, 160))
			}
		}
		out = append(out, p)
	}
	return out, summary, nil
}

// Split separates confirmed proposals from those needing a human.
func Split(props []Proposal) (confirmed []axiom.Axiom, pending []Proposal) {
	for _, p := range props {
		if p.Verdict.Confirmed && p.Error == "" {
			confirmed = append(confirmed, p.Axiom)
		} else {
			pending = append(pending, p)
		}
	}
	return confirmed, pending
}

// Review asks the user about each pending proposal on the terminal and
// returns the accepted axioms. Answers: y(es), n(o), e(dit statement), q(uit).
func Review(pending []Proposal, in io.Reader, out io.Writer, shape []axiom.ClockShape) ([]axiom.Axiom, error) {
	r := bufio.NewReader(in)
	var accepted []axiom.Axiom
	for i, p := range pending {
		fmt.Fprintf(out, "\n[%d/%d] %s %s\n  %s\n", i+1, len(pending), p.Axiom.Time.Format("2006-01-02"), p.Axiom.ClockString(shape), p.Axiom.Statement)
		if p.Axiom.Source != "" {
			fmt.Fprintf(out, "  source: %s\n", p.Axiom.Source)
		}
		if p.Error != "" {
			fmt.Fprintf(out, "  confirmation error: %s\n", p.Error)
		} else {
			fmt.Fprintf(out, "  not confirmed: %s\n", p.Verdict.Reason)
		}
		for {
			fmt.Fprint(out, "Accept? [y/n/e/q] ")
			line, err := r.ReadString('\n')
			if err != nil && line == "" {
				return accepted, fmt.Errorf("read answer: %w", err)
			}
			switch strings.ToLower(strings.TrimSpace(line)) {
			case "y", "yes":
				a := p.Axiom
				a.By = "user"
				accepted = append(accepted, a)
			case "n", "no":
			case "e", "edit":
				fmt.Fprint(out, "New statement: ")
				stmt, _ := r.ReadString('\n')
				stmt = strings.TrimSpace(stmt)
				if stmt == "" {
					continue
				}
				a := p.Axiom
				a.Statement = stmt
				a.By = "user"
				a.Normalise()
				accepted = append(accepted, a)
			case "q", "quit":
				return accepted, nil
			default:
				continue
			}
			break
		}
	}
	return accepted, nil
}

// WritePending appends unconfirmed proposals to a JSON lines file so they
// can be reviewed later.
func WritePending(path string, pending []Proposal) error {
	if len(pending) == 0 {
		return nil
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, p := range pending {
		if err := enc.Encode(p); err != nil {
			return err
		}
	}
	return nil
}
