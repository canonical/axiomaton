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
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/canonical/axiomaton/internal/config"
	"github.com/canonical/axiomaton/internal/subjects/detect"
)

// SyncHeader opens the block ax sync owns in a context file and SyncFooter
// closes it. Whatever is between them is replaced on every run; the rest of
// the file is the user's.
const (
	SyncHeader = "# Axiom changes"
	SyncFooter = "**end of axiom changes**"
)

// agentFiles maps the agents ax sync knows to the context file each reads
// from a project directory. These are the files agents treat as personal
// to the checkout, which is why they must stay out of git.
var agentFiles = []struct{ agent, file string }{
	{"claude", "CLAUDE.local.md"},
	{"codex", "AGENTS.override.md"},
	{"gemini", "GEMINI.md"},
	{"copilot", ".github/copilot-instructions.md"},
}

// agentFile resolves an agent name to its context file; anything else is
// taken as a path.
func agentFile(answer string) string {
	for _, a := range agentFiles {
		if strings.EqualFold(answer, a.agent) {
			return a.file
		}
	}
	return answer
}

func agentChoices() string {
	var parts []string
	for _, a := range agentFiles {
		parts = append(parts, a.agent+" = "+a.file)
	}
	for _, h := range harnesses {
		parts = append(parts, h.name+" = "+h.kind)
	}
	return strings.Join(parts, ", ")
}

func harnessNames() string {
	var names []string
	for _, h := range harnesses {
		names = append(names, h.name)
	}
	return strings.Join(names, " and ")
}

