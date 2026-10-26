# Axiomaton

Axiomaton is a CLI tool that, when given a cutoff date, answers with what
foundational knowledge (axioms) has changed. An `axiomlog` is an append-only log
that contains statements that are considered true at a given clock for a given
subject.

A clock is a vector of multiple values that are subject dependent, it must contain
a UTC time-stamp, but also could contain a semantic version or another ordered
value (either independantly ordered in the case of a number or string, or externally
ordered such as a git commit/repo pair).

To query the `axiomlog`, you can run the tool with `ax since 2025-08-01 go 1.26.1`,
which evaluates the log, for the go (sub-clock 1.26.1) subject
since 2025-08-01 (primary clock), what has changed. It produces a Markdown output
that could be injested by a LLM in it's context. A `--summarise`/`-s` flag can be
provided to run the determistic generated output through a model to summarise it.

`ax for openai/gpt-5-mini go 1.26.1` does the same with the cutoff taken
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
falls back to the global epoch, 2023-08-01. Each answer is stored in
`$XDG_DATA_HOME/axiomaton/cutoffs.json`, which can be edited by hand, and is
reused until `-refresh` is given; `--offline` skips the agent.

The result contains axioms whose primary clock is after the cutoff and whose
sub-clock is at or before the subject clock given (so `go 1.26.1` excludes
Go 1.27 facts). Axioms without a sub-clock always apply.

The global epoch, 2023-08-01, is a hard floor: anything that became true
on or before it is assumed to be prior knowledge of every model. A cutoff
earlier than the epoch (given to `since`, or resolved by `for`) is raised to
it with a warning, `ax add` refuses a date that is not after it, and the
update agents are told not to propose axioms on or before it, with the
propose tool rejecting any that slip through.

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
`axiomlog`). Built-in subjects detect themselves: `go` is listed when a
`go.mod` is found in the directory or its parents up to the repository root,
and every direct requirement in that `go.mod` adds a
`go:<module path>` subject (e.g. `go:golang.org/x/net`);
indirect requirements are skipped. `rust` is listed when a `Cargo.toml` is
found the same way, `python` when a `pyproject.toml`, `setup.py`,
`setup.cfg`, `Pipfile` or `requirements.txt` is, `zig` when a `build.zig`
or `build.zig.zon` is, and `snapcraft` when a `snapcraft.yaml` is (under
`snap/`, at the top level, as `.snapcraft.yaml` or under
`build-aux/snap/`), `charmcraft` when a `charmcraft.yaml` is, and
`rockcraft` when a `rockcraft.yaml` is. Detected subjects are written without a
clock, and a built-in subject named without a clock, in the file or on the
command line, takes its clock from the project at query time (`go` uses
the `go` directive in `go.mod`; `go:<module>` uses that module's
`require` line; `rust` uses the release pinned in `rust-toolchain.toml` or
`rust-toolchain`, else the package's `rust-version`, following a workspace
inheritance to the workspace root; `python` uses the release pinned in
`.python-version`, else the lower bound of `requires-python` in
`pyproject.toml`, a Poetry `python` constraint, or a Pipfile
`python_version`; `zig` uses the compiler pinned in `.zigversion`, else
`minimum_zig_version` in `build.zig.zon`; `snapcraft` uses the base number
from `build-base`, else `base`, so `core24` is `24`; `charmcraft` uses the
Ubuntu release number from `build-base`, else `base` (`ubuntu@24.04` is
`24.04`), or the first `build-on` channel in the older `bases` format;
`rockcraft` likewise uses `build-base`, else `base`, accepting both
`ubuntu@24.04` and the older `ubuntu:22.04`, so a `bare` rock takes its
`build-base`), so the file never goes stale. A project that pins nothing is reported without
a clock, i.e. every release.

The base may be written as it appears in the yaml wherever a clock is
typed or suggested: `core26` and `ubuntu@24.04` are read as `26` and `24.04`
by `ax add`, `-c` and the confirmation agent's corrections.

