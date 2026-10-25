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
	"sync"
	"time"

	"github.com/microsoft/agent-framework-go/tool"

	"github.com/canonical/axiomaton/internal/axiom"
	"github.com/canonical/axiomaton/internal/subjects"
)

// Verdict is the structured answer of a confirmation agent. A refusal comes
// in two kinds: an inaccurate statement, where the agent found what is
// actually true and offers it as Correction, and an unfounded one, where
// nothing like the statement holds and Correction is empty.
type Verdict struct {
	Confirmed bool `json:"confirmed" jsonschema:"True only if the statement, exactly as written, was verified against a primary source."`
	// Correction is the agent's verified rewrite of an inaccurate statement.
	Correction string `json:"correction,omitempty" jsonschema:"Only when confirmed is false because the statement is close but inaccurate (wrong version, name, scope or an overstated or incomplete claim): the corrected statement that you did verify, as one or two self-contained sentences. Leave empty when confirmed, or when the statement is unfounded and no accurate version of it exists."`
	Reason     string `json:"reason" jsonschema:"One or two sentences explaining what was checked and the outcome, and for a correction what was wrong with the original."`
}

// Suggests reports whether the verdict rejects the statement but offers a
// corrected one.
func (v Verdict) Suggests() bool {
	return !v.Confirmed && strings.TrimSpace(v.Correction) != ""
}

// Proposal is an axiom proposed by the update agent plus its verdict.
type Proposal struct {
	Axiom   axiom.Axiom `json:"axiom"`
	Verdict Verdict     `json:"verdict"`
	// Original is the statement as the update agent proposed it when Axiom
	// carries a correction that was confirmed instead.
	Original *axiom.Axiom `json:"original,omitempty"`
	// Error records a confirmation failure (the agent could not run).
	Error string `json:"error,omitempty"`
}

// Corrected returns the proposal with the verdict's correction in place of
// the statement, keeping the original for the record.
func (p Proposal) Corrected() Proposal {
	orig := p.Axiom
	fixed := orig
	fixed.Statement = strings.TrimSpace(p.Verdict.Correction)
	fixed.Normalise()
	return Proposal{Axiom: fixed, Original: &orig}
}

// Updater orchestrates the update and confirmation agents.
type Updater struct {
	Subject     subjects.Builtin
	Runtime     Runtime
	Agent       Agent
	ExecTimeout time.Duration
	// Parallel is how many confirmations run at once; values below one mean
	// one at a time.
	Parallel int
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
	v.Correction = strings.TrimSpace(v.Correction)
	if v.Confirmed || v.Correction == a.Statement {
		v.Correction = ""
	}
	return v, nil
}

// Run proposes and then confirms each proposal in its own container, up to
// Parallel at a time, keeping the proposals in order. When a confirmation
// agent rejects a statement as inaccurate and offers a correction, the
// correction is confirmed once more by a fresh agent and, if that succeeds,
// replaces the proposal; otherwise the correction stays on the verdict as a
// suggestion for the reviewer.
func (u *Updater) Run(ctx context.Context, existing []axiom.Axiom) ([]Proposal, string, error) {
	proposed, summary, err := u.Propose(ctx, existing)
	if err != nil {
		return nil, summary, err
	}
	u.logf("update agent proposed %d axiom(s)", len(proposed))
	workers := max(1, u.Parallel)
	if workers > 1 && len(proposed) > 1 {
		u.logf("confirming up to %d at a time", min(workers, len(proposed)))
	}
	known := &idSet{ids: map[string]bool{}}
	for _, a := range existing {
		known.add(a.ID)
	}
	parent := ctx
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make([]Proposal, len(proposed))
	dropped := make([]bool, len(proposed))
	var (
		wg       sync.WaitGroup
		sem      = make(chan struct{}, workers)
		firstErr error
		errMu    sync.Mutex
	)
	for i, a := range proposed {
		if ctx.Err() != nil {
			break
		}
		sem <- struct{}{}
		wg.Go(func() {
			defer func() { <-sem }()
			p, keep, err := u.confirmOne(ctx, i, len(proposed), a, known)
			if err != nil {
				errMu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				errMu.Unlock()
				cancel()
				return
			}
			results[i] = p
			dropped[i] = !keep
		})
	}
	wg.Wait()
	if firstErr != nil {
		return nil, summary, firstErr
	}
	if err := parent.Err(); err != nil {
		return nil, summary, err
	}
	out := make([]Proposal, 0, len(proposed))
	for i := range results {
		if !dropped[i] {
			out = append(out, results[i])
		}
	}
	return out, summary, nil
}

