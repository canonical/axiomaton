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
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/microsoft/agent-framework-go/tool"

	"github.com/canonical/axiomaton/internal/axiom"
	"github.com/canonical/axiomaton/internal/subjects"
	"github.com/canonical/axiomaton/internal/subjects/snapcraft"
)

type fakeContainer struct {
	rt     *fakeRuntime
	closed bool
	// cmds and pushes record what happened in this container, in order.
	cmds   []string
	pushes []string
}

func (f *fakeContainer) Exec(_ context.Context, cmd string, _ time.Duration) (ExecResult, error) {
	f.cmds = append(f.cmds, cmd)
	if f.rt != nil && f.rt.fail != "" && strings.Contains(cmd, f.rt.fail) {
		return ExecResult{ExitCode: 7, Stderr: "boom"}, nil
	}
	return ExecResult{Stdout: "ran: " + cmd}, nil
}

func (f *fakeContainer) Push(_ context.Context, local, remote string) error {
	f.pushes = append(f.pushes, local+" -> "+remote)
	return nil
}

func (f *fakeContainer) Close(context.Context) error {
	if f.rt.hold != nil {
		<-f.rt.hold
	}
	f.rt.mu.Lock()
	f.closed = true
	f.rt.running--
	f.rt.mu.Unlock()
	return nil
}

type fakeRuntime struct {
	mu      sync.Mutex
	started []*fakeContainer
	// running counts live containers; peak is the most seen at once.
	running, peak int
	// hold keeps each container open until released, to observe overlap.
	hold chan struct{}
	// fail makes any command containing it exit non-zero.
	fail string
}

func (f *fakeRuntime) Start(context.Context) (Container, error) {
	c := &fakeContainer{rt: f}
	f.mu.Lock()
	f.started = append(f.started, c)
	f.running++
	f.peak = max(f.peak, f.running)
	f.mu.Unlock()
	return c, nil
}

func (f *fakeRuntime) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.started)
}

// fakeAgent drives tools directly: the update run proposes two axioms, the
// confirm run confirms statements containing "true".
type fakeAgent struct {
	t    *testing.T
	mu   sync.Mutex
	runs int
}

func (f *fakeAgent) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.runs
}

func call(t *testing.T, tools []tool.Tool, name string, in any) string {
	t.Helper()
	for _, tl := range tools {
		if tl.Name() != name {
			continue
		}
		args, _ := json.Marshal(in)
		ft := tl.(tool.FuncTool)
		out, err := ft.Call(context.Background(), string(args))
		if err != nil {
			t.Fatalf("invoke %s: %v", name, err)
		}
		b, _ := json.Marshal(out)
		return string(b)
	}
	t.Fatalf("tool %s not found", name)
	return ""
}