`snapcraft`, `charmcraft` and `rockcraft` have two-part clocks: the base
first, then the tool's release (`[snapcraft, 24, 8.3.0]`,
`[charmcraft, 24.04, 3.4.0]`, `[rockcraft, 24.04, 1.5.0]`). A
project pins only its base; an axiom carries the base it holds from and,
when the behaviour arrived with a release rather than a base, that release
too. Axioms whose base is newer than the project's are left out, and so are
release-specific axioms newer than a release given explicitly.
Give an explicit clock to pin a version instead. Further built-in subjects can
be named as arguments, e.g. `ax init go go:golang.org/x/text`, and
dependencies you do not want reported on can simply be deleted from the file.

`go:<module>` subjects are a built-in *family*: any valid module path
after the prefix is a subject with a `version` clock of kind `semver`, with no
declaration needed. The `golang.org/x` modules (crypto, exp, image, mod, net,
oauth2, sync, sys, term, text, time and tools) ship with embedded logs like
the language subjects do; any other module starts empty, `ax update
go:<module>` builds its log in the project (see below), and until then
`ax since` warns that nothing is known for the subject.

`axiomaton.cnf` files are written in YAML. Each subject is either the compact
`[name, clock values...]` form or a mapping. Subjects that are not built in need
a clock shape, declared inline or under `shapes:`, before clocked axioms can be
added for them:

