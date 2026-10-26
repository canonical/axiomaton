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
	"bytes"
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/canonical/axiomaton/internal/config"
	"github.com/canonical/axiomaton/internal/subjects"
)

// initEntry is one subject init adds to the project file.
type initEntry struct {
	name    string
	comment string
	summary string
}

// init writes an axiomaton.cnf for the working directory, listing the
// built-in subjects detected there. No axiomlog is written: ax add and ax
// update create it beside the file on first use. Detected subjects
// are written without a clock so the project's version is read from its
// own files, such as go.mod, at query time. An existing file is updated in
// place: subjects detected or requested that it does not list yet are
// appended and everything else, including hand-written subjects and
// comments, is kept; -force replaces it instead.
func (a *app) init(_ context.Context, args []string) error {
	fs := flag.NewFlagSet("ax init", flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	force := fs.Bool("force", false, "replace an existing axiomaton.cnf instead of adding to it")
	fs.Usage = func() {
		fmt.Fprintln(a.stderr, "usage: ax init [-force] [built-in subject...]")
		fs.PrintDefaults()
	}
	rest, err := parseInterleaved(fs, args)
	if err != nil {
		return err
	}

	cnfPath := filepath.Join(a.dir, config.ProjectFile)
	_, statErr := os.Stat(cnfPath)
	exists := statErr == nil

	var entries []initEntry
	seen := map[string]bool{}
	for _, d := range subjects.Detect(a.dir) {
		seen[d.Subject.Name()] = true
		src := shortenFrom(a.dir, d.Source)
		e := initEntry{
			name:    d.Subject.Name(),
			comment: fmt.Sprintf("detected: %s says %s", src, strings.Join(d.Clock, " ")),
			summary: fmt.Sprintf("%s @ %s (from %s)", d.Subject.Name(), strings.Join(d.Clock, " "), src),
		}
		if len(d.Clock) == 0 {
			e.comment = fmt.Sprintf("detected from %s; no version pinned, so every release is reported", src)
			e.summary = fmt.Sprintf("%s (from %s; no version pinned)", d.Subject.Name(), src)
		}
		entries = append(entries, e)
	}
	for _, name := range rest {
		if seen[name] {
			continue
		}
		if _, ok := subjects.Lookup(name); !ok {
			return fmt.Errorf("%q is not a built-in subject (%s); declare it by hand with a clock shape in %s", name, strings.Join(subjects.Names(), ", "), config.ProjectFile)
		}
		seen[name] = true
		entries = append(entries, initEntry{name: name, comment: "requested; clock is detected at query time when possible", summary: name})
	}

	if exists && !*force {
		if err := a.updateProject(cnfPath, entries, rest); err != nil {
			return err
		}
		return a.initTail(cnfPath)
	}

	var b bytes.Buffer
	b.WriteString("# axiomaton.cnf: the subjects this project depends on. See `ax --help`.\n")
	b.WriteString("#\n")
	b.WriteString("# `ax since <date>` reports what changed for every subject listed here.\n")
	b.WriteString("# Built-in subjects need no clock: the project's own files are read at\n")
	b.WriteString("# query time (go uses the go directive in go.mod; go:<module> uses that\n")
	b.WriteString("# module's require line; rust uses the pinned toolchain or the package's\n")
	b.WriteString("# rust-version; python uses .python-version or the requires-python lower\n")
	b.WriteString("# bound; zig uses .zigversion or minimum_zig_version in build.zig.zon;\n")
	b.WriteString("# snapcraft, charmcraft and rockcraft use the base in their yaml). Give an\n")
	b.WriteString("# explicit clock, e.g.\n")
	b.WriteString("# [go, 1.26.1], to pin a version instead. Inside a git repository the\n")
	b.WriteString("# built-in subjects detected from the project's files are reported even\n")
	b.WriteString("# when this list leaves them out; `ax update <subject>` builds a log.\n")
	b.WriteString("# Built-in subjects: " + strings.Join(subjects.Names(), ", ") + ".\n")
	b.WriteString("subjects:\n")
	if len(entries) == 0 {
		b.WriteString("  # - [go, 1.27.0]\n")
	}
	for _, e := range entries {
		fmt.Fprintf(&b, "  - %s  # %s\n", e.name, e.comment)
	}
	b.WriteString("# Other subjects need a clock shape before clocked axioms can be added:\n")
	b.WriteString("#  - name: myservice\n")
	b.WriteString("#    clock: [2.3.0]\n")
	b.WriteString("#    shape: [{name: version, kind: semver}]\n")
	if err := os.WriteFile(cnfPath, b.Bytes(), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(a.stderr, "ax: wrote %s\n", cnfPath)
	if len(entries) == 0 {
		a.warnf("no built-in subject detected in %s; edit the subjects list by hand", a.dir)
	}
	for _, e := range entries {
		fmt.Fprintf(a.stderr, "ax:   %s\n", e.summary)
	}
	return a.initTail(cnfPath)
}

// updateProject adds the entries that cnfPath does not list yet to its
// subjects, keeping the rest of the file as it is. requested names the
// subjects given on the command line, which are never reported as stale.
func (a *app) updateProject(cnfPath string, entries []initEntry, requested []string) error {
	project, err := config.LoadProject(cnfPath)
	if err != nil {
		return fmt.Errorf("%w; fix the file or use -force to replace it", err)
	}
	data, err := os.ReadFile(cnfPath)
	if err != nil {
		return err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("parse %s: %w", cnfPath, err)
	}
	if doc.Kind == 0 || len(doc.Content) == 0 {
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return fmt.Errorf("%s is not a YAML mapping; use -force to replace it", cnfPath)
	}
	var seq *yaml.Node
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value != "subjects" {
			continue
		}
		v := root.Content[i+1]
		switch {
		case v.Kind == yaml.SequenceNode:
			seq = v
		case v.Kind == yaml.ScalarNode && v.Tag == "!!null":
			*v = yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
			seq = v
		default:
			return fmt.Errorf("%s: subjects is not a list; use -force to replace the file", cnfPath)
		}
	}
	if seq == nil {
		seq = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "subjects"}, seq)
	}
	// Block style keeps one entry per line with its comment; a file that
	// wrote subjects: [go, rust] inline is rewritten one per line.
	seq.Style = 0

	listed := map[string]bool{}
	for _, s := range project.Subjects {
		listed[s.Name] = true
	}
	var added []initEntry
	for _, e := range entries {
		if listed[e.name] {
			continue
		}
		seq.Content = append(seq.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: e.name, LineComment: "# " + e.comment})
		added = append(added, e)
	}

	// Built-in subjects the file lists without a clock that nothing here
	// detects any more report every release; say so.
	current := map[string]bool{}
	for _, e := range entries {
		current[e.name] = true
	}
	for _, r := range requested {
		current[r] = true
	}
	for _, s := range project.Subjects {
		if _, builtin := subjects.Lookup(s.Name); builtin && len(s.Clock) == 0 && !current[s.Name] {
			a.warnf("%s lists %s, which is not detected in %s any more; remove it or pin a clock", shortenFrom(a.dir, cnfPath), s.Name, a.dir)
		}
	}

	if len(added) == 0 {
		fmt.Fprintf(a.stderr, "ax: %s is up to date (%d subject(s) listed); nothing written\n", cnfPath, len(project.Subjects))
		return nil
	}
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}
	if err := os.WriteFile(cnfPath, b.Bytes(), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(a.stderr, "ax: updated %s\n", cnfPath)
	for _, e := range added {
		fmt.Fprintf(a.stderr, "ax:   + %s\n", e.summary)
	}
	return nil
}

// initTail points at ax setup when there is no user configuration yet.
func (a *app) initTail(cnfPath string) error {
	if cfg, err := config.LoadUser(a.configPath); err == nil && cfg.Path == "" {
		a.warnf("no %s found; run `ax setup` to configure models", a.configPath)
	}
	return nil
}

// shortenFrom shows path relative to dir when it is inside it or a parent.
func shortenFrom(dir, path string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return path
	}
	if rel, err := filepath.Rel(abs, path); err == nil && len(rel) < len(path) {
		return rel
	}
	return path
}
