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
)

type fakeContainer struct {
	rt     *fakeRuntime
	closed bool
}

func (f *fakeContainer) Exec(_ context.Context, cmd string, _ time.Duration) (ExecResult, error) {
	return ExecResult{Stdout: "ran: " + cmd}, nil
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
	// One update run, five confirmations and two re-confirmations of
	// suggested corrections.
	if summary != "covered 1.28.0" || len(props) != 5 || ag.count() != 8 {
		t.Fatalf("props=%d summary=%q runs=%d", len(props), summary, ag.count())
	}
	if rt.count() != 8 || rt.peak != 1 {
		t.Fatalf("expected a fresh container per agent, one at a time, got %d (peak %d)", rt.count(), rt.peak)
	}
	for _, c := range rt.started {
		if !c.closed {
			t.Fatal("container not closed")
		}
	}
	confirmed, pending := Split(props)
	if len(confirmed) != 2 || confirmed[0].Statement != "this is true" || confirmed[0].By != "agent" {
		t.Fatalf("confirmed: %+v", confirmed)
	}
	if confirmed[1].Statement != "this is exactly true" || confirmed[1].By != "agent" || confirmed[1].Source != "https://go.dev/doc/go1.28" || confirmed[1].ID == "" {
		t.Fatalf("corrected axiom: %+v", confirmed[1])
	}
	if props[2].Original == nil || props[2].Original.Statement != "this is nearly true" || props[2].Verdict.Correction != "" || !props[2].Verdict.Confirmed {
		t.Fatalf("corrected proposal should keep the original: %+v", props[2])
	}
	if len(pending) != 3 || pending[0].Axiom.Statement != "this is dubious" || pending[0].Verdict.Suggests() || pending[2].Axiom.Statement != "on the epoch" {
		t.Fatalf("pending: %+v", pending)
	}
	if p := pending[1]; p.Axiom.Statement != "this is slightly off" || !p.Verdict.Suggests() || p.Verdict.Correction != "this is still off" || p.Original != nil {
		t.Fatalf("unconfirmed correction should stay a suggestion: %+v", p)
	}

	var out strings.Builder
	accepted, err := Review(pending, strings.NewReader("c\n?\ne\nedited statement\nc\nn\n"), &out, b.Shape())
	if err != nil {
		t.Fatal(err)
	}
	if len(accepted) != 2 || accepted[0].Statement != "edited statement" || accepted[0].By != "user" || accepted[1].Statement != "this is still off" || accepted[1].By != "user" {
		t.Fatalf("accepted: %+v", accepted)
	}
	if o := out.String(); !strings.Contains(o, "not confirmed: checked") || !strings.Contains(o, "inaccurate: wrong release\n  suggested instead: this is still off") || !strings.Contains(o, "[y/n/c=suggestion/e/q]") {
		t.Fatalf("review output:\n%s", o)
	}
	if strings.Count(out.String(), "[y/n/e/q]") < 2 {
		t.Fatalf("c without a suggestion should re-prompt:\n%s", out.String())
	}
	accepted, _ = Review(pending, strings.NewReader("n\nn\nn\n"), &out, b.Shape())
	if len(accepted) != 0 {
		t.Fatal("declined should not be accepted")
	}

	path := t.TempDir() + "/axiomlog.pending"
	if err := WritePending(path, pending); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), `"correction":"this is still off"`) {
		t.Fatalf("pending file lacks the suggestion:\n%s", data)
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
	if len(props) != 4 || ag.count() != 7 {
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
	if got := strings.Join(order, "|"); got != "this is true|this is dubious|this is exactly true|this is slightly off|on the epoch" {
		t.Fatalf("order: %s", got)
	}
	if rt.count() != 8 {
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
