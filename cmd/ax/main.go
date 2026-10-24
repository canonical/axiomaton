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

// Command ax answers what foundational knowledge has changed since a cutoff.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/canonical/axiomaton/internal/axiom"
	"github.com/canonical/axiomaton/internal/axiomlog"
	"github.com/canonical/axiomaton/internal/config"
	"github.com/canonical/axiomaton/internal/models"
	"github.com/canonical/axiomaton/internal/models/openrouter"
	"github.com/canonical/axiomaton/internal/query"
	"github.com/canonical/axiomaton/internal/subjects"
	"github.com/canonical/axiomaton/internal/update"
)

const usage = `ax answers what foundational knowledge (axioms) has changed since a cutoff.

Usage:
  ax [global flags] since <date> [subject [clock values...]]
  ax [global flags] add <date> <subject> [clock values...] "<statement>"
  ax [global flags] update <subject>
  ax [global flags] subjects
  ax [global flags] setup

Commands:
  since     Render the axioms for a subject that became true after <date>,
            restricted to the subject's clock (e.g. golang 1.26.1). Without a
            subject, the default subjects from axiomaton.cnf files are used.
  add       Append an axiom to the nearest writeable axiomlog.
  update    Run containerised agents to extend the log of a built-in subject.
  subjects  List known subjects and where they come from.
  setup     Write ax.cnf interactively and test the configured models.

Global flags:
`

type app struct {
	configPath string
	global     bool
	verbose    bool
	offline    bool
	dir        string

	stderr io.Writer
	stdout io.Writer
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		if !errors.Is(err, flag.ErrHelp) {
			fmt.Fprintf(os.Stderr, "ax: %v\n", err)
		}
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	a := &app{stdout: stdout, stderr: stderr}
	fs := flag.NewFlagSet("ax", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&a.configPath, "c", config.UserConfigPath(), "path to ax.cnf")
	fs.BoolVar(&a.global, "g", false, "do not stop default subject discovery at the nearest .git directory")
	fs.BoolVar(&a.global, "global", false, "alias for -g")
	fs.BoolVar(&a.verbose, "v", false, "log progress to stderr")
	fs.BoolVar(&a.offline, "offline", false, "skip all model calls; deterministic output only")
	fs.StringVar(&a.dir, "C", ".", "run as if started in this directory")
	fs.Usage = func() {
		fmt.Fprint(stderr, usage)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) == 0 {
		fs.Usage()
		return errors.New("no command given")
	}
	switch rest[0] {
	case "since":
		return a.since(ctx, rest[1:])
	case "add":
		return a.add(ctx, rest[1:])
	case "update":
		return a.update(ctx, rest[1:])
	case "subjects":
		return a.subjects(ctx, rest[1:])
	case "setup":
		return a.setup(ctx, rest[1:])
	case "help", "-h", "--help":
		fs.Usage()
		return flag.ErrHelp
	}
	fs.Usage()
	return fmt.Errorf("unknown command %q", rest[0])
}

// parseInterleaved parses flags that may appear before, between or after
// positional arguments, which the standard flag package does not do by
// itself. A literal "--" ends flag parsing.
func parseInterleaved(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return positional, nil
		}
		if terminated(args, rest) {
			return append(positional, rest...), nil
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
}

// terminated reports whether fs.Parse stopped because of a "--" argument,
// in which case rest is everything after it.
func terminated(args, rest []string) bool {
	i := len(args) - len(rest) - 1
	return i >= 0 && i < len(args) && args[i] == "--"
}

func (a *app) logf(format string, args ...any) {
	if a.verbose {
		fmt.Fprintf(a.stderr, "ax: "+format+"\n", args...)
	}
}

func (a *app) warnf(format string, args ...any) {
	fmt.Fprintf(a.stderr, "ax: warning: "+format+"\n", args...)
}

// env bundles everything discovered for the working directory.
type env struct {
	cfg    config.User
	ws     *axiomlog.Workspace
	reg    *axiom.Registry
	axioms []axiom.Axiom
}