// confirmOne runs the confirmation (and, for an inaccurate statement, the
// confirmation of its correction) for proposal i of n. keep is false when
// the proposal should be dropped because its correction is already known.
// A non-nil error means the run was cancelled.
func (u *Updater) confirmOne(ctx context.Context, i, n int, a axiom.Axiom, known *idSet) (p Proposal, keep bool, err error) {
	tag := fmt.Sprintf("[%d/%d]", i+1, n)
	u.logf("%s confirming: %s", tag, oneLine(a.Statement, 100))
	p = Proposal{Axiom: a}
	v, err := u.Confirm(ctx, a)
	if err != nil {
		if ctx.Err() != nil {
			return p, false, err
		}
		p.Error = err.Error()
		u.logf("%s confirmation failed: %v", tag, err)
		return p, true, nil
	}
	p.Verdict = v
	switch {
	case v.Confirmed:
		u.logf("%s confirmed: %s", tag, oneLine(v.Reason, 160))
	case v.Suggests():
		u.logf("%s inaccurate: %s", tag, oneLine(v.Reason, 160))
		u.logf("%s suggested instead: %s", tag, oneLine(v.Correction, 160))
		fixed := p.Corrected()
		if known.has(fixed.Axiom.ID) {
			u.logf("%s the correction is already in the log; dropping the proposal", tag)
			return p, false, nil
		}
		v2, err := u.Confirm(ctx, fixed.Axiom)
		switch {
		case err != nil && ctx.Err() != nil:
			return p, false, err
		case err != nil:
			u.logf("%s confirmation of the correction failed: %v", tag, err)
		case v2.Confirmed:
			u.logf("%s correction confirmed: %s", tag, oneLine(v2.Reason, 160))
			fixed.Verdict = v2
			p = fixed
			known.add(p.Axiom.ID)
		default:
			u.logf("%s correction not confirmed: %s", tag, oneLine(v2.Reason, 160))
		}
	default:
		u.logf("%s unfounded: %s", tag, oneLine(v.Reason, 160))
	}
	return p, true, nil
}

// idSet is a concurrency safe set of axiom IDs.
type idSet struct {
	mu  sync.Mutex
	ids map[string]bool
}

func (s *idSet) add(id string) { s.mu.Lock(); s.ids[id] = true; s.mu.Unlock() }
func (s *idSet) has(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ids[id]
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
// returns the accepted axioms. Answers: y(es), n(o), e(dit statement),
// q(uit) and, when the confirmation agent suggested a correction,
// c(orrection) to accept that instead.
func Review(pending []Proposal, in io.Reader, out io.Writer, shape []axiom.ClockShape) ([]axiom.Axiom, error) {
	r := bufio.NewReader(in)
	var accepted []axiom.Axiom
	for i, p := range pending {
		fmt.Fprintf(out, "\n[%d/%d] %s %s\n  %s\n", i+1, len(pending), p.Axiom.Time.Format("2006-01-02"), p.Axiom.ClockString(shape), p.Axiom.Statement)
		if p.Axiom.Source != "" {
			fmt.Fprintf(out, "  source: %s\n", p.Axiom.Source)
		}
		prompt := "Accept? [y/n/e/q] "
		switch {
		case p.Error != "":
			fmt.Fprintf(out, "  confirmation error: %s\n", p.Error)
		case p.Verdict.Suggests():
			fmt.Fprintf(out, "  inaccurate: %s\n  suggested instead: %s\n", p.Verdict.Reason, p.Verdict.Correction)
			prompt = "Accept? [y/n/c=suggestion/e/q] "
		default:
			fmt.Fprintf(out, "  not confirmed: %s\n", p.Verdict.Reason)
		}
		for {
			fmt.Fprint(out, prompt)
			line, err := r.ReadString('\n')
			if err != nil && line == "" {
				return accepted, fmt.Errorf("read answer: %w", err)
			}
			switch strings.ToLower(strings.TrimSpace(line)) {
			case "y", "yes":
				a := p.Axiom
				a.By = "user"
				accepted = append(accepted, a)
			case "c", "correction", "suggestion":
				if !p.Verdict.Suggests() {
					continue
				}
				a := p.Corrected().Axiom
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