// sync writes the since or for report for this project into an agent
// context file, or installs an extension that fetches it for a harness,
// remembering the cutoff and the target in ax.cnf. With -print it only
// writes the report to standard output, for those extensions.
func (a *app) sync(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("ax sync", flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	summarise := fs.Bool("s", false, "summarise the report with the summary model")
	fs.BoolVar(summarise, "summarise", false, "alias for -s")
	refresh := fs.Bool("refresh", false, "fetch the model catalogue again instead of using the cached copy")
	yes := fs.Bool("y", false, "accept defaults without prompting, and install or replace a harness extension without asking")
	file := fs.String("file", "", "context file to update, relative to the project (remembered)")
	agent := fs.String("agent", "", "agent to keep current: "+agentChoices()+" (remembered)")
	global := fs.Bool("global", false, "install the "+harnessNames()+" extension for every project (remembered)")
	local := fs.Bool("project", false, "install the "+harnessNames()+" extension in this project, the default (remembered)")
	print := fs.Bool("print", false, "write the report for this project to standard output and change nothing; the "+harnessNames()+" extensions run this")
	model := fs.String("model", "", "with -print, the model the harness is running as provider/id; its knowledge cutoff is used when OpenRouter publishes it")
	fs.Usage = func() {
		fmt.Fprintln(a.stderr, "usage: ax sync [-s] [-refresh] [-y] [-file path | -agent name [-global | -project]] [model | date]")
		fmt.Fprintln(a.stderr, "       ax sync -print [-s] [-refresh] [-model provider/id]")
		fs.PrintDefaults()
		fmt.Fprintln(a.stderr, "The model (its knowledge cutoff) or date and the agent are asked for once per project and stored in ax.cnf; later runs take no arguments. A model or date given again replaces the stored one. For "+harnessNames()+" an extension is installed, after asking, that adds the report to the system prompt as the agent starts.")
	}
	rest, err := parseInterleaved(fs, args)
	if err != nil {
		return err
	}
	if len(rest) > 1 {
		fs.Usage()
		return fmt.Errorf("sync: unexpected argument %q", rest[1])
	}
	if *file != "" && *agent != "" {
		return errors.New("sync: -file and -agent are mutually exclusive")
	}
	if *global && *local {
		return errors.New("sync: -global and -project are mutually exclusive")
	}
	if *model != "" && !*print {
		return errors.New("sync: -model goes with -print; give the model to report since as the argument")
	}

	cfg, err := config.LoadUser(a.configPath)
	if err != nil {
		return err
	}
	project, err := filepath.Abs(a.dir)
	if err != nil {
		return err
	}
	if *print {
		if len(rest) > 0 || *file != "" || *agent != "" || *global || *local {
			return errors.New("sync: -print takes only -s, -refresh and -model")
		}
		return a.syncPrint(ctx, cfg, project, *model, *summarise, *refresh)
	}
	entry := cfg.Sync.Projects[project]
	before := entry
	p := prompter{raw: stdin, in: bufio.NewReader(stdin), out: a.stderr, ask: !*yes && interactive()}

	// The cutoff: the argument, else what this project used before, else
	// the configured default, else ask.
	cutoff := ""
	if len(rest) == 1 {
		cutoff = rest[0]
	} else if v, _, ok := entry.Cutoff(); ok {
		cutoff = v
	} else if v, _, ok := (config.SyncProject{Model: cfg.Sync.Model, Since: cfg.Sync.Since}).Cutoff(); ok {
		cutoff = v
		a.logf("using the sync default from %s: %s", a.configPath, v)
	} else if p.ask {
		cutoff, err = p.text("Report changes since: an OpenRouter model id (its knowledge cutoff) or a date, YYYY-MM-DD", "")
		if err != nil {
			return err
		}
	}
	cutoff = strings.TrimSpace(cutoff)
	if cutoff == "" {
		return fmt.Errorf("sync: no cutoff for %s; give a model id or a date (e.g. ax sync %s), or set sync.model or sync.since in %s", project, "openai/gpt-6-luna-pro", a.configPath)
	}
	if t, err := parseDate(cutoff); err == nil {
		entry.Since, entry.Model = t.Format("2006-01-02"), ""
	} else {
		entry.Model, entry.Since = cutoff, ""
	}

	// The target: the flag, else what this project used before, else the
	// configured default, else ask. A harness gets an extension, anything
	// else a context file.
	target := *file
	var h harness
	isHarness := false
	if *agent != "" {
		if h, isHarness = findHarness(*agent); !isHarness {
			target = agentFile(*agent)
			if target == *agent {
				return fmt.Errorf("sync: unknown agent %q; one of %s, or use -file", *agent, agentChoices())
			}
		}
	}
	if target == "" && !isHarness {
		switch {
		case entry.Harness != "":
			h, isHarness = findHarness(entry.Harness)
		case entry.File != "":
			target = entry.File
		case cfg.Sync.Harness != "":
			h, isHarness = findHarness(cfg.Sync.Harness)
		case cfg.Sync.File != "":
			target = cfg.Sync.File
		case p.ask:
			answer, err := p.text("Agent to keep current ("+agentChoices()+", or a path)", "claude")
			if err != nil {
				return err
			}
			answer = strings.TrimSpace(answer)
			if h, isHarness = findHarness(answer); !isHarness {
				target = agentFile(answer)
			}
		}
	}
	if (*global || *local) && !isHarness {
		return fmt.Errorf("sync: -global and -project choose where the %s extension goes; they do not apply to a context file", harnessNames())
	}
	if target == "" && !isHarness {
		return fmt.Errorf("sync: no context file for %s; give -agent (%s) or -file, or set sync.file or sync.harness in %s", project, agentChoices(), a.configPath)
	}
	if isHarness {
		entry.File, entry.Harness = "", h.name
		switch {
		case *global:
			entry.Global = true
		case *local:
			entry.Global = false
		case before.Harness == h.name:
			// Where it went last time.
		default:
			entry.Global, err = a.askScope(h, p)
			if err != nil {
				return err
			}
		}
	} else {
		entry.File, entry.Harness, entry.Global = target, "", false
	}

	remember := func() error {
		if entry == before {
			return nil
		}
		if cfg.Sync.Projects == nil {
			cfg.Sync.Projects = map[string]config.SyncProject{}
		}
		cfg.Sync.Projects[project] = entry
		if err := config.WriteUser(a.configPath, cfg, time.Now()); err != nil {
			return fmt.Errorf("remembering the sync settings: %w", err)
		}
		a.logf("remembered sync settings for %s in %s", project, a.configPath)
		return nil
	}

	var path string
	if isHarness {
		if path, err = h.path(project, entry.Global); err != nil {
			return err
		}
	} else {
		if err := remember(); err != nil {
			return err
		}
		path = target
		if !filepath.IsAbs(path) {
			path = filepath.Join(project, path)
		}
		a.warnIfCommitted(ctx, path)
	}

	var since time.Time
	if entry.Since != "" {
		since, _ = parseDate(entry.Since)
	} else if since, err = a.modelCutoff(ctx, cfg, entry.Model, *refresh); err != nil {
		return err
	}
	md, err := a.changes(ctx, since, nil, *summarise)
	if err != nil {
		return err
	}
	source := "since " + since.Format("2006-01-02")
	if entry.Model != "" {
		source = "since the knowledge cutoff of " + entry.Model + ", " + since.Format("2006-01-02")
	}

	if isHarness {
		// The report is rendered above only to check that the extension
		// will have something to say; it renders its own each session.
		ok, err := a.installHarness(ctx, h, path, entry.Global, *yes, p)
		if err != nil || !ok {
			return err
		}
		if err := remember(); err != nil {
			return err
		}
		if md == "" {
			a.warnf("there are no axiom changes for %s yet (%s); %s will add them once there are", project, source, h.name)
			return nil
		}
		fmt.Fprintf(a.stderr, "ax: %s will add %d lines of axiom changes to its system prompt (%s, or since the cutoff of its active model when OpenRouter publishes it)\n", h.name, strings.Count(md, "\n"), source)
		return nil
	}

	if md == "" {
		return fmt.Errorf("sync: nothing to write for %s", project)
	}
	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	updated, replaced := spliceSync(string(existing), md)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		return err
	}
	verb := "appended the axiom changes to"
	if replaced {
		verb = "replaced the axiom changes in"
	}
	fmt.Fprintf(a.stderr, "ax: %s %s (%s)\n", verb, shortenFrom(a.dir, path), source)
	return nil
}