func (a *app) load() (*env, error) {
	cfg, err := config.LoadUser(a.configPath)
	if err != nil {
		return nil, err
	}
	ws, err := axiomlog.Discover(a.dir)
	if err != nil {
		return nil, err
	}
	reg, err := ws.Registry(subjects.Shapes())
	if err != nil {
		return nil, err
	}
	seeds, err := subjects.Seeds()
	if err != nil {
		return nil, err
	}
	all, err := ws.Load(seeds...)
	if err != nil {
		return nil, err
	}
	return &env{cfg: cfg, ws: ws, reg: reg, axioms: all}, nil
}

func parseDate(s string) (time.Time, error) {
	for _, layout := range []string{"2006-01-02", time.RFC3339, "2006-01-02T15:04:05", "2006-01"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("cannot parse date %q (use YYYY-MM-DD or RFC 3339)", s)
}

func (a *app) since(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("ax since", flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	summarise := fs.Bool("s", false, "summarise the output with the summary model")
	fs.BoolVar(summarise, "summarise", false, "alias for -s")
	fs.Usage = func() {
		fmt.Fprintln(a.stderr, "usage: ax since [-s] <date> [subject [clock values...]]")
		fs.PrintDefaults()
	}
	rest, err := parseInterleaved(fs, args)
	if err != nil {
		return err
	}
	if len(rest) == 0 {
		fs.Usage()
		return errors.New("since: a cutoff date is required")
	}
	since, err := parseDate(rest[0])
	if err != nil {
		return err
	}
	e, err := a.load()
	if err != nil {
		return err
	}

	var targets []config.SubjectRef
	if len(rest) > 1 {
		targets = []config.SubjectRef{{Name: rest[1], Clock: rest[2:]}}
	} else {
		targets = e.ws.DefaultSubjects(a.global)
		if len(targets) == 0 {
			a.warnf("no default subjects: add an %s with a subjects list, or name a subject explicitly", config.ProjectFile)
			return nil
		}
	}

	res := a.resolver(e.cfg)
	var results []query.Result
	for _, t := range targets {
		subj, inferred := e.reg.Resolve(t.Name, t.Clock)
		if inferred {
			a.warnf("subject %q has no declared clock shape; inferred %s from the values given", t.Name, describeShape(subj.Shape))
		}
		clock, err := subj.Clock(t.Clock)
		if err != nil {
			return err
		}
		if len(axiomlog.Filter(e.axioms, t.Name)) == 0 {
			a.warnf("no axioms known for subject %q", t.Name)
		}
		r, err := query.Evaluate(ctx, e.axioms, query.Request{Since: since, Subject: subj, Clock: clock}, res)
		if err != nil {
			return err
		}
		results = append(results, r)
	}
	md := query.Render(since, results)
	if *summarise {
		ep, err := e.cfg.Resolve(config.RoleSummary)
		if err != nil {
			return fmt.Errorf("-s: %w", err)
		}
		a.logf("summarising with %s (budget %d tokens)", ep.Model, e.cfg.SummaryBudget)
		md, err = a.client(e.cfg, ep).Summarise(ctx, md, e.cfg.SummaryBudget)
		if err != nil {
			return err
		}
		md += "\n"
	}
	_, err = io.WriteString(a.stdout, md)
	return err
}

func describeShape(shape []axiom.ClockShape) string {
	parts := make([]string, len(shape))
	for i, d := range shape {
		parts[i] = fmt.Sprintf("%s:%s", d.Name, d.Kind)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// client builds an OpenRouter client with the configured retry policy.
func (a *app) client(cfg config.User, ep config.Endpoint) *openrouter.Client {
	return openrouter.New(ep).WithRetry(cfg.Retry, a.logf)
}

// resolver builds the model set for queries, degrading to deterministic mode
// when roles are missing.
func (a *app) resolver(cfg config.User) query.Resolver {
	res := query.Resolver{Search: cfg.Search, Log: a.logf}
	if a.offline {
		return res
	}
	cache := models.Cache{Dir: config.CacheDir()}
	if ep, err := cfg.Resolve(config.RoleEmbedding); err == nil {
		res.Embedder = models.CachedEmbedder{Embedder: a.client(cfg, ep), Cache: cache}
	} else {
		a.logf("%v", err)
	}
	if ep, err := cfg.Resolve(config.RoleDecision); err == nil {
		res.Decider = models.CachedDecider{Decider: a.client(cfg, ep), Cache: cache}
	} else {
		a.logf("%v", err)
	}
	if ep, err := cfg.Resolve(config.RoleRerank); err == nil {
		res.Reranker = models.CachedReranker{Reranker: a.client(cfg, ep), Cache: cache}
	} else if !errors.Is(err, config.ErrNotConfigured) {
		a.logf("%v", err)
	}
	if res.Embedder == nil || res.Decider == nil {
		a.warnf("embedding and decision models are not both configured in %s; contradictions will not be resolved (run ax setup)", a.configPath)
	}
	return res
}

func (a *app) add(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("ax add", flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	logPath := fs.String("l", "", "axiomlog file to append to (default: nearest writeable log)")
	source := fs.String("source", "", "where the statement comes from")
	fs.Usage = func() {
		fmt.Fprintln(a.stderr, `usage: ax add [-l axiomlog] [-source url] <date> <subject> [clock values...] "<statement>"`)
		fs.PrintDefaults()
	}
	rest, err := parseInterleaved(fs, args)
	if err != nil {
		return err
	}
	if len(rest) < 3 {
		fs.Usage()
		return errors.New("add: need a date, a subject and a statement")
	}
	when, err := parseDate(rest[0])
	if err != nil {
		return err
	}
	name := rest[1]
	values := rest[2 : len(rest)-1]
	statement := rest[len(rest)-1]

	e, err := a.load()
	if err != nil {
		return err
	}
	subj, inferred := e.reg.Resolve(name, values)
	if inferred {
		return fmt.Errorf("subject %q has no declared clock shape; declare it under shapes: in %s before adding clocked axioms", name, config.ProjectFile)
	}
	clock, err := subj.Clock(values)
	if err != nil {
		return err
	}
	ax := axiom.Axiom{Subject: name, Time: when, Clock: clock, Statement: statement, Source: *source, Added: time.Now().UTC(), By: "user"}
	ax.Normalise()
	if err := ax.Validate(); err != nil {
		return err
	}
	for _, existing := range e.axioms {
		if existing.ID == ax.ID {
			a.warnf("identical axiom already recorded in %s; nothing added", existing.Origin)
			return nil
		}
	}
	target := *logPath
	if target == "" {
		target, err = e.ws.WriteTarget()
		if err != nil {
			return err
		}
	}
	if err := axiomlog.Append(target, ax); err != nil {
		return err
	}
	fmt.Fprintf(a.stderr, "ax: added %s to %s\n", ax.ID, target)
	return nil
}

func (a *app) update(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("ax update", flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	logPath := fs.String("l", "", "axiomlog file to append to (default: nearest writeable log)")
	image := fs.String("image", "", "container image for the agents (default from ax.cnf)")
	yes := fs.Bool("y", false, "accept unconfirmed axioms without asking")
	fs.Usage = func() {
		fmt.Fprintln(a.stderr, "usage: ax update [-l axiomlog] [-image ref] [-y] <subject>")
		fs.PrintDefaults()
	}
	rest, err := parseInterleaved(fs, args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		fs.Usage()
		return errors.New("update: exactly one subject is required")
	}
	builtin, ok := subjects.Lookup(rest[0])
	if !ok {
		return fmt.Errorf("update only supports built-in subjects (%s); %q is not one", strings.Join(subjects.Names(), ", "), rest[0])
	}
	e, err := a.load()
	if err != nil {
		return err
	}
	ep, err := e.cfg.Resolve(config.RoleAgent)
	if err != nil {
		return err
	}
	target := *logPath
	if target == "" {
		target, err = e.ws.WriteTarget()
		if err != nil {
			return err
		}
	}
	img := e.cfg.Update.Image
	if *image != "" {
		img = *image
	}
	var logger *slog.Logger
	if a.verbose {
		logger = slog.New(slog.NewTextHandler(a.stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	}
	progress := func(format string, args ...any) { fmt.Fprintf(a.stderr, "ax: "+format+"\n", args...) }
	u := &update.Updater{
		Subject:     builtin,
		Runtime:     update.Podman{Image: img, Log: a.logf},
		Agent:       update.FrameworkAgent{Endpoint: ep, MaxIterations: e.cfg.Update.MaxIterations, MaxRetries: e.cfg.Retry.MaxAttempts - 1, Logger: logger},
		ExecTimeout: time.Duration(e.cfg.Update.ExecTimeout) * time.Second,
		Log:         progress,
	}
	existing := axiomlog.Filter(e.axioms, builtin.Name())
	axiom.Sort(existing, builtin.Shape())

	props, summary, err := u.Run(ctx, existing)
	if summary != "" {
		fmt.Fprintf(a.stderr, "ax: agent summary: %s\n", summary)
	}
	if err != nil {
		return err
	}
	confirmed, pending := update.Split(props)
	if len(confirmed) > 0 {
		if err := axiomlog.Append(target, confirmed...); err != nil {
			return err
		}
	}
	fmt.Fprintf(a.stderr, "ax: %d axiom(s) proposed, %d confirmed and appended to %s, %d need review\n", len(props), len(confirmed), target, len(pending))
	if len(pending) == 0 {
		return nil
	}
	var accepted []axiom.Axiom
	switch {
	case *yes:
		for _, p := range pending {
			ax := p.Axiom
			ax.By = "user"
			accepted = append(accepted, ax)
		}
	case isTerminal(os.Stdin):
		accepted, err = update.Review(pending, os.Stdin, a.stderr, builtin.Shape())
		if err != nil {
			return err
		}
	default:
		pendingPath := target + ".pending"
		if err := update.WritePending(pendingPath, pending); err != nil {
			return err
		}
		a.warnf("stdin is not a terminal; %d unconfirmed axiom(s) written to %s for review (use ax add to record them)", len(pending), pendingPath)
		return nil
	}
	if len(accepted) > 0 {
		if err := axiomlog.Append(target, accepted...); err != nil {
			return err
		}
	}
	fmt.Fprintf(a.stderr, "ax: %d reviewed axiom(s) appended to %s\n", len(accepted), target)
	return nil
}

func (a *app) subjects(_ context.Context, args []string) error {
	e, err := a.load()
	if err != nil {
		return err
	}
	counts := map[string]int{}
	origins := map[string]map[string]bool{}
	for _, ax := range e.axioms {
		counts[ax.Subject]++
		if origins[ax.Subject] == nil {
			origins[ax.Subject] = map[string]bool{}
		}
		origins[ax.Subject][ax.Origin] = true
	}
	names := map[string]bool{}
	for _, n := range e.reg.Names() {
		names[n] = true
	}
	for n := range counts {
		names[n] = true
	}
	defaults := map[string][]string{}
	for _, d := range e.ws.DefaultSubjects(a.global) {
		defaults[d.Name] = d.Clock
	}
	var sorted []string
	for n := range names {
		sorted = append(sorted, n)
	}
	sortStrings(sorted)
	for _, n := range sorted {
		subj, _ := e.reg.Lookup(n)
		line := fmt.Sprintf("%s\tshape %s\t%d axiom(s)", n, describeShape(subj.Shape), counts[n])
		if clock, ok := defaults[n]; ok {
			line += fmt.Sprintf("\tdefault @ %s", strings.Join(clock, " "))
		}
		var from []string
		for o := range origins[n] {
			from = append(from, shorten(o))
		}
		sortStrings(from)
		if len(from) > 0 {
			line += "\tfrom " + strings.Join(from, ", ")
		}
		fmt.Fprintln(a.stdout, line)
	}
	target, err := e.ws.WriteTarget()
	if err == nil {
		fmt.Fprintf(a.stderr, "ax: new axioms would be appended to %s\n", target)
	}
	return nil
}

func shorten(p string) string {
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(p, home) {
		return "~" + strings.TrimPrefix(p, home)
	}
	if rel, err := filepath.Rel(".", p); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return p
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
