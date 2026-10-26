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
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/microsoft/agent-framework-go/tool"

	"github.com/canonical/axiomaton/internal/axiom"
	"github.com/canonical/axiomaton/internal/subjects"
	"github.com/canonical/axiomaton/internal/subjects/gomod"
)

// Verdict is the structured answer of a confirmation agent. A refusal comes
// in two kinds: an inaccurate axiom, where the agent found what is actually
// true and offers it as a corrected statement, date and/or clock, and an
// unfounded one, where nothing like the statement holds and no correction
// is offered.
type Verdict struct {
	Confirmed bool `json:"confirmed" jsonschema:"True only if the statement, exactly as written, is accurate for the stated release and date, verified against a primary source."`
	// Correction is the agent's verified rewrite of an inaccurate statement.
	Correction string `json:"correction,omitempty" jsonschema:"Only when confirmed is false because the statement is close but inaccurate (wrong name, scope, or an overstated or incomplete claim): the corrected statement that you did verify, as one or two self-contained sentences. Leave empty when the statement itself is right, when confirmed, or when the statement is unfounded and no accurate version of it exists."`
	// Date is the agent's corrected release date.
	Date string `json:"date,omitempty" jsonschema:"Only when confirmed is false because the statement became true on a different date than stated: the correct date as YYYY-MM-DD (the release date), verified in a source. Leave empty when the stated date is right."`
	// Clock is the agent's corrected clock, e.g. the release version.
	Clock []string `json:"clock,omitempty" jsonschema:"Only when confirmed is false because the statement belongs to a different release than stated: the correct clock values in order (for example the release version such as 1.27.0), verified in a source. Leave empty when the stated release is right."`
	// Split replaces the statement with several axioms.
	Split  []Part `json:"split,omitempty" jsonschema:"Only when confirmed is false because the statement bundles several distinct facts, or is right in part and wrong in part: the separate axioms that should replace it, each verified. Each part has its own statement and, only where it differs from the original, its own date and clock. Leave empty otherwise; do not combine with correction."`
	Reason string `json:"reason" jsonschema:"One or two sentences explaining what was checked and the outcome, and for a correction or split what was wrong with the original."`
}

// Part is one axiom of a split verdict.
type Part struct {
	Statement string   `json:"statement" jsonschema:"One self-contained declarative sentence or two, verified against a primary source."`
	Date      string   `json:"date,omitempty" jsonschema:"The date this part became true as YYYY-MM-DD, only if it differs from the original's."`
	Clock     []string `json:"clock,omitempty" jsonschema:"The clock values for this part in order (e.g. the release version), only if they differ from the original's."`
}

// Suggests reports whether the verdict rejects the axiom but offers a
// corrected statement, date or clock, or a split into several axioms.
func (v Verdict) Suggests() bool {
	return !v.Confirmed && (strings.TrimSpace(v.Correction) != "" || strings.TrimSpace(v.Date) != "" || len(v.Clock) > 0 || len(v.Split) > 0)
}

// Suggestion describes what the verdict proposes to change, for logs and
// the reviewer.
func (v Verdict) Suggestion() string {
	if len(v.Split) > 0 {
		parts := make([]string, len(v.Split))
		for i, part := range v.Split {
			parts[i] = fmt.Sprintf("%d. %s", i+1, part.describe())
		}
		return fmt.Sprintf("split into %d: %s", len(v.Split), strings.Join(parts, " "))
	}
	var parts []string
	if s := strings.TrimSpace(v.Correction); s != "" {
		parts = append(parts, "statement: "+s)
	}
	if d := strings.TrimSpace(v.Date); d != "" {
		parts = append(parts, "date: "+d)
	}
	if len(v.Clock) > 0 {
		parts = append(parts, "clock: "+strings.Join(v.Clock, " "))
	}
	return strings.Join(parts, "; ")
}

func (p Part) describe() string {
	s := strings.TrimSpace(p.Statement)
	var extra []string
	if d := strings.TrimSpace(p.Date); d != "" {
		extra = append(extra, d)
	}
	if len(p.Clock) > 0 {
		extra = append(extra, strings.Join(p.Clock, " "))
	}
	if len(extra) > 0 {
		s += " [" + strings.Join(extra, " ") + "]"
	}
	return s
}