func (f *fakeAgent) Run(_ context.Context, instructions, task string, tools []tool.Tool, out any) (string, error) {
	f.mu.Lock()
	f.runs++
	f.mu.Unlock()
	if out == nil {
		// Update agent.
		if !strings.Contains(instructions, "append-only log") || !strings.Contains(task, "Your task") {
			return "", nil
		}
		f.t.Logf("shell: %s", call(f.t, tools, "shell", ShellInput{Command: "apt-get update"}))
		call(f.t, tools, "propose_axiom", ProposeInput{Date: "2027-02-09", Clock: []string{"1.28.0"}, Statement: "this is true", Source: "https://go.dev/doc/go1.28"})
		call(f.t, tools, "propose_axiom", ProposeInput{Date: "2027-02-09", Clock: []string{"1.28.0"}, Statement: "this is dubious", Source: "https://go.dev/doc/go1.28"})
		call(f.t, tools, "propose_axiom", ProposeInput{Date: "2027-02-09", Clock: []string{"1.28.0"}, Statement: "this is nearly true", Source: "https://go.dev/doc/go1.28"})
		call(f.t, tools, "propose_axiom", ProposeInput{Date: "2027-02-09", Clock: []string{"1.28.0"}, Statement: "this is slightly off", Source: "https://go.dev/doc/go1.28"})
		call(f.t, tools, "propose_axiom", ProposeInput{Date: "2027-02-09", Clock: []string{"1.28.0"}, Statement: "this is misdated", Source: "https://go.dev/doc/go1.28"})
		call(f.t, tools, "propose_axiom", ProposeInput{Date: "2027-02-09", Clock: []string{"1.28.0"}, Statement: "this is ancient", Source: "https://go.dev/doc/go1.28"})
		call(f.t, tools, "propose_axiom", ProposeInput{Date: "2027-02-09", Clock: []string{"1.28.0"}, Statement: "this is badly clocked", Source: "https://go.dev/doc/go1.28"})
		call(f.t, tools, "propose_axiom", ProposeInput{Date: "2027-02-09", Clock: []string{"1.28.0"}, Statement: "this is two things", Source: "https://go.dev/doc/go1.28"})
		// Duplicate and invalid proposals must be rejected, not crash.
		if r := call(f.t, tools, "propose_axiom", ProposeInput{Date: "2027-02-09", Clock: []string{"1.28.0"}, Statement: "this is true"}); !strings.Contains(r, "already recorded") {
			f.t.Fatalf("duplicate not rejected: %s", r)
		}
		if r := call(f.t, tools, "propose_axiom", ProposeInput{Date: "bad", Clock: []string{"1.28.0"}, Statement: "x"}); !strings.Contains(r, "invalid date") {
			f.t.Fatalf("bad date not rejected: %s", r)
		}
		if r := call(f.t, tools, "propose_axiom", ProposeInput{Date: "2023-07-31", Clock: []string{"1.21.0"}, Statement: "old news"}); !strings.Contains(r, "not after the global epoch 2023-08-01") {
			f.t.Fatalf("pre-epoch proposal not rejected: %s", r)
		}
		if r := call(f.t, tools, "propose_axiom", ProposeInput{Date: "2023-08-01", Clock: []string{"1.21.0"}, Statement: "on the epoch"}); !strings.Contains(r, "not after the global epoch") {
			f.t.Fatalf("epoch day itself is prior knowledge: %s", r)
		}
		if r := call(f.t, tools, "propose_axiom", ProposeInput{Date: "2023-08-02", Clock: []string{"1.21.0"}, Statement: "on the epoch", Source: "https://go.dev"}); strings.Contains(r, "epoch") {
			f.t.Fatalf("the day after the epoch must be accepted: %s", r)
		}
		if !strings.Contains(instructions, "Hard limit: never propose an axiom dated on or before 2023-08-01") || !strings.Contains(task, "after\n   2023-08-01, the global epoch") {
			f.t.Fatalf("prompts lack the epoch limit:\n%s\n%s", instructions, task)
		}
		if r := call(f.t, tools, "propose_axiom", ProposeInput{Date: "2027-02-09", Clock: []string{"banana"}, Statement: "x"}); !strings.Contains(r, "not a Go version") {
			f.t.Fatalf("bad clock not rejected: %s", r)
		}
		if r := call(f.t, tools, "propose_axiom", ProposeInput{Date: "2027-02-09", Statement: "x"}); !strings.Contains(r, "clock is required") {
			f.t.Fatalf("missing clock not rejected: %s", r)
		}
		return "covered 1.28.0", nil
	}
	v := out.(*Verdict)
	if !strings.Contains(instructions, "distinguish two cases") || !strings.Contains(task, "a correction giving the") {
		f.t.Fatalf("confirmation prompts do not ask for a correction:\n%s\n%s", instructions, task)
	}
	switch {
	case strings.Contains(task, "Statement: this is true"), strings.Contains(task, "Statement: this is exactly true"):
		v.Confirmed = true
		v.Reason = "checked"
	case strings.Contains(task, "Statement: this is nearly true"):
		// Inaccurate: the correction is then confirmed by a fresh agent.
		v.Reason = "overstated"
		v.Correction = " this is exactly true "
	case strings.Contains(task, "Statement: this is slightly off"):
		// Inaccurate, but the correction does not survive confirmation.
		v.Reason = "wrong release"
		v.Correction = "this is still off"
	case strings.Contains(task, "Statement: this is still off"):
		v.Reason = "also wrong"
	case strings.Contains(task, "Statement: this is misdated") && strings.Contains(task, "Go release: 1.28.1 (released 2027-03-02)"):
		// The re-confirmation of the corrected date and clock.
		v.Confirmed = true
		v.Reason = "right release now"
	case strings.Contains(task, "Statement: this is misdated"):
		// The statement is fine; the release and date are not. A clock
		// equal to the proposal's is noise and dropped.
		v.Reason = "wrong release"
		v.Date = "2027-03-02"
		v.Clock = []string{"1.28.1"}
	case strings.Contains(task, "Statement: this is ancient"):
		// Dated before the epoch: prior knowledge, dropped.
		v.Reason = "this shipped long ago"
		v.Date = "2019-02-25"
		v.Clock = []string{"1.12.0"}
	case strings.Contains(task, "Statement: this is badly clocked"):
		v.Reason = "wrong release"
		v.Clock = []string{"banana"}
	case strings.Contains(task, "Statement: this is two things"):
		// Bundled facts: split into two parts, each confirmed on its own;
		// the second belongs to a later release.
		v.Reason = "two facts in one"
		v.Split = []Part{{Statement: "this is one thing"}, {Statement: "this is another thing", Date: "2027-03-02", Clock: []string{"1.28.1"}}}
	case strings.Contains(task, "Statement: this is one thing"):
		v.Confirmed = true
		v.Reason = "first part holds"
	default:
		v.Reason = "checked"
	}
	return "", nil
}

