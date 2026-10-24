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
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"sync"

	"github.com/microsoft/agent-framework-go/agent"
	"github.com/microsoft/agent-framework-go/agent/format/jsonformat"
	"github.com/microsoft/agent-framework-go/agent/harness/toolautocall"
	"github.com/microsoft/agent-framework-go/message"
	"github.com/microsoft/agent-framework-go/provider/openaiprovider"
	"github.com/microsoft/agent-framework-go/tool"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"

	"github.com/canonical/axiomaton/internal/config"
)

// Agent runs one agentic task to completion.
type Agent interface {
	// Run drives the model with instructions and the task, letting it call
	// tools until it answers. When out is non-nil the answer is decoded into
	// it as structured output. The final text is returned either way.
	Run(ctx context.Context, instructions, task string, tools []tool.Tool, out any) (string, error)
}

// FrameworkAgent implements Agent with agent-framework-go over an OpenAI
// compatible chat completions endpoint such as OpenRouter.
type FrameworkAgent struct {
	Endpoint      config.Endpoint
	MaxIterations int
	// MaxRetries is passed to the OpenAI client, which backs off on rate
	// limits and server errors by itself.
	MaxRetries int
	// MaxNudges is how many times an agent that stops without calling the
	// submit tool is reminded to do so before the run fails. Zero means the
	// default of 2.
	MaxNudges int
	Logger    *slog.Logger
}

// SubmitToolName is the tool an agent must call to deliver a structured
// answer. Prompts refer to it by this name.
const SubmitToolName = "submit_answer"

const defaultMaxNudges = 2

func (f FrameworkAgent) client() openai.Client {
	base := f.Endpoint.BaseURL
	if !strings.HasSuffix(base, "/v1") {
		base += "/v1"
	}
	retries := f.MaxRetries
	if retries <= 0 {
		retries = config.DefaultUser().Retry.MaxAttempts - 1
	}
	return openai.NewClient(
		option.WithBaseURL(base),
		option.WithAPIKey(f.Endpoint.APIKey),
		option.WithMaxRetries(retries),
		option.WithHeader("HTTP-Referer", "https://github.com/canonical/axiomaton"),
		option.WithHeader("X-Title", "axiomaton"),
	)
}

// Run implements Agent.
//
// A structured answer is delivered through a tool rather than the
// framework's structured output: agent-framework-go v0.2.0 decodes
// structured output after every provider call inside the tool loop, so any
// tool-using run fails with "decoding structured output: EOF", and models
// also tend to answer in prose when the schema is only a response format.
// When out is non-nil a submit tool whose arguments match out's type is added
// to the tools and the instructions. If the agent stops without calling it,
// it is woken up in the same conversation and asked to call it, up to
// MaxNudges times.
func (f FrameworkAgent) Run(ctx context.Context, instructions, task string, tools []tool.Tool, out any) (string, error) {
	if out == nil {
		resp, err := f.run(ctx, f.newAgent(instructions, tools), nil, task)
		return strings.TrimSpace(resp.String()), err
	}
	submit, err := newSubmitTool(out)
	if err != nil {
		return "", err
	}
	tools = append(slices.Clone(tools), submit)
	instructions = strings.TrimSpace(instructions) + "\n\n" + submitInstructions()
	a := f.newAgent(instructions, tools)
	session := &agent.Session{}
	nudges := f.MaxNudges
	if nudges <= 0 {
		nudges = defaultMaxNudges
	}
	var text string
	msg := task
	for attempt := 0; ; attempt++ {
		resp, err := f.run(ctx, a, session, msg)
		if t := strings.TrimSpace(resp.String()); t != "" {
			text = t
		}
		if err != nil {
			return text, err
		}
		if submit.Called() {
			return text, nil
		}
		if attempt >= nudges {
			return text, fmt.Errorf("the agent stopped without calling %s despite %d reminder(s); %s", SubmitToolName, nudges, diagnose(resp))
		}
		f.logf("agent stopped without calling %s; reminding it (%d of %d)", SubmitToolName, attempt+1, nudges)
		msg = fmt.Sprintf("You stopped without calling the %s tool, so your answer has not been recorded. "+
			"Call %s now with your answer based on what you have already found. Do not repeat the investigation.", SubmitToolName, SubmitToolName)
	}
}

func submitInstructions() string {
	return fmt.Sprintf("When you have your answer you MUST deliver it by calling the %s tool with the fields it "+
		"describes. Text replies are not recorded; the task is complete only once %s has accepted your answer.", SubmitToolName, SubmitToolName)
}

// submitTool is a FuncTool whose arguments are the structured answer type.
// Calling it decodes and validates the arguments into the target.
type submitTool struct {
	format *jsonformat.Format
	schema any
	target any // pointer to the answer struct

	mu    sync.Mutex
	calls int
}

func newSubmitTool(target any) (*submitTool, error) {
	rt := reflect.TypeOf(target)
	if rt == nil || rt.Kind() != reflect.Pointer || rt.Elem().Kind() != reflect.Struct {
		return nil, fmt.Errorf("structured output target must be a pointer to a struct, got %T", target)
	}
	rf, err := jsonformat.ForType(rt)
	if err != nil {
		return nil, fmt.Errorf("structured output schema: %w", err)
	}
	jf, err := jsonformat.FromResponseFormat(rf)
	if err != nil {
		return nil, err
	}
	return &submitTool{format: jf, schema: rf.Schema, target: target}, nil
}