// Proposal is an axiom proposed by the update agent plus its verdict.
type Proposal struct {
	Axiom   axiom.Axiom `json:"axiom"`
	Verdict Verdict     `json:"verdict"`
	// Original is the axiom as the update agent proposed it when Axiom is a
	// correction of it or one part of a split.
	Original *axiom.Axiom `json:"original,omitempty"`
	// Error records a confirmation failure (the agent could not run).
	Error string `json:"error,omitempty"`
}

// ErrPriorKnowledge means a correction dates the axiom on or before the
// global epoch, so it is prior knowledge and should not be recorded at all.
var ErrPriorKnowledge = errors.New("the corrected date is not after the global epoch " + axiom.GlobalEpoch.Format("2006-01-02") + ", so the axiom is prior knowledge")

// Corrected returns the proposal with the verdict's corrections applied to
// the statement, date and clock, keeping the original for the record. The
// clock is validated against the subject's shape. For a split verdict use
// Suggested.
func (p Proposal) Corrected(subject axiom.Subject) (Proposal, error) {
	if len(p.Verdict.Split) > 0 {
		return p, errors.New("the suggestion is a split; use Suggested")
	}
	fixed, err := p.derive(subject, p.Verdict.Correction, p.Verdict.Date, p.Verdict.Clock)
	if err != nil {
		return p, err
	}
	if fixed.Axiom.ID == p.Axiom.ID {
		return p, errors.New("the suggestion changes nothing")
	}
	return fixed, nil
}

// Suggested returns the proposals the verdict's suggestion stands for: the
// single corrected proposal, or one per part of a split. Parts dated on or
// before the global epoch are left out; when nothing remains the error is
// ErrPriorKnowledge.
func (p Proposal) Suggested(subject axiom.Subject) ([]Proposal, error) {
	if len(p.Verdict.Split) == 0 {
		fixed, err := p.Corrected(subject)
		if err != nil {
			return nil, err
		}
		return []Proposal{fixed}, nil
	}
	var out []Proposal
	seen := map[string]bool{p.Axiom.ID: true}
	for i, part := range p.Verdict.Split {
		if strings.TrimSpace(part.Statement) == "" {
			return nil, fmt.Errorf("part %d of the split has no statement", i+1)
		}
		q, err := p.derive(subject, part.Statement, part.Date, part.Clock)
		if errors.Is(err, ErrPriorKnowledge) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("part %d of the split: %w", i+1, err)
		}
		if seen[q.Axiom.ID] {
			return nil, fmt.Errorf("part %d of the split repeats another part or the original", i+1)
		}
		seen[q.Axiom.ID] = true
		out = append(out, q)
	}
	if len(out) == 0 {
		return nil, ErrPriorKnowledge
	}
	return out, nil
}

// derive builds a replacement for the proposal from an optional statement,
// date and clock, keeping whatever is not given and recording the original.
func (p Proposal) derive(subject axiom.Subject, statement, date string, clock []string) (Proposal, error) {
	orig := p.Axiom
	fixed := orig
	if st := strings.TrimSpace(statement); st != "" {
		fixed.Statement = st
	}
	if d := strings.TrimSpace(date); d != "" {
		t, err := time.Parse("2006-01-02", d)
		if err != nil {
			return p, fmt.Errorf("suggested date %q: use YYYY-MM-DD", d)
		}
		if !t.After(axiom.GlobalEpoch) {
			return p, ErrPriorKnowledge
		}
		fixed.Time = t.UTC()
	}
	if len(clock) > 0 {
		c, err := subject.Clock(clock)
		if err != nil {
			return p, fmt.Errorf("suggested clock: %w", err)
		}
		fixed.Clock = c
	}
	fixed.Normalise()
	return Proposal{Axiom: fixed, Original: &orig}, nil
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
	return subjects.AxiomSubject(u.Subject)
}

// Shipment is a host file to copy into every sandbox before an agent runs.
type Shipment struct {
	Local  string
	Remote string
}

// Prepared is implemented by subjects whose sandboxes need files shipped
// in or a command run before each agent starts, such as custom subjects
// with local repositories or an install step.
type Prepared interface {
	// Shipments lists the files to push, in order.
	Shipments() []Shipment
	// Setup is a shell command to run once the files are in place, or "".
	Setup() string
}

// SetupTimeout bounds a subject's setup command.
const SetupTimeout = 15 * time.Minute