func newFakeAgent(t *testing.T) *fakeAgent { return &fakeAgent{t: t} }

func TestUpdaterRun(t *testing.T) {
	rt := &fakeRuntime{}
	ag := newFakeAgent(t)
	b, _ := subjects.Lookup("go")
	u := &Updater{Subject: b, Runtime: rt, Agent: ag, ExecTimeout: time.Second, Log: t.Logf}
	props, summary, err := u.Run(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	// One update run, nine confirmations and five re-confirmations of
	// suggestions (nearly true, slightly off, misdated, and both parts of
	// the split); the ancient proposal is dropped as prior knowledge
	// without one, and the unusable clock suggestion is left for review.
	// The split turns one proposal into two.
	if summary != "covered 1.28.0" || len(props) != 9 || ag.count() != 15 {
		t.Fatalf("props=%d summary=%q runs=%d", len(props), summary, ag.count())
	}
	if rt.count() != 15 || rt.peak != 1 {
		t.Fatalf("expected a fresh container per agent, one at a time, got %d (peak %d)", rt.count(), rt.peak)
	}
	for _, c := range rt.started {
		if !c.closed {
			t.Fatal("container not closed")
		}
	}
	confirmed, pending := Split(props)
	if len(confirmed) != 4 || confirmed[0].Statement != "this is true" || confirmed[0].By != "agent" || confirmed[3].Statement != "this is one thing" || confirmed[3].Clock["version"] != "1.28.0" {
		t.Fatalf("confirmed: %+v", confirmed)
	}
	for _, p := range props {
		if p.Axiom.Statement == "this is one thing" && (p.Original == nil || p.Original.Statement != "this is two things") {
			t.Fatalf("split part should keep the original: %+v", p)
		}
	}
	if c := confirmed[2]; c.Statement != "this is misdated" || c.Time.Format("2006-01-02") != "2027-03-02" || c.Clock["version"] != "1.28.1" || c.By != "agent" {
		t.Fatalf("date and clock correction: %+v", c)
	}
	for _, p := range props {
		if p.Axiom.Statement == "this is ancient" {
			t.Fatalf("pre-epoch correction should drop the proposal: %+v", p)
		}
	}
	if confirmed[1].Statement != "this is exactly true" || confirmed[1].By != "agent" || confirmed[1].Source != "https://go.dev/doc/go1.28" || confirmed[1].ID == "" {
		t.Fatalf("corrected axiom: %+v", confirmed[1])
	}
	if props[2].Original == nil || props[2].Original.Statement != "this is nearly true" || props[2].Verdict.Correction != "" || !props[2].Verdict.Confirmed {
		t.Fatalf("corrected proposal should keep the original: %+v", props[2])
	}
	if len(pending) != 5 || pending[0].Axiom.Statement != "this is dubious" || pending[0].Verdict.Suggests() || pending[4].Axiom.Statement != "on the epoch" {
		t.Fatalf("pending: %+v", pending)
	}
	if p := pending[3]; p.Axiom.Statement != "this is another thing" || p.Axiom.Clock["version"] != "1.28.1" || p.Axiom.Time.Format("2006-01-02") != "2027-03-02" || p.Original == nil || p.Original.Statement != "this is two things" || p.Verdict.Reason != "checked" {
		t.Fatalf("unconfirmed split part stands alone for review: %+v", p)
	}
	if p := pending[2]; p.Axiom.Statement != "this is badly clocked" || !p.Verdict.Suggests() || p.Verdict.Suggestion() != "clock: banana" {
		t.Fatalf("unusable suggestion stays for review: %+v", p)
	}
	if p := pending[1]; p.Axiom.Statement != "this is slightly off" || !p.Verdict.Suggests() || p.Verdict.Correction != "this is still off" || p.Original != nil {
		t.Fatalf("unconfirmed correction should stay a suggestion: %+v", p)
	}

	var out strings.Builder
	accepted, err := Review(pending, strings.NewReader("c\n?\ne\nedited statement\nc\nc\nn\nn\nn\n"), &out, subjects.AxiomSubject(b))
	if err != nil {
		t.Fatal(err)
	}
	if len(accepted) != 2 || accepted[0].Statement != "edited statement" || accepted[0].By != "user" || accepted[1].Statement != "this is still off" || accepted[1].By != "user" {
		t.Fatalf("accepted: %+v", accepted)
	}
	if o := out.String(); !strings.Contains(o, "not confirmed: checked") || !strings.Contains(o, "inaccurate: wrong release\n  suggested statement: this is still off") || !strings.Contains(o, "[y/n/c=suggestion/e/q]") || !strings.Contains(o, "suggested clock: banana") || !strings.Contains(o, `cannot apply the suggestion: suggested clock: subject go dimension version: "banana" is not a Go version`) {
		t.Fatalf("review output:\n%s", o)
	}
	if strings.Count(out.String(), "[y/n/e/q]") < 2 {
		t.Fatalf("c without a suggestion should re-prompt:\n%s", out.String())
	}
	// A date and clock suggestion is applied by c.
	dated := Proposal{Axiom: pending[0].Axiom, Verdict: Verdict{Reason: "wrong release", Date: "2027-03-02", Clock: []string{"1.28.1"}}}
	accepted, _ = Review([]Proposal{dated}, strings.NewReader("c\n"), &out, subjects.AxiomSubject(b))
	if len(accepted) != 1 || accepted[0].Statement != "this is dubious" || accepted[0].Time.Format("2006-01-02") != "2027-03-02" || accepted[0].Clock["version"] != "1.28.1" {
		t.Fatalf("dated suggestion: %+v", accepted)
	}
	if !strings.Contains(out.String(), "suggested date: 2027-03-02\n  suggested clock: 1.28.1") {
		t.Fatalf("review output:\n%s", out.String())
	}
	// A split suggestion is shown part by part and c accepts every part.
	twofold := Proposal{Axiom: pending[0].Axiom, Verdict: Verdict{Reason: "two facts", Split: []Part{{Statement: "part one"}, {Statement: "part two", Clock: []string{"1.28.1"}}}}}
	out.Reset()
	accepted, _ = Review([]Proposal{twofold}, strings.NewReader("c\n"), &out, subjects.AxiomSubject(b))
	if len(accepted) != 2 || accepted[0].Statement != "part one" || accepted[1].Statement != "part two" || accepted[1].Clock["version"] != "1.28.1" || accepted[1].By != "user" {
		t.Fatalf("split accepted: %+v", accepted)
	}
	if !strings.Contains(out.String(), "suggested split into 2:\n    1. part one\n    2. part two [1.28.1]\n") {
		t.Fatalf("review output:\n%s", out.String())
	}
	accepted, _ = Review(pending, strings.NewReader("n\nn\nn\nn\nn\n"), &out, subjects.AxiomSubject(b))
	if len(accepted) != 0 {
		t.Fatal("declined should not be accepted")
	}

	path := t.TempDir() + "/axiomlog.pending"
	if err := WritePending(path, pending); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), `"correction":"this is still off"`) || !strings.Contains(string(data), `"clock":["banana"]`) {
		t.Fatalf("pending file lacks the suggestion:\n%s", data)
	}
}

