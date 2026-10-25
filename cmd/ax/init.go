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
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/canonical/axiomaton/internal/config"
	"github.com/canonical/axiomaton/internal/subjects"
)

// init writes an axiomaton.cnf (and an empty axiomlog) for the working
// directory, listing the built-in subjects detected there. Detected subjects
// are written without a clock so the project's version is read from its
// own files, such as go.mod, at query time.
func (a *app) init(_ context.Context, args []string) error {
	fs := flag.NewFlagSet("ax init", flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	force := fs.Bool("force", false, "replace an existing axiomaton.cnf")
	fs.Usage = func() {
		fmt.Fprintln(a.stderr, "usage: ax init [-force] [built-in subject...]")
		fs.PrintDefaults()
	}
	rest, err := parseInterleaved(fs, args)
	if err != nil {
		return err
	}

	cnfPath := filepath.Join(a.dir, config.ProjectFile)
	if _, err := os.Stat(cnfPath); err == nil && !*force {
		return fmt.Errorf("%s already exists; use -force to replace it", cnfPath)
	}

	type entry struct {
		name    string
		comment string
		summary string
	}
	var entries []entry
	seen := map[string]bool{}
	for _, d := range subjects.Detect(a.dir) {
		seen[d.Subject.Name()] = true
		src := shortenFrom(a.dir, d.Source)
		e := entry{
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
		entries = append(entries, entry{name: name, comment: "requested; clock is detected at query time when possible", summary: name})
	}

	var b bytes.Buffer
	b.WriteString("# axiomaton.cnf: the subjects this project depends on. See `ax --help`.\n")
	b.WriteString("#\n")
	b.WriteString("# `ax since <date>` reports what changed for every subject listed here.\n")
	b.WriteString("# Built-in subjects need no clock: the project's own files are read at\n")
	b.WriteString("# query time (go uses the go directive in go.mod; go:<module> uses that\n")
	b.WriteString("# module's require line; rust uses the pinned toolchain or the package's\n")
	b.WriteString("# rust-version). Give an explicit clock, e.g.\n")
	b.WriteString("# [go, 1.26.1], to pin a version instead. Remove any dependency you\n")
	b.WriteString("# do not want reported on; `ax update <subject>` builds its log.\n")
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

	logPath := filepath.Join(a.dir, config.LogFile)
	if _, err := os.Stat(logPath); errors.Is(err, os.ErrNotExist) {
		header := "# axiomlog: one JSON object per line, appended by `ax add` and `ax update`.\n"
		if err := os.WriteFile(logPath, []byte(header), 0o644); err != nil {
			return err
		}
		fmt.Fprintf(a.stderr, "ax: created %s\n", logPath)
	}

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
