# Axiomaton

Axiomaton is a CLI tool that, when given a cutoff date, answers with what
foundational knowledge (axioms) has changed. An `axiomlog` is an append-only log
that contains statements that are considered true at a given clock for a given
subject.

A clock is a vector of multiple values that are subject dependent, it must contain
a UTC time-stamp, but also could contain a semantic version or another ordered
value (either independantly ordered in the case of a number or string, or externally
ordered such as a git commit/repo pair).

To query the `axiomlog`, you can run the tool with `ax since 2025-08-01 golang 1.26.1`,
which evaluates the log, for the golang (sub-clock 1.26.1) subject
since 2025-08-01 (primary clock), what has changed. It produces a Markdown output
that could be injested by a LLM in it's context. A `--summarise`/`-s` flag can be
provided to run the determistic generated output through a model to summarise it.

`ax for openai/gpt-5-mini golang 1.26.1` does the same with the cutoff taken
from the knowledge cutoff OpenRouter publishes for that model (its
`/api/v1/models` listing, cached for a day under the cache directory, refreshed
with `-refresh`). Model names match the OpenRouter id, canonical slug or
display name; rolling aliases are followed and a `:variant` without its own
cutoff falls back to the base model. When OpenRouter publishes no cutoff, `ax`
runs a containerised agent (the same machinery as `ax update`, so it needs
podman and an agent model) to find one in the vendor's documentation or model
card, and if that fails it estimates the cutoff from the model's OpenRouter
release date by subtracting the 90th percentile of the release-to-cutoff gaps
seen among the models that publish both dates, with a warning naming the
figure. A model with no release date at all
falls back to the global epoch, 2023-08-01, which predates every
built-in axiom. Each answer is stored in
`$XDG_DATA_HOME/axiomaton/cutoffs.json`, which can be edited by hand, and is
reused until `-refresh` is given; `--offline` skips the agent.

The result contains axioms whose primary clock is after the cutoff and whose
sub-clock is at or before the subject clock given (so `golang 1.26.1` excludes
Go 1.27 facts). Axioms without a sub-clock always apply.

A project can have a `axiomaton.cnf` file, which describes the subjects that are
relevent to that project, `ax` will search the current directory, up to the root
for every `axiomaton.cnf` file, layering them on top of each other. This allows
a project or system to provide responses on subjects. Next to each `axiomaton.cnf`
can be a `axiomlog` file that can also answer questions about subjects. Default
subjects are only consumed up to the nearest directory containing a `.git`
directory, which can be disabled with a `--global`/`-g` flag. Logs above the
repository are still consulted. A user wide log at
`$XDG_DATA_HOME/axiomaton/axiomlog` is always consulted and is the fallback
write target outside of any repository.

When `ax since 2025-08-01` is run, the default subjects are returned. When no
default subjects are declared, nothing is printed and a warning goes to stderr.
A subject with nothing to report (no axiom changed or added since the cutoff
within its clock) is left out of the output, and `-v` says so on stderr; when
that is true of every subject the output is a single "No changes for ..."
line under the heading. The output carries each axiom's clock value (e.g.
`[1.26.0]`) but no dates and no sources: it is written for a model's
context, where those only add noise. The log keeps both.

Run `ax init` in a project to create its `axiomaton.cnf` (and an empty
`axiomlog`). Built-in subjects detect themselves: `golang` is listed when a
`go.mod` is found in the directory or its parents up to the repository root,
and every direct requirement in that `go.mod` adds a
`golang-mod-<module path>` subject (e.g. `golang-mod-golang.org/x/net`);
indirect requirements are skipped. Detected subjects are written without a
clock, and a built-in subject named without a clock, in the file or on the
command line, takes its clock from the project at query time (`golang` uses
the `go` directive in `go.mod`; `golang-mod-<module>` uses that module's
`require` line), so the file never goes stale. Give an explicit clock to pin a
version instead. Further built-in subjects can be named as arguments, e.g.
`ax init golang golang-mod-golang.org/x/text`, and dependencies you do not
want reported on can simply be deleted from the file.

`golang-mod-<module>` subjects are a built-in *family*: any valid module path
after the prefix is a subject with a `version` clock of kind `semver`, with no
declaration needed. They ship with no axioms; `ax update
golang-mod-golang.org/x/net` builds the log for the project (see below), and
until then `ax since` warns that nothing is known for the subject.

`axiomaton.cnf` files are written in YAML. Each subject is either the compact
`[name, clock values...]` form or a mapping. Subjects that are not built in need
a clock shape, declared inline or under `shapes:`, before clocked axioms can be
added for them:

```yaml
subjects:
  - golang                 # clock read from go.mod; pin with [golang, 1.26.1]
  - name: myservice
    clock: [2.3.0]
    shape:
      - {name: version, kind: semver}
  - [juju, 3f9c2a1]
shapes:
  juju:
    - {name: commit, kind: git, repo: ../juju}
```

Clock kinds are `semver`, `goversion`, `number`, `string` and `git` (ordered by
`git merge-base --is-ancestor` in `repo`, relative to the config file).

Axiomaton search is a combination of deterministic vector search through interleaved
log files, and a filtering mechanism based on querying embedding/reranking models
to find similar, possibly contradictory axioms for a given subject. The resolution
of these similar axioms is via a decision model (like Jev), to determine if they
do contracdict each-other or are too similar. If they are contradictory, the newer,
by vector clock, is chosen as the truth. The `ax` tool will cache embedding vectors,
decisions etc. in `$XDG_CACHE_HOME/axiomaton/`. Without an embedding and decision
model configured (or with `--offline`), the deterministic output is still produced
but superseded axioms are not detected.

