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

package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/canonical/axiomaton/internal/config"
	"github.com/canonical/axiomaton/internal/models"
	"github.com/canonical/axiomaton/internal/models/openrouter"
	"github.com/canonical/axiomaton/internal/update"
)

// stdin and interactive are variables so tests can script the wizard.
var (
	stdin       io.Reader = os.Stdin
	interactive           = func() bool { return isTerminal(os.Stdin) }
)

const defaultProvider = "openrouter"

// defaultModels are offered when ax.cnf has nothing for a role.
var defaultModels = map[string]string{
	config.RoleEmbedding: "perplexity/pplx-embed-v1-4b",
	config.RoleRerank:    "",
	config.RoleDecision:  "typesafe/jev-1.13",
	config.RoleSummary:   "openai/gpt-oss-120b",
	config.RoleAgent:     "openai/gpt-6-luna-pro",
}

const probeTimeout = 90 * time.Second

func (a *app) setup(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("ax setup", flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	yes := fs.Bool("y", false, "accept defaults and flags without prompting")
	force := fs.Bool("force", false, "rewrite an existing ax.cnf without asking")
	noCheck := fs.Bool("no-check", false, "do not test the configured models and the agent backend")
	baseURL := fs.String("base-url", "", "provider base URL (default https://openrouter.ai/api)")
	key := fs.String("key", "", "API key to store in ax.cnf (default: read from the environment at run time)")
	keyEnv := fs.String("key-env", "", "environment variable holding the API key (default OPENROUTER_API_KEY)")
	image := fs.String("image", "", "container image for ax update agents")
	roleFlags := map[string]*string{}
	for _, role := range config.Roles {
		roleFlags[role] = fs.String(role, "", role+` model ("-" for none)`)
	}
	fs.Usage = func() {
		fmt.Fprintln(a.stderr, "usage: ax setup [-y] [-force] [-no-check] [-base-url url] [-key k | -key-env VAR] [-image ref] [-<role> model]...")
		fs.PrintDefaults()
		fmt.Fprintln(a.stderr, "With an existing ax.cnf, value flags change only the fields they name; without flags the prompts offer the current values.")
	}
	rest, err := parseInterleaved(fs, args)
	if err != nil {
		return err
	}
	if len(rest) != 0 {
		fs.Usage()
		return fmt.Errorf("setup: unexpected argument %q", rest[0])
	}

	path := a.configPath
	cfg, err := config.LoadUser(path)
	if err != nil {
		return err
	}
	exists := cfg.Path != ""
	// Flags that name a value turn a run against an existing file into a
	// targeted update: those fields change, nothing is asked, the rest is
	// kept.
	targeted := *baseURL != "" || *key != "" || *keyEnv != "" || *image != ""
	for _, f := range roleFlags {
		targeted = targeted || *f != ""
	}
	p := prompter{raw: stdin, in: bufio.NewReader(stdin), out: a.stderr, ask: !*yes && interactive() && !(exists && targeted)}
	before := snapshotUser(cfg)

	if exists && !*force && !targeted && p.ask {
		ok, err := p.yesNo(path+" already exists. Reconfigure it?", false)
		if err != nil {
			return err
		}
		if !ok {
			fmt.Fprintln(a.stderr, "ax: setup cancelled; nothing written")
			return nil
		}
	}
	if p.ask {
		verb := "Configuring"
		if exists {
			verb = "Updating"
		}
		fmt.Fprintf(a.stderr, "%s %s. Press Enter to accept the value in brackets.\n", verb, path)
	}

	// Provider.
	prov := cfg.Providers[defaultProvider]
	if *baseURL != "" {
		prov.BaseURL = *baseURL
	} else if prov.BaseURL, err = p.text("Provider base URL", prov.BaseURL); err != nil {
		return err
	}
	switch {
	case *key != "" && *keyEnv != "":
		return errors.New("setup: -key and -key-env are mutually exclusive")
	case *key != "":
		prov.APIKey, prov.APIKeyEnv = *key, ""
	case *keyEnv != "":
		prov.APIKey, prov.APIKeyEnv = "", *keyEnv
	default:
		if prov.APIKeyEnv == "" && prov.APIKey == "" {
			prov.APIKeyEnv = "OPENROUTER_API_KEY"
		}
		label := fmt.Sprintf("API key (blank to read $%s at run time)", prov.APIKeyEnv)
		if prov.APIKey != "" {
			label = "API key (blank to keep the stored key)"
		}
		entered, err := p.secret(label)
		if err != nil {
			return err
		}
		if entered != "" {
			prov.APIKey, prov.APIKeyEnv = entered, ""
		}
	}
	cfg.Providers[defaultProvider] = prov

	// Models.
	for _, role := range config.Roles {
		m := cfg.Models[role]
		choice := *roleFlags[role]
		if choice == "" {
			// A role an existing file leaves unconfigured stays so unless
			// the user is there to be offered the default.
			def := m.Model
			if def == "" && (!exists || p.ask) {
				def = defaultModels[role]
			}
			label := fmt.Sprintf("%s model, %s (\"-\" for none)", strings.ToUpper(role[:1])+role[1:], config.RoleNotes[role])
			if choice, err = p.text(label, def); err != nil {
				return err
			}
		}
		switch choice {
		case "-":
			delete(cfg.Models, role)
			continue
		case "":
			continue
		}
		m.Model = choice
		cfg.Models[role] = m
	}
	if *image != "" {
		cfg.Update.Image = *image
	} else if cfg.Update.Image, err = p.text("Container image for ax update", cfg.Update.Image); err != nil {
		return err
	}

	if err := config.WriteUser(path, cfg, time.Now()); err != nil {
		return err
	}
	changes := before.diff(snapshotUser(cfg))
	switch {
	case !exists:
		fmt.Fprintf(a.stderr, "ax: wrote %s\n", path)
	case len(changes) == 0:
		fmt.Fprintf(a.stderr, "ax: rewrote %s with no changes\n", path)
	default:
		fmt.Fprintf(a.stderr, "ax: updated %s\n", path)
		for _, c := range changes {
			fmt.Fprintf(a.stderr, "ax:   %s\n", c)
		}
	}
	if !cfg.Models[config.RoleEmbedding].Configured() || !cfg.Models[config.RoleDecision].Configured() {
		a.warnf("embedding and decision models are not both configured; ax since will not resolve contradictions")
	}
	if prov.APIKey == "" && prov.APIKeyEnv != "" && os.Getenv(prov.APIKeyEnv) == "" {
		a.warnf("$%s is not set in this environment; export it before running ax", prov.APIKeyEnv)
	}
	if *noCheck {
		return nil
	}
	return a.check(ctx, cfg)
}

// userSnapshot is the part of ax.cnf that setup edits, flattened for
// reporting what a run changed.
type userSnapshot struct {
	fields []string
	values map[string]string
}

func snapshotUser(u config.User) userSnapshot {
	s := userSnapshot{values: map[string]string{}}
	add := func(name, value string) {
		s.fields = append(s.fields, name)
		s.values[name] = value
	}
	prov := u.Providers[defaultProvider]
	add("provider base-url", prov.BaseURL)
	switch {
	case prov.APIKey != "":
		add("api key", "stored in the file")
	case prov.APIKeyEnv != "":
		add("api key", "read from $"+prov.APIKeyEnv)
	default:
		add("api key", "none")
	}
	for _, role := range config.Roles {
		m := u.Models[role]
		v := "none"
		if m.Configured() {
			v = m.Model
		}
		add(role+" model", v)
	}
	add("update image", u.Update.Image)
	return s
}

// diff lists the fields whose value differs in after, as "name: old -> new".
func (s userSnapshot) diff(after userSnapshot) []string {
	var out []string
	for _, f := range s.fields {
		if s.values[f] != after.values[f] {
			out = append(out, fmt.Sprintf("%s: %s -> %s", f, s.values[f], after.values[f]))
		}
	}
	return out
}

// check probes every configured model with a tiny request and looks for
// podman and LXD. Model failures make the command fail; a missing backend
// only disables ax update, so it is a warning.
func (a *app) check(ctx context.Context, cfg config.User) error {
	failed := 0
	report := func(name, status, detail string) {
		fmt.Fprintf(a.stdout, "%-10s %-5s %s\n", name, status, detail)
	}
	for _, role := range config.Roles {
		ep, err := cfg.Resolve(role)
		if errors.Is(err, config.ErrNotConfigured) {
			report(role, "skip", "not configured")
			continue
		}
		if err != nil {
			failed++
			report(role, "FAIL", err.Error())
			continue
		}
		start := time.Now()
		pctx, cancel := context.WithTimeout(ctx, probeTimeout)
		err = probe(pctx, ep, cfg.Retry)
		cancel()
		if err != nil {
			failed++
			report(role, "FAIL", err.Error())
			continue
		}
		report(role, "ok", fmt.Sprintf("%s (%s)", ep.Model, time.Since(start).Round(time.Millisecond)))
	}
	if out, err := exec.CommandContext(ctx, "podman", "--version").Output(); err != nil {
		report("podman", "warn", "not found")
	} else {
		report("podman", "ok", strings.TrimSpace(string(out)))
	}
	if out, err := exec.CommandContext(ctx, "lxc", "version").Output(); err != nil {
		report("lxd", "warn", "not found")
	} else {
		report("lxd", "ok", strings.Join(strings.Fields(string(out)), " "))
	}
	if backend, err := update.ResolveBackend(cfg.Update.Backend, false); err != nil {
		report("backend", "warn", err.Error()+"; ax update needs one of them")
	} else {
		report("backend", "ok", fmt.Sprintf("%s (update.backend: %s)", backend, cfg.Update.Backend))
	}
	if failed > 0 {
		return fmt.Errorf("%d model check(s) failed; the configuration was written, fix it or rerun ax setup", failed)
	}
	return nil
}

// probe makes the smallest useful request for the role.
func probe(ctx context.Context, ep config.Endpoint, retry config.Retry) error {
	c := openrouter.New(ep).WithRetry(retry, nil)
	switch ep.Role {
	case config.RoleEmbedding:
		vecs, err := c.Embed(ctx, []string{"axiomaton setup check"})
		if err != nil {
			return err
		}
		if len(vecs) == 0 || len(vecs[0]) == 0 {
			return errors.New("empty embedding returned")
		}
		return nil
	case config.RoleRerank:
		_, err := c.Rerank(ctx, "Which is a programming language?", []string{"Go", "a cup of tea"}, 1)
		return err
	case config.RoleDecision:
		_, err := c.Decide(ctx, models.Pair{
			Subject: "setup check",
			Newer:   "The default port is 8081.", NewerClock: "2",
			Older: "The default port is 8080.", OlderClock: "1",
		})
		return err
	default:
		_, err := c.Chat(ctx, "You are a connectivity check.", "Reply with the single word OK.", 64)
		return err
	}
}

// prompter reads answers from a terminal; with ask false every question
// returns its default.
type prompter struct {
	raw io.Reader
	in  *bufio.Reader
	out io.Writer
	ask bool
}

func (p prompter) line() (string, error) {
	s, err := p.in.ReadString('\n')
	if err != nil && !(errors.Is(err, io.EOF) && s != "") {
		if errors.Is(err, io.EOF) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(s), nil
}

func (p prompter) text(label, def string) (string, error) {
	if !p.ask {
		return def, nil
	}
	if def != "" {
		fmt.Fprintf(p.out, "%s [%s]: ", label, def)
	} else {
		fmt.Fprintf(p.out, "%s: ", label)
	}
	s, err := p.line()
	if err != nil || s == "" {
		return def, err
	}
	return s, nil
}

func (p prompter) yesNo(label string, def bool) (bool, error) {
	if !p.ask {
		return def, nil
	}
	hint := "y/N"
	if def {
		hint = "Y/n"
	}
	fmt.Fprintf(p.out, "%s [%s]: ", label, hint)
	s, err := p.line()
	if err != nil || s == "" {
		return def, err
	}
	return strings.HasPrefix(strings.ToLower(s), "y"), nil
}

// secret reads a line with terminal echo disabled when stdin is a real
// terminal; it falls back to a plain read when stty is unavailable.
func (p prompter) secret(label string) (string, error) {
	if !p.ask {
		return "", nil
	}
	fmt.Fprintf(p.out, "%s: ", label)
	if f, ok := p.raw.(*os.File); ok {
		if restore := echoOff(f); restore != nil {
			defer restore()
			defer fmt.Fprintln(p.out)
		}
	}
	return p.line()
}

func echoOff(f *os.File) (restore func()) {
	off := exec.Command("stty", "-echo")
	off.Stdin = f
	if off.Run() != nil {
		return nil
	}
	return func() {
		on := exec.Command("stty", "echo")
		on.Stdin = f
		_ = on.Run()
	}
}
