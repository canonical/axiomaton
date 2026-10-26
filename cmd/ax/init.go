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
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/canonical/go-cnf/cnfast"

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

	header := config.Lines(
		"axiomaton.cnf: the subjects this project depends on. See `ax --help`.",
		"",
		"`ax since <date>` reports what changed for every subject listed here.",
		"Built-in subjects need no clock: the project's own files are read at",
		"query time (go uses the go directive in go.mod; go:<module> uses that",
		"module's require line; rust uses the pinned toolchain or the package's",
		"rust-version; python uses .python-version or the requires-python lower",
		"bound; zig uses .zigversion or minimum_zig_version in build.zig.zon;",
		"snapcraft, charmcraft and rockcraft use the base in their yaml). Give an",
		"explicit clock, e.g.",
		"[\"go\", \"1.26.1\"], to pin a version instead. Inside a git repository the",
		"built-in subjects detected from the project's files are reported even",
		"when this list leaves them out; `ax update <subject>` builds a log.",
		"Built-in subjects: "+strings.Join(subjects.Names(), ", ")+".",
	)
	list := config.DashedList()
	for _, e := range entries {
		list.Items = append(list.Items, &cnfast.Item{Value: config.Str(e.name), InCmt: config.Comment(e.comment)})
	}
	subjectsItem := config.Pair("subjects", list, "")
	if len(entries) == 0 {
		subjectsItem.InCmt = config.Comment("e.g. [\"go\", \"1.27.0\"]")
	}
	subjectsItem.PostCmt = config.Lines(
		"Other subjects need a clock shape before clocked axioms can be added:",
		"   - {name: \"myservice\", clock: [\"2.3.0\"], shape: [{name: \"version\", kind: \"semver\"}]}",
	)
	out, err := config.Document(header, config.BlockMap(subjectsItem))
	if err != nil {
		return err
	}
	if err := os.WriteFile(cnfPath, out, 0o644); err != nil {
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
	doc, err := cnfast.Parse(data, nil)
	if err != nil {
		return fmt.Errorf("parse %s: %w", cnfPath, err)
	}
	root, ok := doc.Value.(*cnfast.Map)
	if !ok {
		if doc.Value != nil {
			return fmt.Errorf("%s is not a map; use -force to replace it", cnfPath)
		}
		root = &cnfast.Map{}
		doc.Value = root
	}
	root.Block = true
	var seq *cnfast.List
	var pair *cnfast.Item
	for _, item := range root.Items {
		if len(item.Key) != 1 || item.Key[0].Data != "subjects" {
			continue
		}
		pair = item
		switch v := item.Value.(type) {
		case *cnfast.List:
			seq = v
		case *cnfast.Scalar:
			if v.Text != "null" {
				return fmt.Errorf("%s: subjects is not a list; use -force to replace the file", cnfPath)
			}
			seq = &cnfast.List{}
			item.Value = seq
		default:
			return fmt.Errorf("%s: subjects is not a list; use -force to replace the file", cnfPath)
		}
	}
	if seq == nil {
		seq = &cnfast.List{}
		pair = &cnfast.Item{Key: []*cnfast.Str{{Data: "subjects"}}, Value: seq}
		root.Items = append(root.Items, pair)
	}
	// One entry per line with its comment; a file that wrote
	// subjects: ["go", "rust"] inline is rewritten one per line.
	if !seq.Block {
		seq.Block, seq.Dashed = true, true
		pair.Indent = config.Indent
	}

	listed := map[string]bool{}
	for _, s := range project.Subjects {
		listed[s.Name] = true
	}
	var added []initEntry
	for _, e := range entries {
		if listed[e.name] {
			continue
		}
		seq.Items = append(seq.Items, &cnfast.Item{Value: config.Str(e.name), InCmt: config.Comment(e.comment)})
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
	out, err := cnfast.Render(doc, nil)
	if err != nil {
		return err
	}
	if err := os.WriteFile(cnfPath, out, 0o644); err != nil {
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
