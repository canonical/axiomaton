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
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseInterleaved(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	s := fs.Bool("s", false, "")
	src := fs.String("source", "", "")
	pos, err := parseInterleaved(fs, []string{"2026-01-01", "golang", "-s", "1.26", "-source", "u"})
	if err != nil || !*s || *src != "u" || strings.Join(pos, ",") != "2026-01-01,golang,1.26" {
		t.Fatalf("pos=%v s=%v src=%q err=%v", pos, *s, *src, err)
	}
	fs = flag.NewFlagSet("t", flag.ContinueOnError)
	s = fs.Bool("s", false, "")
	pos, err = parseInterleaved(fs, []string{"a", "--", "-s", "b"})
	if err != nil || *s || strings.Join(pos, ",") != "a,-s,b" {
		t.Fatalf("terminator: pos=%v s=%v err=%v", pos, *s, err)
	}
}

func TestCLI(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "cfg"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	repo := filepath.Join(root, "repo")
	os.MkdirAll(filepath.Join(repo, ".git"), 0o755)
	os.MkdirAll(filepath.Join(repo, "sub"), 0o755)
	os.WriteFile(filepath.Join(repo, "axiomaton.cnf"), []byte("subjects:\n  - [golang, 1.26.1]\n  - name: svc\n    clock: [2.3.0]\n    shape: [{name: version, kind: semver}]\n"), 0o644)

	exec := func(args ...string) (string, string, error) {
		var out, errb strings.Builder
		err := run(context.Background(), append([]string{"-C", filepath.Join(repo, "sub"), "--offline"}, args...), &out, &errb)
		return out.String(), errb.String(), err
	}

	out, _, err := exec("since", "2025-08-01", "golang", "1.26.1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "## golang (version <= 1.26.1)") || !strings.Contains(out, "new(expr)") || strings.Contains(out, "1.27.0") {
		t.Fatalf("since output:\n%s", out)
	}
	// Log order, not hash order, within a release.
	if strings.Index(out, "no longer uses the notion of core types") > strings.Index(out, "testing/synctest package is generally available") {
		t.Fatalf("expected log order within 1.25.0:\n%s", out)
	}

	out, _, err = exec("since", "2026-01-01")
	if err != nil || !strings.Contains(out, "## golang") || !strings.Contains(out, "## svc") {
		t.Fatalf("default subjects: %v\n%s", err, out)
	}

	_, errOut, err := exec("add", "2026-09-01", "svc", "2.4.0", "svc exposes a v2 API", "-source", "https://example.com")
	if err != nil || !strings.Contains(errOut, "added") {
		t.Fatalf("add: %v %s", err, errOut)
	}
	data, _ := os.ReadFile(filepath.Join(repo, "axiomlog"))
	if !strings.Contains(string(data), `"source":"https://example.com"`) {
		t.Fatalf("log: %s", data)
	}
	_, errOut, err = exec("add", "2026-09-01", "svc", "2.4.0", "svc exposes a v2 API")
	if err != nil || !strings.Contains(errOut, "already recorded") {
		t.Fatalf("duplicate add: %v %s", err, errOut)
	}
	if _, _, err = exec("add", "2026-09-01", "rust", "1.80", "x"); err == nil || !strings.Contains(err.Error(), "no declared clock shape") {
		t.Fatalf("undeclared subject add should fail: %v", err)
	}
	if _, _, err = exec("add", "2026-09-01", "svc", "banana", "x"); err == nil {
		t.Fatal("bad clock should fail")
	}

	out, _, err = exec("since", "2026-01-01", "svc", "2.4.0")
	if err != nil || !strings.Contains(out, "svc exposes a v2 API") {
		t.Fatalf("since svc: %v\n%s", err, out)
	}
	out, _, _ = exec("since", "2026-01-01", "svc", "2.3.0")
	if strings.Contains(out, "svc exposes a v2 API") {
		t.Fatalf("2.4.0 axiom must not apply to 2.3.0:\n%s", out)
	}

	out, errOut, err = exec("subjects")
	if err != nil || !strings.Contains(out, "golang\tshape [version:goversion]") || !strings.Contains(out, "svc\tshape [version:semver]\t1 axiom(s)\tdefault @ 2.3.0") {
		t.Fatalf("subjects: %v\n%s\n%s", err, out, errOut)
	}

	_, errOut, err = exec("since", "2026-01-01", "golang", "-s")
	if err == nil || !strings.Contains(err.Error(), "summary") {
		t.Fatalf("-s without a summary model should fail clearly: %v", err)
	}

	var out2, err2 strings.Builder
	if err := run(context.Background(), []string{"-C", root, "--offline", "since", "2026-01-01"}, &out2, &err2); err != nil || out2.Len() != 0 || !strings.Contains(err2.String(), "no default subjects") {
		t.Fatalf("no defaults: %v out=%q err=%q", err, out2.String(), err2.String())
	}
	if err := run(context.Background(), []string{"bogus"}, &out2, &err2); err == nil {
		t.Fatal("unknown command should fail")
	}
	if _, _, err := exec("update", "rust"); err == nil || !strings.Contains(err.Error(), "built-in") {
		t.Fatalf("update rust: %v", err)
	}
}