func TestBatchTool(t *testing.T) {
	bt, err := BatchTool(t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	in := BatchInput{Commits: []BatchCommit{
		{Hash: "c1c1c1c1c1c1c1c1", Time: "2025-03-02T09:00:00Z", Go: "go1.23"},
		{Hash: "c3c3c3c3c3c3c3c3", Time: "2025-03-20T10:00:00Z", Go: "1.23"},
		{Hash: "c5c5c5c5c5c5c5c5", Time: "2025-04-03T10:00:00+02:00", Go: "1.23"},
	}}
	raw := call(t, []tool.Tool{bt}, "batch_commits", in)
	var out BatchOutput
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Releases) != 2 || out.Releases[0].Version != "v0.0.0-20250320100000-c3c3c3c3c3c3" || out.Releases[0].Date != "2025-03-20" || out.Releases[0].Commits != 2 || out.Releases[1].Reason != "new calendar month" || out.Releases[1].GoVersion != "1.23" {
		t.Fatalf("releases: %+v", out.Releases)
	}
	if !strings.Contains(out.Message, "3 commit(s) grouped into 2 release(s)") {
		t.Fatalf("message: %s", out.Message)
	}
	raw = call(t, []tool.Tool{bt}, "batch_commits", BatchInput{Commits: []BatchCommit{{Hash: "x", Time: "yesterday"}}})
	if !strings.Contains(raw, "not RFC 3339") {
		t.Fatalf("bad time: %s", raw)
	}

	// The update agent gets the tool only for module subjects.
	rt := &fakeRuntime{}
	seen := map[string]bool{}
	spy := &toolSpy{seen: seen}
	mod, _ := subjects.Lookup("go:golang.org/x/exp")
	if _, _, err := (&Updater{Subject: mod, Runtime: rt, Agent: spy, ExecTimeout: time.Second}).Propose(context.Background(), nil); err != nil || !seen["batch_commits"] || !seen["propose_axiom"] {
		t.Fatalf("module tools: %v %v", seen, err)
	}
	seen = map[string]bool{}
	spy.seen = seen
	lang, _ := subjects.Lookup("go")
	if _, _, err := (&Updater{Subject: lang, Runtime: rt, Agent: spy, ExecTimeout: time.Second}).Propose(context.Background(), nil); err != nil || seen["batch_commits"] {
		t.Fatalf("language tools: %v %v", seen, err)
	}
}

