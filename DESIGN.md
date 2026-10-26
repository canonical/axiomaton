# Axiomaton design

This document describes how `ax` works: the model of axioms and clocks,
where configuration and logs live, how subjects are detected, how the
update agents run, and the cache proxy they run behind. The
[README](README.md) covers installation and everyday use.

## Axioms and clocks

An `axiomlog` is an append-only log of statements that are considered true
at a given clock for a given subject. A clock is a vector of values that
are subject dependent. It always contains a UTC timestamp (the primary
clock) and may also contain a semantic version or another ordered value,
either independently ordered, as a number or string is, or externally
ordered, as a git commit is by its repository.

`ax since 2025-08-01 go 1.26.1` evaluates the log for the `go` subject at
sub-clock 1.26.1 and reports what has changed since the primary clock
2025-08-01. The result contains the axioms whose primary clock is after the
cutoff and whose sub-clock is at or before the subject clock given, so
`go 1.26.1` excludes Go 1.27 facts. Axioms without a sub-clock always
apply. The output is Markdown written for a model's context: it carries
each axiom's clock value (e.g. `[1.26.0]`) but no dates and no sources,
which only add noise there. The log keeps both. The `--summarise`/`-s`
flag runs the deterministic output through the summary model.

A subject with nothing to report (no axiom changed or added since the
cutoff within its clock) is left out of the output, and `-v` says so on
stderr; when that is true of every subject the output is a single "No
changes for ..." line under the heading.

### The global epoch

The global epoch, 2023-08-01, is a hard floor: anything that became true on
or before it is assumed to be prior knowledge of every model. A cutoff
earlier than the epoch, given to `since` or resolved by `for`, is raised to
it with a warning. `ax add` refuses a date that is not after it, and the
update agents are told not to propose axioms on or before it, with the
propose tool rejecting any that slip through.

### Model cutoffs

`ax for openai/gpt-5-mini go 1.26.1` takes the cutoff from the knowledge
cutoff OpenRouter publishes for the model in its `/api/v1/models` listing,
cached for a day under the cache directory and refreshed with `-refresh`.
Model names match the OpenRouter id, canonical slug or display name;
rolling aliases are followed and a `:variant` without its own cutoff falls
back to the base model.

When OpenRouter publishes no cutoff, `ax` runs a containerised agent (the
same machinery as `ax update`, so it needs a sandbox backend and an agent
model) to find one in the vendor's documentation or model card. If that
fails it estimates the cutoff from the model's OpenRouter release date by
subtracting the 90th percentile of the release-to-cutoff gaps seen among
the models that publish both dates, with a warning naming the figure. A
model with no release date at all falls back to the global epoch. Each
answer is stored in `$XDG_DATA_HOME/axiomaton/cutoffs.json`, which can be
edited by hand, and is reused until `-refresh` is given; `--offline` skips
the agent.

## Projects, configuration layers and logs

A project can have an `axiomaton.cnf` file describing the subjects relevant
to it. `ax` searches from the current directory up to the root for every
`axiomaton.cnf`, layering them on top of each other, so a project or a
system can provide subjects. Next to each `axiomaton.cnf` can be an
`axiomlog` that answers for those subjects. Default subjects are only taken
from files up to the nearest directory containing a `.git` entry, which
`--global`/`-g` disables; logs above the repository are still consulted. A
user wide log at `$XDG_DATA_HOME/axiomaton/axiomlog` is always consulted.

### Default subjects

When no subject is named, the default subjects are those declared in
`axiomaton.cnf` files (the nearest declaration of a name wins) plus, inside
a git repository, every built-in subject detected from the project's own
files, which is the same list `ax init` would write. No file is written for
this. `ax for` and `ax sync` use the same defaults. When there are none,
nothing is printed and a warning goes to stderr. A detected built-in is
reported even when an `axiomaton.cnf` leaves it out.

### Write target

`add` and `update` append to the `axiomlog` beside the nearest writeable
`axiomaton.cnf` inside the repository (or, outside any repository, in the
working directory itself), creating the log when it does not exist yet, and
otherwise to the user wide log. An `axiomlog` with no `axiomaton.cnf`
beside it is read but never written to. `-l <axiomlog>` overrides the
target.

### Subject detection

Built-in subjects detect themselves from the project's files, searching the
directory and its parents up to the repository root:

- `go` when a `go.mod` is found, and every direct requirement in that
  `go.mod` adds a `go:<module path>` subject (e.g. `go:golang.org/x/net`);
  indirect requirements are skipped.