Configuration of `ax` is stored in `$XDG_CONFIG_HOME/axiomaton/ax.cnf`. Run
`ax setup` to create it: on a terminal it asks for the provider URL, API key
and a model for each role, writes the file and then sends a tiny request to
each configured model (and looks for podman) to confirm everything works.
Every answer has a flag (`-base-url`, `-key` or `-key-env`, `-embedding`,
`-rerank`, `-decision`, `-summary`, `-agent`, `-image`), `-y` accepts the
defaults without prompting, `-force` replaces an existing file and `-no-check`
skips the connectivity test. A key given with `-key` is stored in the file,
which is then made readable by the owner only; otherwise the key is read from
the environment variable at run time. The file defines
the model configuration for each model used, e.g. provider (e.g. OpenRouter),
the model and authentication details. It requires an embedding/reranking model,
decision model and two general purpose LLM models, one for summarisation (a low
cost model good at summarisation) and a agentic capable model (a model capable of
running tools, etc). It also defines a summary budget for the `-s` flag.

```yaml
providers:
  openrouter:                       # built in; shown here with its defaults
    base_url: https://openrouter.ai/api
    api_key_env: OPENROUTER_API_KEY # or api_key: sk-or-...
models:
  embedding: {model: openai/text-embedding-3-small}
  rerank:    {model: cohere/rerank-v3.5}        # optional
  decision:  {model: typesafe/jev-1.13}         # OpenRouter Decisions API
  summary:   {model: openai/gpt-5-mini}
  agent:     {model: anthropic/claude-sonnet-4.5}
summary_budget: 1500                # tokens
search:
  candidates: 8                     # nearest neighbours per axiom
  min_similarity: 0.35              # cosine floor
  rerank_top_n: 5
  min_relevance: 0.1
  decision_threshold: 0.5           # minimum probability to accept a verdict
retry:
  max_attempts: 8                   # tries per model request
  initial_backoff: 1                # seconds; doubles each attempt, with jitter
  max_backoff: 60                   # seconds
update:
  image: public.ecr.aws/ubuntu/ubuntu:latest
  max_iterations: 80                # tool calls per agent run
  exec_timeout: 300                 # seconds per command in the container
  confirmations: 4                  # confirmation agents at once (default: half the CPUs)
```

Requests that fail with 429, 5xx or a dropped connection are retried with
exponential backoff and full jitter, honouring any `Retry-After` the provider
sends; `-v` logs each retry. Any model may set its own `provider`, `base_url`,
`api_key` or `api_key_env`.
Embeddings, rerank and chat use the OpenAI compatible `/v1` endpoints, so an
OpenAI compatible server works for those roles; the decision role uses the
OpenRouter Decisions API.

The `ax` tool also contains these other commands:
- `ax for anthropic/claude-sonnet-4.5`
- `ax add 2026-08-19 golang 1.27.0 "a method declaration may declare its own type parameters."`
- `ax update golang`
- `ax update golang-mod-golang.org/x/net`
- `ax subjects`
- `ax init`
- `ax setup`

`add` and `update` append to the nearest writeable `axiomlog` inside the
repository (a directory with an `axiomaton.cnf` or `axiomlog`), then the
repository root, then the user wide log. `-l <axiomlog>` overrides the target.

The update command only works for built-in subjects: `golang`, and
`golang-mod-<module path>` for any Go module. All subject updates are performed as such, firstly, a containerised
agent is run (using github.com/microsoft/agent-framework-go, running a podman container,
with tool calls running inside the container via podman exec), to update the axiomlog
based on go's release notes. For a module subject the agent takes the release
list and dates from the module proxy and works out what changed for callers
from the module's release notes, an API comparison between tags (apidiff) and
the commit log, starting from the newest release the log already covers, or,
for an empty log, from releases published since the global epoch
(2023-08-01). Then after the agent has concluded, for each individual
axiom added, a new agent, with a new container, is asked to confirm, it; these
confirmations run several at a time (`update.confirmations` in `ax.cnf`, half
the CPUs by default, or `-j n` on the command line), each in its own
container, and the results keep the proposal order. Only axioms
that are confirmed are added automatically, axioms that are not confirmed require
user input. A confirmation agent that rejects a statement says which kind of
rejection it is: an *inaccurate* statement (wrong release, identifier or scope,
overstated or incomplete) comes back with a corrected statement the agent did
verify, while an *unfounded* one comes back with no suggestion. A correction
is confirmed once more by a fresh agent and, if that succeeds, is appended in
place of the proposal (the original is kept in the pending record). On a
terminal the user is asked about each remaining proposal, with `c` accepting
the suggested correction where there is one; otherwise they are written to
`<axiomlog>.pending` (suggestions under `verdict.correction`) for later
review, or accepted with `-y`, which takes the suggestion over the original
when the agent offered one.

The built-in golang log is embedded from `internal/subjects/golang/axiomlog`;
maintainers refresh it with `ax update golang -l internal/subjects/golang/axiomlog`.

## Log format

An `axiomlog` is JSON lines; blank lines and `#` comments are ignored. The `id`
is a content hash and is recomputed on read.

```json
{"subject":"golang","time":"2026-08-19T00:00:00Z","clock":{"version":"1.27.0"},"statement":"A method declaration may declare its own type parameters.","source":"https://go.dev/doc/go1.27","by":"agent"}
```

## Building

```sh
go build ./cmd/ax
go test ./...
AX_PODMAN_TEST=1 go test ./internal/update/ -run TestPodmanIntegration   # needs podman and network
```
