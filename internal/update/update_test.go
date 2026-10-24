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
	"strings"
	"testing"
	"time"

	"github.com/microsoft/agent-framework-go/tool"

	"github.com/canonical/axiomaton/internal/axiom"
	"github.com/canonical/axiomaton/internal/subjects"
)

type fakeContainer struct{ closed bool }

func (f *fakeContainer) Exec(_ context.Context, cmd string, _ time.Duration) (ExecResult, error) {
	return ExecResult{Stdout: "ran: " + cmd}, nil
}
func (f *fakeContainer) Close(context.Context) error { f.closed = true; return nil }

type fakeRuntime struct{ started []*fakeContainer }

func (f *fakeRuntime) Start(context.Context) (Container, error) {
	c := &fakeContainer{}
	f.started = append(f.started, c)
	return c, nil
}

// fakeAgent drives tools directly: the update run proposes two axioms, the
// confirm run confirms statements containing "true".
type fakeAgent struct {
	t    *testing.T
	runs int
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
	f.runs++
	if out == nil {
		// Update agent.
		if !strings.Contains(instructions, "append-only log") || !strings.Contains(task, "Your task") {
			return "", nil
		}
		f.t.Logf("shell: %s", call(f.t, tools, "shell", ShellInput{Command: "apt-get update"}))
		call(f.t, tools, "propose_axiom", ProposeInput{Date: "2027-02-09", Clock: []string{"1.28.0"}, Statement: "this is true", Source: "https://go.dev/doc/go1.28"})
		call(f.t, tools, "propose_axiom", ProposeInput{Date: "2027-02-09", Clock: []string{"1.28.0"}, Statement: "this is dubious", Source: "https://go.dev/doc/go1.28"})
		// Duplicate and invalid proposals must be rejected, not crash.
		if r := call(f.t, tools, "propose_axiom", ProposeInput{Date: "2027-02-09", Clock: []string{"1.28.0"}, Statement: "this is true"}); !strings.Contains(r, "already recorded") {
			f.t.Fatalf("duplicate not rejected: %s", r)
		}
		if r := call(f.t, tools, "propose_axiom", ProposeInput{Date: "bad", Clock: []string{"1.28.0"}, Statement: "x"}); !strings.Contains(r, "invalid date") {
			f.t.Fatalf("bad date not rejected: %s", r)
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
	v.Confirmed = strings.Contains(task, "this is true")
	v.Reason = "checked"
	return "", nil
}

func newFakeAgent(t *testing.T) *fakeAgent { return &fakeAgent{t: t} }

func TestUpdaterRun(t *testing.T) {
	rt := &fakeRuntime{}
	ag := newFakeAgent(t)
	b, _ := subjects.Lookup("golang")
	u := &Updater{Subject: b, Runtime: rt, Agent: ag, ExecTimeout: time.Second, Log: t.Logf}
	props, summary, err := u.Run(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if summary != "covered 1.28.0" || len(props) != 2 || ag.runs != 3 {
		t.Fatalf("props=%d summary=%q runs=%d", len(props), summary, ag.runs)
	}
	if len(rt.started) != 3 {
		t.Fatalf("expected a fresh container per agent, got %d", len(rt.started))
	}
	for _, c := range rt.started {
		if !c.closed {
			t.Fatal("container not closed")
		}
	}
	confirmed, pending := Split(props)
	if len(confirmed) != 1 || confirmed[0].Statement != "this is true" || confirmed[0].By != "agent" {
		t.Fatalf("confirmed: %+v", confirmed)
	}
	if len(pending) != 1 || pending[0].Axiom.Statement != "this is dubious" {
		t.Fatalf("pending: %+v", pending)
	}

	var out strings.Builder
	accepted, err := Review(pending, strings.NewReader("?\ne\nedited statement\n"), &out, b.Shape())
	if err != nil {
		t.Fatal(err)
	}
	if len(accepted) != 1 || accepted[0].Statement != "edited statement" || accepted[0].By != "user" {
		t.Fatalf("accepted: %+v", accepted)
	}
	if !strings.Contains(out.String(), "not confirmed: checked") {
		t.Fatalf("review output:\n%s", out.String())
	}
	accepted, _ = Review(pending, strings.NewReader("n\n"), &out, b.Shape())
	if len(accepted) != 0 {
		t.Fatal("declined should not be accepted")
	}

	path := t.TempDir() + "/axiomlog.pending"
	if err := WritePending(path, pending); err != nil {
		t.Fatal(err)
	}
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