```yaml
subjects:
  - go                 # clock read from go.mod; pin with [go, 1.26.1]
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
  embedding: {model: perplexity/pplx-embed-v1-4b}
  rerank:    {model: cohere/rerank-v3.5}        # optional
  decision:  {model: typesafe/jev-1.13}         # OpenRouter Decisions API
  summary:   {model: openai/gpt-oss-120b}
  agent:     {model: openai/gpt-6-luna-pro}
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
  backend: auto                     # podman if installed, else lxd; or podman, lxd, lxd-vm
  image: public.ecr.aws/ubuntu/ubuntu:latest   # podman image
  lxd_image: ubuntu:26.04           # LXD image for containers and virtual machines
  max_iterations: 80                # tool calls per agent run
  exec_timeout: 300                 # seconds per command in the container
  confirmations: 4                  # confirmation agents at once (default: half the CPUs)
proxy:
  disabled: false                   # containers have no network except the caching proxy
  max_object_mb: 1024
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
- `ax add 2026-08-19 go 1.27.0 "a method declaration may declare its own type parameters."`
- `ax update go`
- `ax update rust`
- `ax update python`
- `ax update zig`
- `ax update snapcraft`
- `ax update charmcraft`
- `ax update rockcraft`
- `ax update go:golang.org/x/net`
- `ax update juju` (a project subject with an `update:` block, see below)
- `ax subjects`
- `ax init`
- `ax setup`

`add` and `update` append to the nearest writeable `axiomlog` inside the
repository (a directory with an `axiomaton.cnf` or `axiomlog`), then the
repository root, then the user wide log. `-l <axiomlog>` overrides the target.

The update command works for built-in subjects: `go`, `python`, `rust`,
`zig`, `snapcraft`, `charmcraft`, `rockcraft`, and `go:<module path>` for any
Go module, and for project subjects that carry an update brief (see
[Custom subjects](#custom-subjects)). All subject updates are performed as such, firstly, a sandboxed
agent is run (using github.com/microsoft/agent-framework-go, with tool calls
running inside a fresh instance), to update the axiomlog
based on go's release notes. The instance is a podman container when podman
is installed and an LXD system container otherwise (`update.backend`
chooses explicitly: `podman`, `lxd`, or `lxd-vm` for LXD virtual machines).
A built-in subject can declare that its agents need a real machine by
implementing `RequiresVM() bool`; such subjects always run in an LXD virtual
machine, and fail with a clear message if LXD is not installed. `snapcraft`,
`charmcraft` and `rockcraft` are such subjects: their agents install snapd,
the tool and LXD inside the machine and run real builds to verify what they
record. For a module subject the agent takes the release
list and dates from the module proxy and works out what changed for callers
from the module's release notes, an API comparison between tags (apidiff) and
the commit log, starting from the newest release the log already covers, or,
for an empty log, from releases published since the global epoch
(2023-08-01). A module that has never been tagged, such as
`golang.org/x/exp`, is reviewed in batches of commits that stand in for
releases: the agent lists the commits in scope with the `go` directive at
each one and hands them to a `batch_commits` tool, which deterministically
opens a new release when the `go` directive changes, after a gap of more
than a month between commits, or at a new calendar month (so at most one
release per month otherwise), and names each release by the pseudo-version
of its last commit (`v0.0.0-20250320100000-c3c3c3c3c3c3`). That is the same
form a `go.mod` records for the commit, so a project's requirement and the
axioms' clocks compare directly. Then after the agent has concluded, for each individual
axiom added, a new agent, with a new container, is asked to confirm, it; these
confirmations run several at a time (`update.confirmations` in `ax.cnf`, half
the CPUs by default, or `-j n` on the command line), each in its own
container, and the results keep the proposal order. Only axioms
that are confirmed are added automatically, axioms that are not confirmed require
user input. A confirmation agent that rejects a statement says which kind of
rejection it is: an *inaccurate* axiom (wrong release or date, wrong
identifier or scope, overstated or incomplete) comes back with a correction
the agent did verify, which may be a corrected statement, a corrected date,
a corrected clock (release) or any combination, or, when the statement
bundles several facts or is right only in part, a *split* into separate
axioms each with its own statement and, where different, date and clock,
while an *unfounded* one comes back with no suggestion. A correction is
validated (the clock against the subject's shape; a date on or before the
global epoch drops the proposal, or that part of a split, as prior
knowledge), and each replacement is confirmed once more by a fresh agent:
one that succeeds is appended in place of the proposal, one that fails waits
for review on its own, and the original is kept alongside in the pending
record either way. On a terminal the user is asked about each remaining
proposal, with `c` accepting the suggested correction, or every part of a
split, where there is one; otherwise they are written to `<axiomlog>.pending`
(suggestions under `verdict.correction`, `verdict.date`, `verdict.clock` and
`verdict.split`) for later review, or accepted with `-y`, which takes the
suggestion over the original when the agent offered a usable one.

The built-in go log is embedded from `internal/subjects/golang/axiomlog`;
maintainers refresh it with `ax update go -l internal/subjects/golang/axiomlog`.
The rust log at `internal/subjects/rust/axiomlog` is generated the same way
with `ax update rust -l internal/subjects/rust/axiomlog`; the agent reads
RELEASES.md from rust-lang/rust and installs toolchains with rustup. The
python log at `internal/subjects/python/axiomlog` comes from
`ax update python -l internal/subjects/python/axiomlog`; the agent reads the
What's New documents and installs exact CPython releases with uv. The zig
log at `internal/subjects/zig/axiomlog` comes from
`ax update zig -l internal/subjects/zig/axiomlog`; the agent reads each
version's release notes and the download index on ziglang.org. The
snapcraft log at `internal/subjects/snapcraft/axiomlog` comes from
`ax update snapcraft -l internal/subjects/snapcraft/axiomlog`, which runs
in an LXD virtual machine; the agent reads the snapcraft release notes and
builds small snaps on each base. Likewise
`ax update charmcraft -l internal/subjects/charmcraft/axiomlog` for
charmcraft, packing small charms, and
`ax update rockcraft -l internal/subjects/rockcraft/axiomlog` for
rockcraft, packing small rocks. The shipped
module logs live under `internal/subjects/gomod/seeds/<module path>/axiomlog`,
e.g. `ax update go:golang.org/x/net -l internal/subjects/gomod/seeds/golang.org/x/net/axiomlog`;
adding a directory with an `axiomlog` there ships another module.

### Custom subjects

Any subject declared in `axiomaton.cnf` can be updated by the same agents
once its entry carries an `update:` block. The block is the research brief:
what the subject is, where its truth lives and how to prepare the sandbox.
Everything else (the sandbox, the proxy, proposing, confirming, corrections,
splits, review, the epoch floor) is exactly as for built-in subjects.

```yaml
subjects:
  - name: juju
    clock: [3.6.4]
    shape: [{name: version, kind: semver}]
    update:
      about: >
        Juju, the application orchestration engine. Axioms are what a charm
        author or an operator driving the juju CLI needs to know: CLI and
        model config changes, charm and relation semantics, supported bases.
      sources:
        - https://github.com/juju/juju/releases
        - https://documentation.ubuntu.com/juju/3.6/
        - ../juju                      # a local checkout, shipped into the sandbox
      setup: snap install juju --classic   # optional, run in every fresh sandbox
      clock: the Juju release the statement holds from, without a leading v   # optional
      verify: a ready `juju bootstrap localhost` is the fastest check          # optional
      vm: false                        # optional, forces the lxd-vm backend
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

