# Axiomaton

Axiomaton tells a language model what has changed since its knowledge
cutoff. It keeps an append-only log of dated facts (axioms) about subjects
such as a programming language, a tool or a library, and renders the ones
that became true after a given date as Markdown ready to drop into a
model's context.

Facts carry a clock as well as a date, so a project on Go 1.26 is not told
about Go 1.27. Logs for the built-in subjects (`go`, `python`, `rust`,
`zig`, `snapcraft`, `charmcraft`, `rockcraft` and the `golang.org/x`
modules) ship with the tool. Project specific logs are extended by
sandboxed agents that read release notes and verify what they record.

## Install

Requires Go 1.27 or later.

```sh
go install github.com/canonical/axiomaton/cmd/ax@latest
```

Or build from a checkout:

```sh
go build ./cmd/ax
```

Querying the shipped logs needs nothing else. Summaries, model cutoffs and
log updates use models reached through OpenRouter (or any OpenAI compatible
endpoint), and `ax update` needs podman or LXD on the host.

## Setup

`ax setup` writes the user configuration, `$XDG_CONFIG_HOME/axiomaton/ax.cnf`,
asking for the provider, API key and a model for each role, then sends a
small request to each model to confirm the configuration works. Every
answer can be given as a flag, and `-y` takes the defaults.

```sh
export OPENROUTER_API_KEY=sk-or-...
ax setup
```

Running `ax setup` again updates the file in place. A single value flag,
such as `ax setup -agent openai/gpt-6-luna-pro`, changes only that field.
The full set of options is described in [DESIGN.md](DESIGN.md#user-configuration).

## Usage

### In a project

Inside a git repository `ax` works out the project's subjects from its own
files: `go.mod` gives `go` and one `go:<module>` subject per direct
dependency, `Cargo.toml` gives `rust`, and so on. Nothing needs to be
written to use them.

```sh
ax since 2025-08-01                 # changes since a date
ax for openai/gpt-5-mini            # changes since a model's knowledge cutoff
```

[EXAMPLE.md](EXAMPLE.md) shows the report this produces: one section per
subject, listing the facts that changed or were added after the cutoff,
each with the release it holds from.

`ax init` records those subjects in an `axiomaton.cnf` in the project so
they can be pinned, extended with subjects of your own, or committed. Run it
again after adding dependencies; existing entries are kept.

```sh
ax init
ax init go:golang.org/x/text        # also list a subject that is not detected
```

### Keeping an agent context file current

`ax sync` writes the report into the context file of the agent working in
the project, between a `# Axiom changes` heading and an
`**end of axiom changes**` line. The first run asks for the model or date
and the file, and remembers both, so later runs take no arguments.

```sh
ax sync openai/gpt-6-luna-pro -agent claude   # first time
ax sync                                       # from then on
ax sync 2025-06-01                            # switch this project to a date
```

`-agent` knows `claude` (CLAUDE.local.md), `codex` (AGENTS.override.md),
`gemini` (GEMINI.md) and `copilot` (.github/copilot-instructions.md);
`-file <path>` names any other file. The file is personal to a checkout, so
`ax sync` warns when git would commit it.

### Any subject, anywhere

Name a subject and, optionally, its clock to query it directly:

```sh
ax since 2025-08-01 go 1.26.1
ax for anthropic/claude-sonnet-4.5 python 3.13
ax since 2025-08-01 -s                 # summarise the report with a model
ax subjects                            # list known subjects and their logs
```

Add `--offline` to skip every model call; the report is then produced from
the logs alone.

### Recording facts

```sh
ax add 2026-08-19 go 1.27.0 "A method declaration may declare its own type parameters."
ax update go                            # agents extend the go log from its release notes
ax update go:golang.org/x/net           # likewise for a Go module
ax update juju                          # a project subject with an update brief
```

`ax update` runs an agent in a sandbox with no network except a caching
proxy, then confirms each proposed axiom with a fresh agent in a fresh
sandbox. Confirmed axioms are appended; the rest are offered for review.
Both commands write to the `axiomlog` beside the nearest `axiomaton.cnf`,
or to the user wide log outside any project.

## Configuration

`ax.cnf` names the models for each role. A minimal file:

```
providers.openrouter.api-key-env: "OPENROUTER_API_KEY"
models:
    embedding: {model: "perplexity/pplx-embed-v1-4b"}
    decision: {model: "typesafe/jev-1.13"}
    summary: {model: "openai/gpt-oss-120b"}
    agent: {model: "openai/gpt-6-luna-pro"}
```

The first time `ax` runs podman, launches an LXD instance or creates the
isolated LXD network it explains what it is about to do and asks for
permission; answering `always` records it under `consent` in `ax.cnf`.

`axiomaton.cnf` lists a project's subjects. Built-in subjects need only a
name; others declare a clock shape:

```
subjects:
    - "go"  # clock read from go.mod; pin with ["go", "1.26.1"]
    - {name: "myservice", clock: ["2.3.0"], shape: [{name: "version", kind: "semver"}]}
```

Both files use the [cnf](https://github.com/canonical/cnf) format: strings
are quoted, and `a.b: 1` is shorthand for a nested map.

## Further reading

[DESIGN.md](DESIGN.md) covers clocks and the global epoch, how
configuration files and logs are layered, subject detection, the update
agents and their confirmation step, custom subject briefs, the cache
proxy and the log format.

## Development

```sh
go build ./cmd/ax
go test ./...
AX_PODMAN_TEST=1 go test ./internal/update/ -run TestPodman      # needs podman and network
AX_LXD_TEST=1 go test ./internal/update/ -run TestLXDIntegration  # needs LXD; AX_LXD_VM_TEST=1 for a VM
```

## License

GNU General Public License, version 3. See [COPYING](COPYING).