// toolSpy records the tool names offered to it and does nothing else.
type toolSpy struct{ seen map[string]bool }

func (s *toolSpy) Run(_ context.Context, _, _ string, tools []tool.Tool, _ any) (string, error) {
	for _, tl := range tools {
		s.seen[tl.Name()] = true
	}
	return "", nil
}

func TestCorrected(t *testing.T) {
	subject := axiom.Subject{Name: "go", Shape: []axiom.ClockShape{{Name: "version", Kind: axiom.KindGoVersion}}}
	base := axiom.Axiom{Subject: "go", Time: time.Date(2027, 2, 9, 0, 0, 0, 0, time.UTC), Clock: map[string]string{"version": "1.28.0"}, Statement: "s", Source: "u"}
	base.Normalise()
	if _, err := (Proposal{Axiom: base, Verdict: Verdict{Date: "yesterday"}}).Corrected(subject); err == nil || !strings.Contains(err.Error(), "YYYY-MM-DD") {
		t.Fatalf("bad date: %v", err)
	}
	if _, err := (Proposal{Axiom: base, Verdict: Verdict{Date: "2023-08-01"}}).Corrected(subject); err != ErrPriorKnowledge {
		t.Fatalf("epoch: %v", err)
	}
	if _, err := (Proposal{Axiom: base, Verdict: Verdict{Clock: []string{"1.28.0", "extra"}}}).Corrected(subject); err == nil {
		t.Fatal("too many clock values")
	}
	if _, err := (Proposal{Axiom: base, Verdict: Verdict{Correction: "s", Clock: []string{"1.28"}}}).Corrected(subject); err == nil || !strings.Contains(err.Error(), "changes nothing") {
		t.Fatalf("no-op: %v", err)
	}
	fixed, err := (Proposal{Axiom: base, Verdict: Verdict{Correction: " t ", Date: "2027-03-02", Clock: []string{"1.28.1"}}}).Corrected(subject)
	if err != nil {
		t.Fatal(err)
	}
	if fixed.Axiom.Statement != "t" || fixed.Axiom.Clock["version"] != "1.28.1" || fixed.Axiom.Time.Day() != 2 || fixed.Axiom.Source != "u" || fixed.Original.ID != base.ID || fixed.Axiom.ID == base.ID {
		t.Fatalf("fixed: %+v", fixed)
	}

	// Splits.
	split := func(parts ...Part) Proposal { return Proposal{Axiom: base, Verdict: Verdict{Split: parts}} }
	if _, err := split(Part{Statement: "a"}, Part{}).Suggested(subject); err == nil || !strings.Contains(err.Error(), "part 2 of the split has no statement") {
		t.Fatalf("empty part: %v", err)
	}
	if _, err := split(Part{Statement: "a"}, Part{Statement: "a"}).Suggested(subject); err == nil || !strings.Contains(err.Error(), "repeats") {
		t.Fatalf("duplicate part: %v", err)
	}
	if _, err := split(Part{Statement: "a"}, Part{Statement: "b", Clock: []string{"banana"}}).Suggested(subject); err == nil || !strings.Contains(err.Error(), "part 2 of the split: suggested clock") {
		t.Fatalf("bad clock part: %v", err)
	}
	if _, err := split(Part{Statement: "old", Date: "2020-01-01"}).Suggested(subject); err != ErrPriorKnowledge {
		t.Fatalf("all parts prior knowledge: %v", err)
	}
	parts, err := split(Part{Statement: "old", Date: "2020-01-01"}, Part{Statement: "new"}, Part{Statement: "later", Date: "2027-03-02", Clock: []string{"1.28.1"}}).Suggested(subject)
	if err != nil || len(parts) != 2 || parts[0].Axiom.Statement != "new" || parts[0].Axiom.Clock["version"] != "1.28.0" || parts[1].Axiom.Clock["version"] != "1.28.1" || parts[1].Original.ID != base.ID || parts[0].Axiom.Source != "u" {
		t.Fatalf("parts: %v %+v", err, parts)
	}
	if _, err := split(Part{Statement: "a"}, Part{Statement: "b"}).Corrected(subject); err == nil {
		t.Fatal("Corrected should refuse a split")
	}
	// A single correction still comes back as one proposal.
	one, err := (Proposal{Axiom: base, Verdict: Verdict{Correction: "t"}}).Suggested(subject)
	if err != nil || len(one) != 1 || one[0].Axiom.Statement != "t" {
		t.Fatalf("single: %v %+v", err, one)
	}
	if got := (Verdict{Split: []Part{{Statement: "a"}, {Statement: "b", Date: "2027-03-02", Clock: []string{"1.28.1"}}}}).Suggestion(); got != "split into 2: 1. a 2. b [2027-03-02 1.28.1]" {
		t.Fatalf("suggestion: %q", got)
	}
}