// prepare pushes the subject's shipments and runs its setup command.
func (u *Updater) prepare(ctx context.Context, c Container) error {
	p, ok := u.Subject.(Prepared)
	if !ok {
		return nil
	}
	for _, s := range p.Shipments() {
		u.logf("shipping %s to %s", s.Local, s.Remote)
		if err := c.Push(ctx, s.Local, s.Remote); err != nil {
			return fmt.Errorf("shipping %s: %w", s.Local, err)
		}
	}
	if cmd := strings.TrimSpace(p.Setup()); cmd != "" {
		u.logf("running setup: %s", oneLine(cmd, 200))
		res, err := c.Exec(ctx, cmd, SetupTimeout)
		if err != nil {
			return fmt.Errorf("setup: %w", err)
		}
		if res.TimedOut {
			return fmt.Errorf("setup timed out after %s", SetupTimeout)
		}
		if res.ExitCode != 0 {
			tail := strings.TrimSpace(res.Stderr)
			if tail == "" {
				tail = strings.TrimSpace(res.Stdout)
			}
			return fmt.Errorf("setup exited %d: %s", res.ExitCode, oneLine(tail, 400))
		}
	}
	return nil
}

// hasGitClock reports whether any dimension of the shape is git ordered.
func hasGitClock(shape []axiom.ClockShape) bool {
	for _, d := range shape {
		if d.Kind == axiom.KindGit {
			return true
		}
	}
	return false
}