func (s *submitTool) Name() string { return SubmitToolName }
func (s *submitTool) Description() string {
	return "Record your final answer. Call this exactly once, when you are done, with every field filled in."
}
func (s *submitTool) Schema() any       { return s.schema }
func (s *submitTool) ReturnSchema() any { return nil }

// Call decodes args into a fresh value, validated against the schema, and
// copies it into the target so a malformed call leaves earlier answers
// intact. Validation errors go back to the model so it can correct the call.
func (s *submitTool) Call(_ context.Context, args string) (any, error) {
	fresh := reflect.New(reflect.TypeOf(s.target).Elem())
	if err := s.format.Unmarshal([]byte(strings.TrimSpace(args)), fresh.Interface()); err != nil {
		return nil, fmt.Errorf("invalid %s arguments: %w; call it again with corrected fields", SubmitToolName, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	reflect.ValueOf(s.target).Elem().Set(fresh.Elem())
	s.calls++
	return map[string]any{"accepted": true, "message": "Answer recorded; you may stop now."}, nil
}

// Called reports whether a valid answer was recorded.
func (s *submitTool) Called() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls > 0
}

func (f FrameworkAgent) logf(format string, args ...any) {
	if f.Logger != nil {
		f.Logger.Info(fmt.Sprintf(format, args...))
	}
}

func (f FrameworkAgent) newAgent(instructions string, tools []tool.Tool) *agent.Agent {
	autocall := &toolautocall.Config{Logger: f.Logger, IncludeDetailedErrors: true}
	if f.MaxIterations > 0 {
		n := f.MaxIterations
		autocall.MaximumIterationsPerRequest = &n
	}
	return openaiprovider.NewChatCompletionsAgent(f.client(), openaiprovider.AgentConfig{
		Config: agent.Config{
			Name:   "axiomaton",
			Tools:  tools,
			Logger: f.Logger,
		},
		ProviderName: f.Endpoint.Provider,
		Instructions: instructions,
		Model:        f.Endpoint.Model,
		ToolAutoCall: autocall,
	})
}

// run drives one agent turn, collecting every update so the response is
// available even when the run ends in an error. A non-nil session carries
// the conversation across turns.
func (f FrameworkAgent) run(ctx context.Context, a *agent.Agent, session *agent.Session, msg string) (*agent.Response, error) {
	opts := []agent.Option{agent.Stream(false)}
	if session != nil {
		opts = append(opts, agent.WithSession(session))
	}
	resp := &agent.Response{}
	var runErr error
	for update, err := range a.RunText(ctx, msg, opts...) {
		if err != nil {
			runErr = err
			break
		}
		if update == nil {
			continue
		}
		if f.Logger != nil {
			f.Logger.Debug("agent update", "role", update.Role, "finish", update.FinishReason, "contents", describeContents(update.Contents))
		}
		resp.Update(update)
	}
	resp.Coalesce()
	return resp, runErr
}

func lastAssistant(resp *agent.Response) *message.Message {
	for i := len(resp.Messages) - 1; i >= 0; i-- {
		if resp.Messages[i].Role == message.RoleAssistant {
			return resp.Messages[i]
		}
	}
	return nil
}

// diagnose summarises how a turn ended, for error messages.
func diagnose(resp *agent.Response) string {
	if resp == nil || len(resp.Messages) == 0 {
		return "the model returned no messages"
	}
	rounds := 0
	for _, m := range resp.Messages {
		if m.Role != message.RoleAssistant {
			continue
		}
		for _, c := range m.Contents {
			if _, ok := c.(*message.FunctionCallContent); ok {
				rounds++
				break
			}
		}
	}
	last := lastAssistant(resp)
	if last == nil {
		return fmt.Sprintf("no assistant message among %d messages (finish_reason %q)", len(resp.Messages), resp.FinishReason)
	}
	return fmt.Sprintf("after %d tool round(s) the final assistant message had %s (finish_reason %q)", rounds, describeContents(last.Contents), resp.FinishReason)
}

// describeContents names each content item compactly, e.g.
// "text 0 chars, reasoning 812 chars, call shell".
func describeContents(contents message.Contents) string {
	if len(contents) == 0 {
		return "no content"
	}
	var parts []string
	for _, c := range contents {
		switch v := c.(type) {
		case *message.TextContent:
			parts = append(parts, fmt.Sprintf("text %d chars", len(v.Text)))
		case *message.TextReasoningContent:
			parts = append(parts, fmt.Sprintf("reasoning %d chars", len(v.Text)))
		case *message.FunctionCallContent:
			parts = append(parts, "call "+v.Name)
		case *message.FunctionResultContent:
			parts = append(parts, "tool result")
		case *message.ErrorContent:
			parts = append(parts, fmt.Sprintf("error %s: %s", v.ErrorCode, oneLine(v.Message, 200)))
		case *message.UsageContent:
			parts = append(parts, fmt.Sprintf("usage out=%d", v.Details.OutputTokenCount))
		default:
			parts = append(parts, fmt.Sprintf("%T", c))
		}
	}
	return strings.Join(parts, ", ")
}