func TestSuggestedClockSpelling(t *testing.T) {
	subject := subjects.AxiomSubject(snapcraft.Subject{})
	p := Proposal{
		Axiom:   axiom.Axiom{Subject: "snapcraft", Time: time.Date(2025, 9, 1, 0, 0, 0, 0, time.UTC), Clock: map[string]string{"base": "24", "version": "8.10.0"}, Statement: "core26 is supported"},
		Verdict: Verdict{Confirmed: false, Clock: []string{"core26", "8.11.0"}, Reason: "lands in 8.11"},
	}
	p.Axiom.Normalise()
	rs, err := p.Suggested(subject)
	if err != nil || len(rs) != 1 || rs[0].Axiom.Clock["base"] != "26" || rs[0].Axiom.Clock["version"] != "8.11.0" || rs[0].Original == nil {
		t.Fatalf("a base spelt core26 must be applied as 26: %+v %v", rs, err)
	}
	p.Verdict.Clock = []string{"bare", "8.11.0"}
	if _, err := p.Suggested(subject); err == nil || !strings.Contains(err.Error(), "is not a number") {
		t.Fatalf("unrecognised base must still be refused: %v", err)
	}
}

func TestCorrectionAlreadyKnown(t *testing.T) {
	// A correction that matches an existing axiom is dropped rather than
	// confirmed again.
	rt := &fakeRuntime{}
	ag := newFakeAgent(t)
	b, _ := subjects.Lookup("go")
	existing := axiom.Axiom{Subject: "go", Time: time.Date(2027, 2, 9, 0, 0, 0, 0, time.UTC), Clock: map[string]string{"version": "1.28.0"}, Statement: "this is exactly true"}
	existing.Normalise()
	u := &Updater{Subject: b, Runtime: rt, Agent: ag, ExecTimeout: time.Second, Log: t.Logf}
	props, _, err := u.Run(context.Background(), []axiom.Axiom{existing})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range props {
		if p.Axiom.Statement == "this is exactly true" || p.Axiom.Statement == "this is nearly true" {
			t.Fatalf("known correction should be dropped: %+v", p)
		}
	}
	// Nine proposals less the known correction and the ancient one, plus
	// one for the split; one update run, nine confirmations and four
	// re-confirmations (slightly off, misdated, two split parts).
	if len(props) != 8 || ag.count() != 14 {
		t.Fatalf("props=%d runs=%d", len(props), ag.count())
	}
}