- `rust` when a `Cargo.toml` is found.
- `python` when a `pyproject.toml`, `setup.py`, `setup.cfg`, `Pipfile` or
  `requirements.txt` is found.
- `zig` when a `build.zig` or `build.zig.zon` is found.
- `snapcraft` when a `snapcraft.yaml` is found under `snap/`, at the top
  level, as `.snapcraft.yaml` or under `build-aux/snap/`.
- `charmcraft` when a `charmcraft.yaml` is found.
- `rockcraft` when a `rockcraft.yaml` is found.

Detected subjects carry no clock. A built-in subject named without a clock,
in the file or on the command line, takes its clock from the project at
query time, so the file never goes stale:

- `go` uses the `go` directive in `go.mod`; `go:<module>` uses that
  module's `require` line.
- `rust` uses the release pinned in `rust-toolchain.toml` or
  `rust-toolchain`, else the package's `rust-version`, following a
  workspace inheritance to the workspace root.
- `python` uses the release pinned in `.python-version`, else the lower
  bound of `requires-python` in `pyproject.toml`, a Poetry `python`
  constraint, or a Pipfile `python_version`.
- `zig` uses the compiler pinned in `.zigversion`, else
  `minimum_zig_version` in `build.zig.zon`.
- `snapcraft` uses the base number from `build-base`, else `base`, so
  `core24` is `24`.
- `charmcraft` uses the Ubuntu release number from `build-base`, else
  `base` (`ubuntu@24.04` is `24.04`), or the first `build-on` channel in
  the older `bases` format.
- `rockcraft` likewise uses `build-base`, else `base`, accepting both
  `ubuntu@24.04` and the older `ubuntu:22.04`, so a `bare` rock takes its
  `build-base`.

A project that pins nothing is reported without a clock, that is, every
release. Give an explicit clock in `axiomaton.cnf` to pin a version
instead.

The base may be written as it appears in the yaml wherever a clock is typed
or suggested: `core26` and `ubuntu@24.04` are read as `26` and `24.04` by
`ax add`, `-c` and the confirmation agent's corrections.

### Two-part clocks

`snapcraft`, `charmcraft` and `rockcraft` have two-part clocks: the base
first, then the tool's release (`[snapcraft, 24, 8.3.0]`,
`[charmcraft, 24.04, 3.4.0]`, `[rockcraft, 24.04, 1.5.0]`). A project pins
only its base; an axiom carries the base it holds from and, when the
behaviour arrived with a release rather than a base, that release too.
Axioms whose base is newer than the project's are left out, and so are
release-specific axioms newer than a release given explicitly.

### `ax init`

`ax init` writes an `axiomaton.cnf` listing the detected subjects, each
with a comment naming the file it was detected from. Further built-in
subjects can be named as arguments, e.g. `ax init go go:golang.org/x/text`.
Running it again on a project that already has an `axiomaton.cnf` updates
the file in place: subjects detected or requested that it does not list yet
are appended, and everything else, including hand-written subjects, shapes,
update briefs and comments, is kept. A built-in subject the file lists
without a clock that is no longer detected is pointed out, not removed.
`-force` replaces the file. No `axiomlog` is written; the first `ax add` or
`ax update` creates it beside the file.

### The `go:<module>` family

`go:<module>` subjects are a built-in family: any valid module path after
the prefix is a subject with a `version` clock of kind `semver`, with no
declaration needed. The `golang.org/x` modules (crypto, exp, image, mod,
net, oauth2, sync, sys, term, text, time and tools) ship with embedded logs
like the language subjects do; any other module starts empty,
`ax update go:<module>` builds its log in the project, and until then
`ax since` warns that nothing is known for the subject.

### Project file format