### Cache proxy and network isolation

Agent instances have no way to the network except a caching forward proxy
that `ax` runs on the host for the length of the command. Podman containers
are created with no network of their own (`--network none`), and for each
one `ax` runs a small forwarder of its own binary inside the container's
network namespace (via `nsenter`, under `podman unshare` when rootless) that
relays `127.0.0.1:3128` in the container to the proxy's unix socket on the
host. LXD containers and virtual machines are attached to a dedicated bridge
network named `axiomaton` that `ax` creates with no NAT and no routing and a
network ACL that drops all egress except DHCP and TCP to ports 31280-31299
on the host's side of the bridge, where the proxy listens (one port per
running `ax`); before an instance is used `ax` checks from inside it that
the proxy answers and that the internet does not. The network and ACL are
created once and shared by every `ax` on the machine; `lxc network delete
axiomaton` and `lxc network acl delete axiomaton` remove them. The proxy
speaks only HTTP: plain requests are relayed by URL, and `CONNECT`
is accepted for port 443 alone and even then terminated by the proxy, which
presents a certificate from its own authority and talks HTTPS to the origin
itself. Nothing else can leave the container, so SSH, raw TCP and any other
protocol are blocked by construction rather than by a firewall rule the
agent could undo. An instance that cannot be attached to the proxy is not
started. Setting `proxy.disabled: true` gives instances the backend's
default network, with no caching and no restriction.

The CA is generated on first use and kept at
`$XDG_DATA_HOME/axiomaton/proxy/ca.crt` (key readable by the owner only).
The certificate is installed in each container at `/etc/axiomaton/ca.crt`
and under `/usr/local/share/ca-certificates`, so `apt-get install
ca-certificates` adds it to the system store, and every command runs with
`HTTP_PROXY`/`HTTPS_PROXY`, `SSL_CERT_FILE`, `CURL_CA_BUNDLE`,
`GIT_SSL_CAINFO`, `REQUESTS_CA_BUNDLE`, `PIP_CERT`, `CARGO_HTTP_CAINFO` and
`NODE_EXTRA_CA_CERTS` set; apt gets a matching `apt.conf.d` snippet. The
proxy URL carries a random per-process token as well.

Responses to plain `GET`s are stored under `$XDG_CACHE_HOME/axiomaton/proxy`.
Immutable artefacts (`.deb`, module `.zip`/`.info`/`.mod`, wheels, tarballs,
apt pools, PyPI package files, dated Rust dists and so on) are served from
the cache for a month; indexes and metadata (`InRelease`, `@v/list`, PyPI
simple pages) for a few minutes, after which they are revalidated with
`If-None-Match`/`If-Modified-Since` and served stale if the origin is
unreachable. `Cache-Control: max-age` extends either, `no-store` and
`private` are honoured, and non-`GET`, `Range` and oversized responses pass
straight through.

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

An `axiomlog` is JSON lines; blank lines and `#` comments are ignored. The `id`
is a content hash and is recomputed on read.

```json
{"subject":"go","time":"2026-08-19T00:00:00Z","clock":{"version":"1.27.0"},"statement":"A method declaration may declare its own type parameters.","source":"https://go.dev/doc/go1.27","by":"agent"}
```

## Building

```sh
go build ./cmd/ax
go test ./...
AX_PODMAN_TEST=1 go test ./internal/update/ -run TestPodman      # needs podman and network
AX_LXD_TEST=1 go test ./internal/update/ -run TestLXDIntegration  # needs LXD; AX_LXD_VM_TEST=1 for a VM
```