// Propose runs the update agent in a fresh container and returns the axioms
// it proposed along with its closing summary.
func (u *Updater) Propose(ctx context.Context, existing []axiom.Axiom) ([]axiom.Axiom, string, error) {
	c, err := u.Runtime.Start(ctx)
	if err != nil {
		return nil, "", err
	}
	defer c.Close(ctx)
	if err := u.prepare(ctx, c); err != nil {
		return nil, "", err
	}

	props := NewProposals(u.subject(), existing)
	shell, err := ShellTool(c, u.ExecTimeout, u.Log)
	if err != nil {
		return nil, "", err
	}
	propose, err := ProposeTool(props, u.Log)
	if err != nil {
		return nil, "", err
	}
	tools := []tool.Tool{shell, propose}
	if _, ok := u.Subject.(gomod.Subject); ok {
		batch, err := BatchTool(u.Log)
		if err != nil {
			return nil, "", err
		}
		tools = append(tools, batch)
	} else if hasGitClock(u.Subject.Shape()) {
		batch, err := CommitBatchTool(u.Log)
		if err != nil {
			return nil, "", err
		}
		tools = append(tools, batch)
	}
	u.logf("running update agent for %s", u.Subject.Name())
	summary, err := u.Agent.Run(ctx, u.Subject.UpdateInstructions(), u.Subject.UpdateTask(existing), tools, nil)
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
	if err := u.prepare(ctx, c); err != nil {
		return Verdict{}, err
	}
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
	v.Date = strings.TrimSpace(v.Date)
	if v.Confirmed {
		v.Correction, v.Date, v.Clock, v.Split = "", "", nil, nil
	}
	// A split takes precedence over a single correction; a split with one
	// part is just a correction.
	if len(v.Split) > 0 {
		v.Correction, v.Date, v.Clock = "", "", nil
		if len(v.Split) == 1 {
			v.Correction, v.Date, v.Clock, v.Split = strings.TrimSpace(v.Split[0].Statement), strings.TrimSpace(v.Split[0].Date), v.Split[0].Clock, nil
		}
	}
	// Suggestions that repeat the proposal are not corrections.
	if v.Correction == a.Statement {
		v.Correction = ""
	}
	if v.Date == a.Time.Format("2006-01-02") {
		v.Date = ""
	}
	if len(v.Clock) > 0 {
		shape := u.Subject.Shape()
		if c, err := u.subject().Clock(v.Clock); err == nil && strings.Join(axiom.ClockValues(c, shape), " ") == strings.Join(axiom.ClockValues(a.Clock, shape), " ") {
			v.Clock = nil
		}
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
	results := make([][]Proposal, len(proposed))
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
			ps, err := u.confirmOne(ctx, i, len(proposed), a, known)
			if err != nil {
				errMu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				errMu.Unlock()
				cancel()
				return
			}
			results[i] = ps
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
	for _, ps := range results {
		out = append(out, ps...)
	}
	return out, summary, nil
}

// confirmOne runs the confirmation for proposal i of n and, when the agent
// suggests a correction or a split, the confirmation of each replacement.
// It returns the proposals that result: usually one, several for a split,
// none when the proposal turns out to be prior knowledge or already in the
// log. A non-nil error means the run was cancelled.
func (u *Updater) confirmOne(ctx context.Context, i, n int, a axiom.Axiom, known *idSet) ([]Proposal, error) {
	tag := fmt.Sprintf("[%d/%d]", i+1, n)
	u.logf("%s confirming: %s", tag, oneLine(a.Statement, 100))
	p := Proposal{Axiom: a}
	v, err := u.Confirm(ctx, a)
	if err != nil {
		if ctx.Err() != nil {
			return nil, err
		}
		p.Error = err.Error()
		u.logf("%s confirmation failed: %v", tag, err)
		return []Proposal{p}, nil
	}
	p.Verdict = v
	switch {
	case v.Confirmed:
		u.logf("%s confirmed: %s", tag, oneLine(v.Reason, 160))
		return []Proposal{p}, nil
	case !v.Suggests():
		u.logf("%s unfounded: %s", tag, oneLine(v.Reason, 160))
		return []Proposal{p}, nil
	}
	u.logf("%s inaccurate: %s", tag, oneLine(v.Reason, 160))
	u.logf("%s suggested instead: %s", tag, oneLine(v.Suggestion(), 300))
	replacements, err := p.Suggested(u.subject())
	if errors.Is(err, ErrPriorKnowledge) {
		u.logf("%s %v; dropping the proposal", tag, err)
		return nil, nil
	}
	if err != nil {
		u.logf("%s the suggestion cannot be applied (%v); leaving the proposal for review", tag, err)
		return []Proposal{p}, nil
	}
	split := len(v.Split) > 0
	var out []Proposal
	for j, r := range replacements {
		what := "correction"
		if split {
			what = fmt.Sprintf("part %d/%d", j+1, len(replacements))
		}
		if known.has(r.Axiom.ID) {
			u.logf("%s the %s is already in the log; dropping it", tag, what)
			continue
		}
		v2, err := u.Confirm(ctx, r.Axiom)
		switch {
		case err != nil && ctx.Err() != nil:
			return nil, err
		case err != nil:
			u.logf("%s confirmation of the %s failed: %v", tag, what, err)
			if split {
				r.Error = err.Error()
				out = append(out, r)
			} else {
				out = append(out, p)
			}
		case v2.Confirmed:
			u.logf("%s %s confirmed: %s", tag, what, oneLine(v2.Reason, 160))
			r.Verdict = v2
			known.add(r.Axiom.ID)
			out = append(out, r)
		default:
			u.logf("%s %s not confirmed: %s", tag, what, oneLine(v2.Reason, 160))
			if split {
				// The part stands on its own for review, with the
				// original attached and the part's own verdict.
				r.Verdict = v2
				out = append(out, r)
			} else {
				out = append(out, p)
			}
		}
	}
	return out, nil
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
// q(uit) and, when the confirmation agent suggested a correction to the
// statement, date or clock, c(orrection) to accept that instead.
func Review(pending []Proposal, in io.Reader, out io.Writer, subject axiom.Subject) ([]axiom.Axiom, error) {
	r := bufio.NewReader(in)
	shape := subject.Shape
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
			fmt.Fprintf(out, "  inaccurate: %s\n", p.Verdict.Reason)
			if st := strings.TrimSpace(p.Verdict.Correction); st != "" {
				fmt.Fprintf(out, "  suggested statement: %s\n", st)
			}
			if d := strings.TrimSpace(p.Verdict.Date); d != "" {
				fmt.Fprintf(out, "  suggested date: %s\n", d)
			}
			if len(p.Verdict.Clock) > 0 {
				fmt.Fprintf(out, "  suggested clock: %s\n", strings.Join(p.Verdict.Clock, " "))
			}
			if len(p.Verdict.Split) > 0 {
				fmt.Fprintf(out, "  suggested split into %d:\n", len(p.Verdict.Split))
				for i, part := range p.Verdict.Split {
					fmt.Fprintf(out, "    %d. %s\n", i+1, part.describe())
				}
			}
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
				replacements, err := p.Suggested(subject)
				if err != nil {
					fmt.Fprintf(out, "  cannot apply the suggestion: %v\n", err)
					continue
				}
				for _, r := range replacements {
					a := r.Axiom
					a.By = "user"
					accepted = append(accepted, a)
				}
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