// askScope asks whether the harness extension goes in the project or in
// the user's configuration for every project. A global copy already in
// place answers it: a project copy would only duplicate it.
func (a *app) askScope(h harness, p prompter) (bool, error) {
	if gp, err := h.global(); err == nil {
		if _, err := os.Stat(gp); err == nil {
			fmt.Fprintf(a.stderr, "ax: the global %s %s at %s already serves this project\n", h.name, h.kind, shorten(gp))
			return true, nil
		}
	}
	for {
		answer, err := p.text(fmt.Sprintf("Install the %s %s for this project or for every project (project, global)", h.name, h.kind), "project")
		if err != nil {
			return false, err
		}
		switch strings.ToLower(strings.TrimSpace(answer)) {
		case "p", "project":
			return false, nil
		case "g", "global":
			return true, nil
		}
		fmt.Fprintln(p.out, "Please answer project or global.")
	}
}

// spliceSync replaces the block between SyncHeader and SyncFooter in doc
// with block, or appends it when doc has no block yet. The block always ends
// with the footer so the next run finds its bounds. replaced reports which
// happened.
func spliceSync(doc, block string) (out string, replaced bool) {
	block = strings.TrimRight(block, "\n") + "\n" + SyncFooter + "\n"
	lines := strings.SplitAfter(doc, "\n")
	start, end := -1, -1
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if start < 0 && t == SyncHeader {
			start = i
			continue
		}
		if start >= 0 && t == SyncFooter {
			end = i
			break
		}
	}
	if start < 0 {
		if doc == "" {
			return block, false
		}
		// One blank line between the user's text and the block.
		sep := ""
		switch {
		case !strings.HasSuffix(doc, "\n"):
			sep = "\n\n"
		case !strings.HasSuffix(doc, "\n\n"):
			sep = "\n"
		}
		return doc + sep + block, false
	}
	if end < 0 {
		// A heading without a footer: the block runs to the end of file.
		end = len(lines) - 1
	}
	var b strings.Builder
	for _, l := range lines[:start] {
		b.WriteString(l)
	}
	b.WriteString(block)
	for _, l := range lines[end+1:] {
		b.WriteString(l)
	}
	return b.String(), true
}

// warnIfCommitted warns when git would commit the context file: inside a
// repository it should be ignored and not already tracked. The file is
// written either way; .gitignore is never touched.
func (a *app) warnIfCommitted(ctx context.Context, path string) {
	root, ok := repoRoot(filepath.Dir(path))
	if !ok {
		return
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return
	}
	rel = filepath.ToSlash(rel)
	switch {
	case tracked(ctx, root, rel):
		a.warnf("%s is tracked by git in %s; an agent context file should not be committed. Run `git rm --cached %s` and add it to .gitignore", rel, root, rel)
	case !ignored(ctx, root, rel):
		a.warnf("%s is inside the git repository at %s and not ignored; add /%s to %s so the agent context is not committed", rel, root, rel, filepath.Join(root, ".gitignore"))
	}
}

// repoRoot finds the nearest enclosing directory with a .git entry.
func repoRoot(dir string) (string, bool) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", false
	}
	for {
		if detect.RepoRoot(dir) {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// tracked reports whether git has rel in the index of the repository at
// root. Without a working git it reports false.
func tracked(ctx context.Context, root, rel string) bool {
	err := exec.CommandContext(ctx, "git", "-C", root, "ls-files", "--error-unmatch", "--", rel).Run()
	return err == nil
}

// ignored reports whether git ignores rel in the repository at root. When
// git is unavailable or root is not a usable repository the root .gitignore
// is scanned for the path itself.
func ignored(ctx context.Context, root, rel string) bool {
	cmd := exec.CommandContext(ctx, "git", "-C", root, "check-ignore", "-q", "--", rel)
	if err := cmd.Run(); err == nil {
		return true
	} else if ee := new(exec.ExitError); errors.As(err, &ee) && ee.ExitCode() == 1 {
		return false
	}
	data, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		return false
	}
	base := rel[strings.LastIndex(rel, "/")+1:]
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		switch line {
		case rel, "/" + rel, base, "/" + base:
			return true
		}
	}
	return false
}