`axiomaton.cnf` and `ax.cnf` are written in the
[cnf](https://github.com/canonical/cnf) format, read with
`github.com/canonical/go-cnf/cnfmap` and decoded through its JSON form.
Strings are always quoted, a dotted key such as `shapes.juju` is a path
through nested maps, and a list is written inline (`["go", "1.26.1"]`),
dashed (one `- ` line per item, each on a single line) or fenced (`---`
between items, which may then span several lines).

Each subject is a bare name `"go"`, the compact list `["name", "clock
values"...]` or a map with `name`, `clock`, `shape` and `update` keys.
Subjects that are not built in need a clock shape, declared inline or under
`shapes`, before clocked axioms can be added for them:

```
subjects:
    - "go"  # clock read from go.mod; pin with ["go", "1.26.1"]
    - {name: "myservice", clock: ["2.3.0"], shape: [{name: "version", kind: "semver"}]}
    - ["juju", "3f9c2a1"]
shapes:
    juju:
        - {name: "commit", kind: "git", repo: "../juju"}
```

A subject map too long for one line goes in the fenced form instead:

```
subjects:
    ---
    "go"
    ---
    name: "myservice"
    clock: ["2.3.0"]
    shape: [{name: "version", kind: "semver"}]
    ---
```

`ax init` edits the file through the cnf syntax tree, so comments and
hand-written entries survive, and a file written in the inline or fenced
form keeps that form.

Clock kinds are `semver`, `goversion`, `number`, `string` and `git`
(ordered by `git merge-base --is-ancestor` in `repo`, relative to the
config file).

## Search and supersession

Search combines a deterministic walk through the interleaved log files with
a filter that queries embedding and reranking models to find similar,
possibly contradictory, axioms for a subject. Similar axioms are resolved by
a decision model (such as Jev) that determines whether they contradict each
other or are merely alike. When they contradict, the newer by vector clock
is chosen as the truth. Embedding vectors, decisions and so on are cached
under `$XDG_CACHE_HOME/axiomaton/`. Without an embedding and decision model
configured, or with `--offline`, the deterministic output is still produced
but superseded axioms are not detected.

## User configuration

Configuration lives in `$XDG_CONFIG_HOME/axiomaton/ax.cnf`. `ax setup`
creates it: on a terminal it asks for the provider URL, API key and a model
for each role, writes the file and then sends a tiny request to each
configured model (and looks for a sandbox backend) to confirm everything
works. Every answer has a flag (`-base-url`, `-key` or `-key-env`,
`-embedding`, `-rerank`, `-decision`, `-summary`, `-agent`, `-image`),
`-y` accepts the defaults without prompting and `-no-check` skips the
connectivity test.

An existing `ax.cnf` is updated rather than replaced: on a terminal, after
a confirmation, the prompts offer its current values (`-force` skips the
confirmation); any value flag turns the run into a targeted update that
changes only the fields named, asks nothing and keeps the rest, so
`ax setup -agent openai/gpt-6-luna-pro` swaps one model. The run reports
each field it changed. A key given with `-key` is stored in the file, which
is then made readable by the owner only; otherwise the key is read from the
environment variable at run time.

The file requires an embedding model, a decision model and two general
purpose models: one for summarisation (a low cost model good at it) and one
capable of running tools for the agents. A reranking model is optional.

```
providers:
    # built in; shown here with its defaults
    openrouter: {base_url: "https://openrouter.ai/api", api_key_env: "OPENROUTER_API_KEY"}  # or api_key: "sk-or-..."
models:
    embedding: {model: "perplexity/pplx-embed-v1-4b"}
    rerank: {model: "cohere/rerank-v3.5"}  # optional
    decision: {model: "typesafe/jev-1.13"}  # OpenRouter Decisions API
    summary: {model: "openai/gpt-oss-120b"}
    agent: {model: "openai/gpt-6-luna-pro"}
summary_budget: 1500  # tokens
search:
    candidates: 8  # nearest neighbours per axiom
    min_similarity: 0.35  # cosine floor
    rerank_top_n: 5
    min_relevance: 0.1
    decision_threshold: 0.5  # minimum probability to accept a verdict
retry:
    max_attempts: 8  # tries per model request
    initial_backoff: 1  # seconds; doubles each attempt, with jitter
    max_backoff: 60  # seconds
update:
    backend: "auto"  # podman if installed, else lxd; or podman, lxd, lxd-vm
    image: "public.ecr.aws/ubuntu/ubuntu:latest"  # podman image
    lxd_image: "ubuntu:26.04"  # LXD image for containers and virtual machines
    max_iterations: 80  # tool calls per agent run
    exec_timeout: 300  # seconds per command in the container
    confirmations: 4  # confirmation agents at once (default: half the CPUs)
proxy:
    disabled: false  # containers have no network except the caching proxy
    max_object_mb: 1024
sync:
    file: "CLAUDE.local.md"  # defaults for ax sync in projects it has not seen
    model: "openai/gpt-6-luna-pro"  # or since: "2025-06-01"
    projects: {}  # filled in by ax sync, one entry per project
```

Any model may set its own `provider`, `base_url`, `api_key` or
`api_key_env`. Embeddings, rerank and chat use the OpenAI compatible `/v1`
endpoints, so an OpenAI compatible server works for those roles; the
decision role uses the OpenRouter Decisions API.

Requests that fail with 429, 5xx or a dropped connection are retried with
exponential backoff and full jitter, honouring any `Retry-After` the
provider sends; `-v` logs each retry.

## Agent context files

`ax sync` writes the report that `ax since` or `ax for` would print into an
agent context file in the project, so the agent working there always has
the changes since its own knowledge cutoff in front of it. The first run in
a project asks two things, or takes them from flags, and remembers both in
`ax.cnf` under `sync.projects` keyed by the project directory, so every
later run is just `ax sync`:

- what to report since: an OpenRouter model id (its knowledge cutoff is
  looked up as `ax for` does) or a date, given as the argument;
- which file to write: `-agent claude` (CLAUDE.local.md), `codex`
  (AGENTS.override.md), `gemini` (GEMINI.md), `copilot`
  (.github/copilot-instructions.md), or `-file <path>` relative to the
  project.

The report goes between a `# Axiom changes` heading and an
`**end of axiom changes**` line; anything else in the file is left alone,
and a file without the block gets it appended. `-s` summarises the report
with the summary model and `-refresh` refetches the model catalogue.

A context file is personal to a checkout and should not be committed, so
inside a repository `ax sync` warns when the file is not ignored, or is
already tracked by git, and says what to add to `.gitignore`. The file is
written either way; `.gitignore` is never edited. Files outside any
repository need nothing.

`sync.file`, `sync.model` or `sync.since` in `ax.cnf` set defaults for
projects that have never been synced, so a user who always works with the
same agent and model never gets asked:

```
sync:
    file: "CLAUDE.local.md"
    model: "openai/gpt-6-luna-pro"  # or since: "2025-06-01"
    projects:
        "/home/me/src/juju": {file: "CLAUDE.local.md", since: "2025-06-01"}
```

## Updating logs with agents

`ax update` works for the built-in subjects (`go`, `python`, `rust`, `zig`,
`snapcraft`, `charmcraft`, `rockcraft`, and `go:<module path>` for any Go
module) and for project subjects that carry an update brief (see
[Custom subjects](#custom-subjects)).

### Sandboxes

An update first runs a sandboxed agent, built on
github.com/microsoft/agent-framework-go, whose tool calls run inside a
fresh instance. The instance is a podman container when podman is installed
and an LXD system container otherwise; `update.backend` chooses explicitly
between `podman`, `lxd` and `lxd-vm` (LXD virtual machines). A built-in
subject can declare that its agents need a real machine by implementing
`RequiresVM() bool`; such subjects always run in an LXD virtual machine and
fail with a clear message if LXD is not installed. `snapcraft`,
`charmcraft` and `rockcraft` are such subjects: their agents install snapd,
the tool and LXD inside the machine and run real builds to verify what they
record.

### Proposing

The agent extends the log from the subject's release notes. For a module
subject it takes the release list and dates from the module proxy and works
out what changed for callers from the module's release notes, an API
comparison between tags (apidiff) and the commit log, starting from the
newest release the log already covers or, for an empty log, from releases
published since the global epoch.

A module that has never been tagged, such as `golang.org/x/exp`, is
reviewed in batches of commits that stand in for releases: the agent lists
the commits in scope with the `go` directive at each one and hands them to
a `batch_commits` tool, which deterministically opens a new release when
the `go` directive changes, after a gap of more than a month between
commits, or at a new calendar month (so at most one release per month
otherwise), and names each release by the pseudo-version of its last commit
(`v0.0.0-20250320100000-c3c3c3c3c3c3`). That is the same form a `go.mod`
records for the commit, so a project's requirement and the axioms' clocks
compare directly.

### Confirming

After the agent has concluded, each proposed axiom is handed to a new agent
in a new container to confirm. Confirmations run several at a time
(`update.confirmations` in `ax.cnf`, half the CPUs by default, or `-j n` on
the command line), each in its own container, and the results keep the
proposal order. Only confirmed axioms are appended automatically; the rest
need user input.

A confirmation agent that rejects a statement says which kind of rejection
it is. An *inaccurate* axiom (wrong release or date, wrong identifier or
scope, overstated or incomplete) comes back with a correction the agent did
verify: a corrected statement, date, clock (release) or any combination,
or, when the statement bundles several facts or is right only in part, a
*split* into separate axioms each with its own statement and, where
different, date and clock. An *unfounded* one comes back with no
suggestion.

A correction is validated (the clock against the subject's shape; a date on
or before the global epoch drops the proposal, or that part of a split, as
prior knowledge), and each replacement is confirmed once more by a fresh
agent: one that succeeds is appended in place of the proposal, one that
fails waits for review on its own, and the original is kept alongside in
the pending record either way.

### Review

On a terminal the user is asked about each remaining proposal, with `c`
accepting the suggested correction, or every part of a split, where there
is one. Otherwise the proposals are written to `<axiomlog>.pending`
(suggestions under `verdict.correction`, `verdict.date`, `verdict.clock`
and `verdict.split`) for later review, or accepted with `-y`, which takes
the suggestion over the original when the agent offered a usable one.

### Shipped logs

The built-in logs are embedded from the source tree and refreshed by
maintainers with `ax update <subject> -l <path>`:

| Subject | Path | How the agent works |
| --- | --- | --- |
| `go` | `internal/subjects/golang/axiomlog` | reads the Go release notes |
| `rust` | `internal/subjects/rust/axiomlog` | reads RELEASES.md from rust-lang/rust and installs toolchains with rustup |
| `python` | `internal/subjects/python/axiomlog` | reads the What's New documents and installs exact CPython releases with uv |
| `zig` | `internal/subjects/zig/axiomlog` | reads each version's release notes and the download index on ziglang.org |
| `snapcraft` | `internal/subjects/snapcraft/axiomlog` | runs in an LXD virtual machine, reads the release notes and builds small snaps on each base |
| `charmcraft` | `internal/subjects/charmcraft/axiomlog` | likewise, packing small charms |
| `rockcraft` | `internal/subjects/rockcraft/axiomlog` | likewise, packing small rocks |
| `go:<module>` | `internal/subjects/gomod/seeds/<module path>/axiomlog` | as for any module, e.g. `ax update go:golang.org/x/net -l internal/subjects/gomod/seeds/golang.org/x/net/axiomlog` |

Adding a directory with an `axiomlog` under the gomod seeds ships another
module.

## Custom subjects

Any subject declared in `axiomaton.cnf` can be updated by the same agents
once its entry carries an `update:` block. The block is the research brief:
what the subject is, where its truth lives and how to prepare the sandbox.
Everything else (the sandbox, the proxy, proposing, confirming,
corrections, splits, review, the epoch floor) is exactly as for built-in
subjects.

```
subjects:
    ---
    name: "juju"
    clock: ["3.6.4"]
    shape: [{name: "version", kind: "semver"}]
    update:
        about:
            """
            Juju, the application orchestration engine. Axioms are what a charm
            author or an operator driving the juju CLI needs to know: CLI and
            model config changes, charm and relation semantics, supported bases.
            """
        sources:
            - "https://github.com/juju/juju/releases"
            - "https://documentation.ubuntu.com/juju/3.6/"
            - "../juju"  # a local checkout, shipped into the sandbox
        setup: "snap install juju --classic"  # optional, run in every fresh sandbox
        clock: "the Juju release the statement holds from, without a leading v"  # optional
        verify: "a ready `juju bootstrap localhost` is the fastest check"  # optional
        vm: false  # optional, forces the lxd-vm backend
    ---
```

`about` and at least one source are required, and the subject needs a
declared clock shape. Sources are listed most authoritative first. An https
URL is fetched through the cache proxy (git over https is mirrored like any
other repository). A path is a local git checkout, relative to the config
file: because the sandbox has no access to the host, it is packed with
`git bundle` before each run, pushed to `/work/src/<dir>.bundle` and the
agents are told to clone it from there. SSH remotes and plain http are
refused when the file is read. `setup` runs once in every sandbox, with the
proxy environment, before the update agent and before each confirmation
agent; a failing setup aborts the run. `clock` adds to the generated
description of each dimension (its name and kind), and `verify` is extra
guidance for the confirmation agent. `vm: true` runs the agents in an LXD
virtual machine. The nearest `axiomaton.cnf` declaring the subject wins, as
for shapes, and `ax subjects` marks such subjects `updatable`. A built-in
subject cannot be given a brief.

For a `git` clock the agent has no version to name releases by, so it gets
the same `batch_commits` tool as untagged Go modules, naming each batch by
the full hash of its last commit: that hash is the clock value, the batch's
date is the time, and the shape's `repo` orders the axioms on the host.

## Cache proxy and network isolation

Agent instances have no way to the network except a caching forward proxy
that `ax` runs on the host for the length of the command. Podman containers
are created with no network of their own (`--network none`), and for each
one `ax` runs a small forwarder of its own binary inside the container's
network namespace (via `nsenter`, under `podman unshare` when rootless)
that relays `127.0.0.1:3128` in the container to the proxy's unix socket on
the host. LXD containers and virtual machines are attached to a dedicated
bridge network named `axiomaton` that `ax` creates with no NAT and no
routing and a network ACL that drops all egress except DHCP and TCP to
ports 31280-31299 on the host's side of the bridge, where the proxy listens
(one port per running `ax`); before an instance is used `ax` checks from
inside it that the proxy answers and that the internet does not. The
network and ACL are created once and shared by every `ax` on the machine;
`lxc network delete axiomaton` and `lxc network acl delete axiomaton`
remove them.

The proxy speaks only HTTP: plain requests are relayed by URL, and
`CONNECT` is accepted for port 443 alone and even then terminated by the
proxy, which presents a certificate from its own authority and talks HTTPS
to the origin itself. Nothing else can leave the container, so SSH, raw TCP
and any other protocol are blocked by construction rather than by a
firewall rule the agent could undo. An instance that cannot be attached to
the proxy is not started. Setting `proxy.disabled: true` gives instances
the backend's default network, with no caching and no restriction.

### Certificates

The CA is generated on first use and kept at
`$XDG_DATA_HOME/axiomaton/proxy/ca.crt` (key readable by the owner only).
The certificate is installed in each container at `/etc/axiomaton/ca.crt`
and under `/usr/local/share/ca-certificates`, so `apt-get install
ca-certificates` adds it to the system store, and every command runs with
`HTTP_PROXY`/`HTTPS_PROXY`, `SSL_CERT_FILE`, `CURL_CA_BUNDLE`,
`GIT_SSL_CAINFO`, `REQUESTS_CA_BUNDLE`, `PIP_CERT`, `CARGO_HTTP_CAINFO` and
`NODE_EXTRA_CA_CERTS` set; apt gets a matching `apt.conf.d` snippet. The
proxy URL carries a random per-process token as well.

### Caching

Responses to plain `GET`s are stored under `$XDG_CACHE_HOME/axiomaton/proxy`.
Immutable artefacts (`.deb`, module `.zip`/`.info`/`.mod`, wheels,
tarballs, apt pools, PyPI package files, dated Rust dists and so on) are
served from the cache for a month; indexes and metadata (`InRelease`,
`@v/list`, PyPI simple pages) for a few minutes, after which they are
revalidated with `If-None-Match`/`If-Modified-Since` and served stale if
the origin is unreachable. `Cache-Control: max-age` extends either,
`no-store` and `private` are honoured, and non-`GET`, `Range` and oversized
responses pass straight through.

Git over HTTPS is cached as repositories rather than responses, since smart
HTTP fetches are `POST`s whose answers depend on what the client has. The
first fetch of a repository through the proxy makes a bare mirror under
`$XDG_CACHE_HOME/axiomaton/proxy/git`, later fetches are answered from the
mirror by `git http-backend`, and the mirror is refreshed from the origin
when it is more than a few minutes old (or served as is if the origin is
unreachable). Requests that carry credentials, and pushes, pass through to
the origin untouched; this needs `git` on the host.

Several `ax` commands can run at once: each runs its own proxy over the
same cache, CA and mirror directories, and they coordinate with file locks
so a file is downloaded once however many containers ask for it, a mirror
is refreshed by one process at a time, and only one process creates the CA
while the others load it. Cache entries are written atomically, so a
process never sees another's half-written file.

Each response carries an `X-Axiomaton-Cache` header (`HIT`, `MISS`,
`REVALIDATED`, `STALE`, `BYPASS`, or `GIT-...` for mirrored git), and `-v`
logs each request, including refused `CONNECT`s.

## Log format

An `axiomlog` is JSON lines; blank lines and `#` comments are ignored. The
`id` is a content hash and is recomputed on read.

```json
{"subject":"go","time":"2026-08-19T00:00:00Z","clock":{"version":"1.27.0"},"statement":"A method declaration may declare its own type parameters.","source":"https://go.dev/doc/go1.27","by":"agent"}
```

## Testing

```sh
go test ./...
AX_PODMAN_TEST=1 go test ./internal/update/ -run TestPodman      # needs podman and network
AX_LXD_TEST=1 go test ./internal/update/ -run TestLXDIntegration  # needs LXD; AX_LXD_VM_TEST=1 for a VM
```
