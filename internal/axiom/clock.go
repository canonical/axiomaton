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

package axiom

import (
	"context"
	"fmt"
	"go/version"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/mod/semver"
)

// Kind is the type of an ordered sub-clock dimension.
type Kind string

const (
	// KindSemver orders values as semantic versions ("1.2.3", "v1.2.3").
	KindSemver Kind = "semver"
	// KindGoVersion orders values as Go toolchain versions ("1.26", "1.27rc1").
	KindGoVersion Kind = "goversion"
	// KindNumber orders values as decimal numbers.
	KindNumber Kind = "number"
	// KindString orders values lexically.
	KindString Kind = "string"
	// KindGit orders commit hashes by ancestry within a repository.
	KindGit Kind = "git"
)

// Ordering is the result of comparing two sub-clock values.
type Ordering int

const (
	Less Ordering = iota - 1
	Equal
	Greater
	// Incomparable means neither value precedes the other, e.g. two git
	// commits on divergent branches.
	Incomparable
)

func (o Ordering) String() string {
	switch o {
	case Less:
		return "less"
	case Equal:
		return "equal"
	case Greater:
		return "greater"
	}
	return "incomparable"
}

// ClockShape describes one dimension of a subject's sub-clock.
type ClockShape struct {
	Name string `json:"name"`
	Kind Kind   `json:"kind"`
	// Repo is the git repository used to order KindGit values.
	Repo string `json:"repo,omitempty"`
}

// Validate checks the shape is usable.
func (d ClockShape) Validate() error {
	if d.Name == "" {
		return fmt.Errorf("clock dimension has no name")
	}
	switch d.Kind {
	case KindSemver, KindGoVersion, KindNumber, KindString:
	case KindGit:
		if d.Repo == "" {
			return fmt.Errorf("clock dimension %q of kind git needs a repo", d.Name)
		}
	default:
		return fmt.Errorf("clock dimension %q has unknown kind %q", d.Name, d.Kind)
	}
	return nil
}

// Parse checks that a value is valid for the dimension's kind and returns
// its canonical form.
func (d ClockShape) Parse(v string) (string, error) {
	v = strings.TrimSpace(v)
	switch d.Kind {
	case KindSemver:
		c := canonSemver(v)
		if !semver.IsValid(c) {
			return "", fmt.Errorf("%q is not a semantic version", v)
		}
		return strings.TrimPrefix(c, "v"), nil
	case KindGoVersion:
		c := canonGoVersion(v)
		if !version.IsValid(c) {
			return "", fmt.Errorf("%q is not a Go version", v)
		}
		return strings.TrimPrefix(c, "go"), nil
	case KindNumber:
		if _, err := strconv.ParseFloat(v, 64); err != nil {
			return "", fmt.Errorf("%q is not a number", v)
		}
		return v, nil
	case KindGit:
		if v == "" {
			return "", fmt.Errorf("empty git commit")
		}
		return v, nil
	}
	return v, nil
}

// Compare orders two values of this dimension.
func (d ClockShape) Compare(a, b string) Ordering {
	switch d.Kind {
	case KindSemver:
		return fromInt(semver.Compare(canonSemver(a), canonSemver(b)))
	case KindGoVersion:
		return fromInt(version.Compare(canonGoVersion(a), canonGoVersion(b)))
	case KindNumber:
		af, aerr := strconv.ParseFloat(a, 64)
		bf, berr := strconv.ParseFloat(b, 64)
		if aerr != nil || berr != nil {
			return fromInt(strings.Compare(a, b))
		}
		switch {
		case af < bf:
			return Less
		case af > bf:
			return Greater
		}
		return Equal
	case KindGit:
		return compareGit(d.Repo, a, b)
	}
	return fromInt(strings.Compare(a, b))
}

func fromInt(c int) Ordering {
	switch {
	case c < 0:
		return Less
	case c > 0:
		return Greater
	}
	return Equal
}

func canonSemver(v string) string {
	v = strings.TrimSpace(v)
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	return v
}

// canonGoVersion normalises a Go version. A bare language version such as
// "1.27" is treated as the release "1.27.0" so that queries for "1.27"
// include the 1.27.0 release, which go/version would otherwise order after
// the language version.
func canonGoVersion(v string) string {
	v = strings.TrimSpace(v)
	if !strings.HasPrefix(v, "go") {
		v = "go" + v
	}
	if bareGoVersion.MatchString(v) {
		v += ".0"
	}
	return v
}

var bareGoVersion = regexp.MustCompile(`^go[0-9]+\.[0-9]+$`)

// GitAncestor reports whether commit a is an ancestor of (or equal to)
// commit b in repo. It is a variable so tests can replace it.
var GitAncestor = func(repo, a, b string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", repo, "merge-base", "--is-ancestor", a, b)
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("git merge-base in %s: %w", repo, err)
}

var (
	gitMemoMu sync.Mutex
	gitMemo   = map[string]Ordering{}
)

func compareGit(repo, a, b string) Ordering {
	if a == b {
		return Equal
	}
	key := repo + "\x00" + a + "\x00" + b
	gitMemoMu.Lock()
	if o, ok := gitMemo[key]; ok {
		gitMemoMu.Unlock()
		return o
	}
	gitMemoMu.Unlock()

	o := Incomparable
	if anc, err := GitAncestor(repo, a, b); err == nil && anc {
		o = Less
	} else if anc, err := GitAncestor(repo, b, a); err == nil && anc {
		o = Greater
	}
	gitMemoMu.Lock()
	gitMemo[key] = o
	gitMemoMu.Unlock()
	return o
}

// InferShape guesses a clock shape for positional values when a subject has
// no declared shape. Dimensions are named "c1", "c2", ...
func InferShape(values []string) []ClockShape {
	shape := make([]ClockShape, len(values))
	for i, v := range values {
		d := ClockShape{Name: fmt.Sprintf("c%d", i+1), Kind: KindString}
		if _, err := strconv.ParseFloat(v, 64); err == nil {
			d.Kind = KindNumber
		} else if semver.IsValid(canonSemver(v)) {
			d.Kind = KindSemver
		}
		shape[i] = d
	}
	return shape
}
