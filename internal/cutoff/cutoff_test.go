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

package cutoff

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/microsoft/agent-framework-go/tool"

	"github.com/canonical/axiomaton/internal/update"
)

type fakeContainer struct{ closed bool }

func (f *fakeContainer) Push(context.Context, string, string) error { return nil }

func (f *fakeContainer) Exec(_ context.Context, cmd string, _ time.Duration) (update.ExecResult, error) {
	return update.ExecResult{Stdout: "ran: " + cmd}, nil
}
func (f *fakeContainer) Close(context.Context) error { f.closed = true; return nil }

type fakeRuntime struct{ started []*fakeContainer }

func (f *fakeRuntime) Start(context.Context) (update.Container, error) {
	c := &fakeContainer{}
	f.started = append(f.started, c)
	return c, nil
}

type fakeAgent struct {
	t       *testing.T
	finding Finding
	tasks   []string
}

func (f *fakeAgent) Run(ctx context.Context, instructions, task string, tools []tool.Tool, out any) (string, error) {
	f.tasks = append(f.tasks, task)
	if !strings.Contains(instructions, "knowledge cutoff") || len(tools) != 1 || tools[0].Name() != "shell" {
		f.t.Fatalf("unexpected agent setup: %d tools", len(tools))
	}
	args, _ := json.Marshal(map[string]any{"command": "curl -L https://example.com"})
	if _, err := tools[0].(tool.FuncTool).Call(ctx, string(args)); err != nil {
		f.t.Fatal(err)
	}
	*(out.(*Finding)) = f.finding
	return "done", nil
}

func TestStoreAndEstimate(t *testing.T) {
	s := Store{Path: filepath.Join(t.TempDir(), "sub", "cutoffs.json")}
	if _, ok, err := s.Get("x/y"); ok || err != nil {
		t.Fatalf("empty store: %v %v", ok, err)
	}
	r := Record{Model: "x/y", Cutoff: "2025-03-31", Method: MethodAgent, Source: "https://x", Found: time.Now()}
	if err := s.Put(r); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(Record{Model: "a/b", Cutoff: "2024-01-01", Method: MethodEstimate}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.Get("x/y")
	if err != nil || !ok || got.Cutoff != "2025-03-31" || got.Method != MethodAgent {
		t.Fatalf("get: %+v %v %v", got, ok, err)
	}
	if d, err := got.Date(); err != nil || d.Month() != time.March {
		t.Fatalf("date: %v %v", d, err)
	}

	day := func(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }
	// Two usable gaps: the 90th percentile of {241, 433} is the larger.
	obs := []Observation{
		{Model: "a/one", Released: day(2025, 8, 7), Cutoff: day(2024, 5, 31)},   // 433 days
		{Model: "a/two", Released: day(2025, 9, 29), Cutoff: day(2025, 1, 31)},  // 241 days
		{Model: "a/odd", Released: day(2024, 2, 26), Cutoff: day(2024, 11, 30)}, // cutoff after release: ignored
		{Model: "a/none", Released: day(2025, 1, 1)},                            // no cutoff: ignored
	}
	lead := PercentileLead(obs)
	if lead.Days != 433 || lead.Model != "a/one" || lead.Sample != 2 || !strings.Contains(lead.Describe(), "433 days, the 90th percentile") || !strings.Contains(lead.Describe(), "a/one") {
		t.Fatalf("lead: %+v", lead)
	}
	if def := PercentileLead(obs[2:]); def.Days != DefaultLeadDays || def.Model != "" || !strings.Contains(def.Describe(), "default") {
		t.Fatalf("default lead: %+v", def)
	}
	// With twenty gaps of 1..20 days the 90th percentile (nearest rank) is
	// 18, so the two largest outliers are ignored.
	var many []Observation
	for i := 1; i <= 20; i++ {
		many = append(many, Observation{Model: fmt.Sprint("m/", i), Released: day(2025, 1, 1).AddDate(0, 0, i), Cutoff: day(2025, 1, 1)})
	}
	if l := PercentileLead(many); l.Days != 18 || l.Model != "m/18" || l.Sample != 20 {
		t.Fatalf("percentile: %+v", l)
	}
	released := time.Date(2025, 8, 7, 15, 4, 5, 0, time.UTC)
	if e := Estimate(released, lead); !e.Equal(day(2024, 5, 31)) {
		t.Fatalf("estimate: %v", e)
	}
	for in, want := range map[string]string{"2025-01-31": "2025-01-31", "2025-02": "2025-02-28", "2024-02": "2024-02-29", " 2023-12 ": "2023-12-31"} {
		d, err := ParseDate(in)
		if err != nil || d.Format("2006-01-02") != want {
			t.Errorf("%q: %v %v", in, d, err)
		}
	}
	if _, err := ParseDate("January 2025"); err == nil {
		t.Error("prose date should fail")
	}
}

func TestResearcher(t *testing.T) {
	rt := &fakeRuntime{}
	ag := &fakeAgent{t: t, finding: Finding{Found: true, Cutoff: "2024-10", Source: "https://vendor.example/model-card"}}
	r := Researcher{Runtime: rt, Agent: ag, ExecTimeout: time.Second}
	m := Model{ID: "vendor/model-1", Name: "Vendor: Model 1", Released: time.Date(2025, 2, 1, 0, 0, 0, 0, time.UTC)}
	rec, ok, err := r.Find(context.Background(), m)
	if err != nil || !ok || rec.Cutoff != "2024-10-31" || rec.Method != MethodAgent || rec.Source != ag.finding.Source || rec.Model != m.ID {
		t.Fatalf("find: %+v %v %v", rec, ok, err)
	}
	if len(rt.started) != 1 || !rt.started[0].closed {
		t.Fatal("container not started and closed")
	}
	if !strings.Contains(ag.tasks[0], `"vendor/model-1"`) || !strings.Contains(ag.tasks[0], "released on 2025-02-01") {
		t.Fatalf("task: %s", ag.tasks[0])
	}

	// Not found, unusable date, and a date after release are all negative
	// results rather than errors.
	for _, f := range []Finding{{Found: false}, {Found: true, Cutoff: "autumn 2024"}, {Found: true, Cutoff: "2025-06-01"}} {
		ag.finding = f
		if _, ok, err := r.Find(context.Background(), m); ok || err != nil {
			t.Fatalf("%+v: ok=%v err=%v", f, ok, err)
		}
	}
}
