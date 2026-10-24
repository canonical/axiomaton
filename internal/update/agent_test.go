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
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/microsoft/agent-framework-go/tool"

	"github.com/canonical/axiomaton/internal/config"
)

// step is one scripted model reply: a tool call or a text answer, with an
// optional check on the request that provoked it.
type step struct {
	call  string // tool name; empty for a text reply
	args  string
	text  string
	check func(t *testing.T, req map[string]any)
}

func shellStep() step { return step{call: "shell", args: `{"command":"echo hi"}`} }

// chatScript fakes an OpenAI chat completions endpoint that replays steps.
type chatScript struct {
	steps    []step
	requests []map[string]any
}

func (s *chatScript) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		s.requests = append(s.requests, req)
		n := len(s.requests)
		if n > len(s.steps) {
			t.Errorf("unexpected request %d: %v", n, req["messages"])
			http.Error(w, "no more steps", 500)
			return
		}
		names := toolNames(req)
		if !names["shell"] || !names[SubmitToolName] {
			t.Errorf("request %d offers tools %v; want shell and %s", n, names, SubmitToolName)
		}
		st := s.steps[n-1]
		if st.check != nil {
			st.check(t, req)
		}
		msg := map[string]any{"role": "assistant"}
		finish := "stop"
		if st.call != "" {
			msg["content"] = nil
			msg["tool_calls"] = []any{map[string]any{"id": fmt.Sprintf("c%d", n), "type": "function",
				"function": map[string]any{"name": st.call, "arguments": st.args}}}
			finish = "tool_calls"
		} else {
			msg["content"] = st.text
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"id": fmt.Sprintf("r%d", n), "object": "chat.completion", "created": 1, "model": "m",
			"choices": []any{map[string]any{"index": 0, "message": msg, "finish_reason": finish}},
			"usage":   map[string]any{"prompt_tokens": 1, "completion_tokens": 2, "total_tokens": 3},
		})
	}
}

func toolNames(req map[string]any) map[string]bool {
	out := map[string]bool{}
	tools, _ := req["tools"].([]any)
	for _, tl := range tools {
		fn, _ := tl.(map[string]any)["function"].(map[string]any)
		out[fn["name"].(string)] = true
	}
	return out
}

// messagesText flattens the request's messages for substring checks.
func messagesText(req map[string]any) string {
	b, _ := json.Marshal(req["messages"])
	return string(b)
}

func lastMessage(req map[string]any) map[string]any {
	msgs, _ := req["messages"].([]any)
	if len(msgs) == 0 {
		return nil
	}
	return msgs[len(msgs)-1].(map[string]any)
}

func runScript(t *testing.T, s *chatScript, nudges int) (Verdict, string, error) {
	t.Helper()
	srv := httptest.NewServer(s.handler(t))
	defer srv.Close()
	shell, err := ShellTool(&fakeContainer{}, time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	f := FrameworkAgent{Endpoint: config.Endpoint{Provider: "test", Model: "m", BaseURL: srv.URL, APIKey: "k"}, MaxIterations: 6, MaxNudges: nudges}
	var v Verdict
	text, err := f.Run(context.Background(), "Check the statement.", "Is it true?", []tool.Tool{shell}, &v)
	return v, text, err
}

func TestFrameworkAgentSubmitTool(t *testing.T) {
	submit := `{"confirmed":true,"reason":"checked"}`

	// The agent investigates, submits, and signs off.
	s := &chatScript{steps: []step{
		shellStep(),
		{call: SubmitToolName, args: submit},
		{text: "Done.", check: func(t *testing.T, req map[string]any) {
			if !strings.Contains(messagesText(req), "Answer recorded") {
				t.Errorf("submit result not fed back: %s", messagesText(req))
			}
		}},
	}}
	v, text, err := runScript(t, s, 0)
	if err != nil || !v.Confirmed || v.Reason != "checked" || text != "Done." || len(s.requests) != 3 {
		t.Fatalf("direct: %+v %q %v requests=%d", v, text, err, len(s.requests))
	}
	if sys := s.requests[0]["messages"].([]any)[0].(map[string]any); sys["role"] != "system" || !strings.Contains(sys["content"].(string), "MUST deliver it by calling the submit_answer tool") {
		t.Fatalf("instructions: %v", sys)
	}

	// The agent answers in prose; it is woken up in the same conversation
	// and then submits.
	s = &chatScript{steps: []step{
		shellStep(),
		{text: "I verified the statement; it is correct."},
		{call: SubmitToolName, args: submit, check: func(t *testing.T, req map[string]any) {
			last := lastMessage(req)
			if last["role"] != "user" || !strings.Contains(last["content"].(string), "Call submit_answer now") {
				t.Errorf("expected a nudge as the last message, got %v", last)
			}
			if txt := messagesText(req); !strings.Contains(txt, "I verified the statement") || !strings.Contains(txt, "echo hi") {
				t.Errorf("history lost across the nudge: %s", txt)
			}
		}},
		{text: "Recorded."},
	}}
	v, text, err = runScript(t, s, 0)
	if err != nil || !v.Confirmed || text != "Recorded." || len(s.requests) != 4 {
		t.Fatalf("nudge: %+v %q %v requests=%d", v, text, err, len(s.requests))
	}

	// Invalid arguments are rejected with the reason, and the corrected
	// call is accepted.
	s = &chatScript{steps: []step{
		shellStep(),
		{call: SubmitToolName, args: `{"confirmed":"yes","reason":"typo"}`},
		{call: SubmitToolName, args: submit, check: func(t *testing.T, req map[string]any) {
			last := lastMessage(req)
			if last["role"] != "tool" || !strings.Contains(fmt.Sprint(last["content"]), "invalid submit_answer arguments") {
				t.Errorf("validation error not relayed: %v", last)
			}
		}},
		{text: "Fixed."},
	}}
	v, _, err = runScript(t, s, 0)
	if err != nil || !v.Confirmed || v.Reason != "checked" || len(s.requests) != 4 {
		t.Fatalf("invalid args: %+v %v requests=%d", v, err, len(s.requests))
	}

	// An agent that ignores the reminders fails with a diagnostic.
	s = &chatScript{steps: []step{
		shellStep(),
		{text: "Probably fine."},
		{text: "Still probably fine."},
	}}
	v, text, err = runScript(t, s, 1)
	if err == nil || v.Confirmed || len(s.requests) != 3 {
		t.Fatalf("ignored: %+v %v requests=%d", v, err, len(s.requests))
	}
	for _, want := range []string{"stopped without calling submit_answer despite 1 reminder(s)", "final assistant message had text 20 chars", `finish_reason "stop"`} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("diagnostic missing %q: %v", want, err)
		}
	}
	if text != "Still probably fine." {
		t.Fatalf("text: %q", text)
	}
}

func TestSubmitToolRejectsNonStruct(t *testing.T) {
	var n int
	if _, err := newSubmitTool(&n); err == nil {
		t.Fatal("expected an error for a non-struct target")
	}
	if _, err := newSubmitTool(Verdict{}); err == nil {
		t.Fatal("expected an error for a non-pointer target")
	}
}
