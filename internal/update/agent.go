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
	"log/slog"
	"strings"

	"github.com/microsoft/agent-framework-go/agent"
	"github.com/microsoft/agent-framework-go/agent/harness/toolautocall"
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
	Logger     *slog.Logger
}

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
func (f FrameworkAgent) Run(ctx context.Context, instructions, task string, tools []tool.Tool, out any) (string, error) {
	autocall := &toolautocall.Config{Logger: f.Logger}
	if f.MaxIterations > 0 {
		n := f.MaxIterations
		autocall.MaximumIterationsPerRequest = &n
	}
	a := openaiprovider.NewChatCompletionsAgent(f.client(), openaiprovider.AgentConfig{
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
	opts := []agent.Option{agent.Stream(false)}
	if out != nil {
		opts = append(opts, agent.WithStructuredOutput(out))
	}
	resp, err := a.RunText(ctx, task, opts...).Collect()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(resp.String()), nil
}
