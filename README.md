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

A project can have a `axiomaton.cnf` file, which describes the subjects that are
relevent to that project, `ax` will search the current directory, up to the root
for every `axiomaton.cnf` file, layering them on top of each other. This allows
a project or system to provide responses on subjects. Next to each `axiomaton.cnf`
can be a `axiomlog` file that can also answer questions about subjects. Default
subjects are only consumed up to the nearest directory containing a `.git`
directory, which can be disabled with a `--global`/`-g` flag.

When `ax since 2025-08-01` is run, the default subjects are returned.

`axiomaton.cnf` files are written in YAML in this format:
```yaml
subjects: [[golang, 1.26.1], ...]
```

Axiomaton search is a combination of deterministic vector search through interleaved
log files, and a filtering mechanism based on querying embedding/reranking models
to find similar, possibly contradictory axioms for a given subject. The resolution
of these similar axioms is via a decision model (like Jev), to determine if they
do contracdict each-other or are too similar. If they are contradictory, the newer,
by vector clock, is chosen as the truth. The `ax` tool will cache embedding vectors,
decisions etc. in `$XDG_CACHE_HOME/axiomaton/`.

Configuration of `ax` is stored in `$XDG_CONFIG_HOME/aixomaton/ax.cnf`. It defines
the model configuration for each model used, e.g. provider (e.g. OpenRouter),
the model and authentication details. It requires an embedding/reranking model,
decision model and two general purpose LLM models, one for summarisation (a low
cost model good at summarisation) and a agentic capable model (a model capable of
running tools, etc). It also defines a summary budget for the `-s` flag.

The `ax` tool also contains these other commands:
- `ax add 2026-08-19 golang 1.27.0 "a method declaration may declare its own type parameters."`
- `ax update golang`

The update command only works for built-in subjects, with golang as the only current
built-in subject. All subject updates are performed as such, firstly, a containerised
agent is run (using github.com/microsoft/agent-framework-go, running a podman container,
with tool calls running inside the container via podman exec), to update the axiomlog
based on go's release notes. Then after the agent has concluded, for each individual
axiom added, a new agent, with a new container, is asked to confirm, it. Only axioms
that are confirmed are added automatically, axioms that are not confirmed require
user input.