func TestParallelConfirmation(t *testing.T) {
	// Containers block in Close until released, so with Parallel 3 the
	// first three confirmations must all be open at once; results come
	// back in proposal order regardless of completion order.
	rt := &fakeRuntime{hold: make(chan struct{})}
	ag := newFakeAgent(t)
	b, _ := subjects.Lookup("go")
	u := &Updater{Subject: b, Runtime: rt, Agent: ag, ExecTimeout: time.Second, Parallel: 3, Log: t.Logf}
	done := make(chan struct{})
	var props []Proposal
	var err error
	go func() {
		defer close(done)
		props, _, err = u.Run(context.Background(), nil)
	}()
	// The update agent's container, then three confirmations.
	for range 4 {
		rt.hold <- struct{}{}
	}
	for {
		rt.mu.Lock()
		peak := rt.peak
		rt.mu.Unlock()
		if peak >= 3 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	close(rt.hold)
	<-done
	if err != nil {
		t.Fatal(err)
	}
	if rt.peak > 3 {
		t.Fatalf("more than 3 containers at once: %d", rt.peak)
	}
	var order []string
	for _, p := range props {
		order = append(order, p.Axiom.Statement)
	}
	if got := strings.Join(order, "|"); got != "this is true|this is dubious|this is exactly true|this is slightly off|this is misdated|this is badly clocked|this is one thing|this is another thing|on the epoch" {
		t.Fatalf("order: %s", got)
	}
	if rt.count() != 15 {
		t.Fatalf("containers: %d", rt.count())
	}

	// Cancellation stops the fan-out and reports the context error.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rt2 := &fakeRuntime{}
	u2 := &Updater{Subject: b, Runtime: rt2, Agent: &cancelAgent{inner: newFakeAgent(t)}, ExecTimeout: time.Second, Parallel: 2, Log: t.Logf}
	if _, _, err := u2.Run(ctx, nil); err == nil {
		t.Fatal("cancelled run should fail")
	}
}

// cancelAgent lets the update run through but fails confirmations with the
// context's error, as a real agent would once cancelled.
type cancelAgent struct{ inner *fakeAgent }

func (c *cancelAgent) Run(ctx context.Context, instructions, task string, tools []tool.Tool, out any) (string, error) {
	if out == nil {
		return c.inner.Run(ctx, instructions, task, tools, out)
	}
	return "", ctx.Err()
}

func TestTruncate(t *testing.T) {
	s, tr := truncate(strings.Repeat("x", maxToolOutput+10))
	if !tr || !strings.Contains(s, "truncated") {
		t.Fatal("not truncated")
	}
	if _, tr := truncate("short"); tr {
		t.Fatal("short truncated")
	}
	a := axiom.Axiom{}
	_ = a
}

// preparedSubject wraps a built-in with shipments and a setup command.
type preparedSubject struct {
	subjects.Builtin
	ships []Shipment
	setup string
}

func (p preparedSubject) Shipments() []Shipment { return p.ships }
func (p preparedSubject) Setup() string         { return p.setup }

func TestPrepared(t *testing.T) {
	rt := &fakeRuntime{}
	ag := newFakeAgent(t)
	b, _ := subjects.Lookup("go")
	sub := preparedSubject{Builtin: b, ships: []Shipment{{Local: "/tmp/a.bundle", Remote: "/work/src/a.bundle"}}, setup: "apt-get install -y juju"}
	u := &Updater{Subject: sub, Runtime: rt, Agent: ag, ExecTimeout: time.Second, Log: t.Logf}
	if _, _, err := u.Propose(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	c := rt.started[0]
	if len(c.pushes) != 1 || c.pushes[0] != "/tmp/a.bundle -> /work/src/a.bundle" {
		t.Fatalf("pushes: %v", c.pushes)
	}
	if len(c.cmds) == 0 || c.cmds[0] != "apt-get install -y juju" {
		t.Fatalf("setup must run first: %v", c.cmds)
	}
	if _, err := u.Confirm(context.Background(), axiom.Axiom{Subject: "go", Statement: "this is true"}); err != nil {
		t.Fatal(err)
	}
	if c := rt.started[1]; len(c.pushes) != 1 || len(c.cmds) == 0 || c.cmds[0] != "apt-get install -y juju" {
		t.Fatalf("confirmation sandboxes are prepared too: %v %v", c.pushes, c.cmds)
	}

	rt.fail = "juju"
	if _, _, err := u.Propose(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "setup exited 7: boom") {
		t.Fatalf("failed setup: %v", err)
	}
	if rt.started[2].closed != true {
		t.Fatal("the sandbox is removed after a failed setup")
	}

	// A plain built-in is not prepared at all.
	rt = &fakeRuntime{}
	u = &Updater{Subject: b, Runtime: rt, Agent: ag, ExecTimeout: time.Second}
	u.Propose(context.Background(), nil)
	if c := rt.started[0]; len(c.pushes) != 0 || (len(c.cmds) > 0 && c.cmds[0] == "apt-get install -y juju") {
		t.Fatalf("unexpected preparation: %v %v", c.pushes, c.cmds)
	}
}

func TestCommitBatchTool(t *testing.T) {
	bt, err := CommitBatchTool(t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(bt.Description(), "full hash of its last commit") {
		t.Fatalf("description: %s", bt.Description())
	}
	in := `{"commits":[
		{"hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","time":"2025-03-01T10:00:00Z"},
		{"hash":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","time":"2025-03-20T10:00:00Z"},
		{"hash":"cccccccccccccccccccccccccccccccccccccccc","time":"2025-04-02T10:00:00Z"}]}`
	res, err := bt.Call(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	out := res.(BatchOutput)
	if len(out.Releases) != 2 || out.Releases[0].Version != "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" || out.Releases[1].Version != "cccccccccccccccccccccccccccccccccccccccc" || out.Releases[1].Date != "2025-04-02" {
		t.Fatalf("releases: %+v", out.Releases)
	}
	if !strings.Contains(out.Message, "last_commit hash as the clock") {
		t.Fatalf("message: %s", out.Message)
	}

	// A git-clocked subject that is not a Go module gets this tool.
	spy := &toolSpy{seen: map[string]bool{}}
	rt := &fakeRuntime{}
	tree := preparedSubject{Builtin: fakeGitSubject{}}
	u := &Updater{Subject: tree, Runtime: rt, Agent: spy, ExecTimeout: time.Second}
	u.Propose(context.Background(), nil)
	if !spy.seen["batch_commits"] {
		t.Fatalf("git clock subject lacks batch_commits: %v", spy.seen)
	}
}

// fakeGitSubject is a minimal subject with a git ordered clock.
type fakeGitSubject struct{ subjects.Builtin }

func (fakeGitSubject) Name() string { return "tree" }
func (fakeGitSubject) Shape() []axiom.ClockShape {
	return []axiom.ClockShape{{Name: "commit", Kind: axiom.KindGit, Repo: "/r"}}
}
func (fakeGitSubject) UpdateInstructions() string             { return "i" }
func (fakeGitSubject) UpdateTask([]axiom.Axiom) string        { return "t" }
func (fakeGitSubject) ConfirmInstructions() string            { return "ci" }
func (fakeGitSubject) ConfirmTask(axiom.Axiom) string         { return "ct" }
func (fakeGitSubject) Seed() ([]axiom.Axiom, error)           { return nil, nil }
func (fakeGitSubject) Detect(string) ([]string, string, bool) { return nil, "", false }
