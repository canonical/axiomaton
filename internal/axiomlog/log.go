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

// Package axiomlog reads and appends the JSON lines axiomlog files and
// discovers the logs and configuration that apply to a directory.
package axiomlog

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/canonical/axiomaton/internal/axiom"
)

// Read parses an axiomlog from r. Blank lines and lines starting with '#'
// are ignored. origin is recorded on each axiom.
func Read(r io.Reader, origin string) ([]axiom.Axiom, error) {
	var out []axiom.Axiom
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	line := 0
	for sc.Scan() {
		line++
		raw := bytes.TrimSpace(sc.Bytes())
		if len(raw) == 0 || raw[0] == '#' {
			continue
		}
		var a axiom.Axiom
		if err := json.Unmarshal(raw, &a); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", origin, line, err)
		}
		if err := a.Validate(); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", origin, line, err)
		}
		a.Normalise()
		a.Origin = origin
		out = append(out, a)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("%s: %w", origin, err)
	}
	return out, nil
}

// ReadFile parses the axiomlog at path. A missing file yields no axioms.
func ReadFile(path string) ([]axiom.Axiom, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return Read(f, path)
}

// Append writes axioms to the end of the log at path, creating the file and
// its directory as needed.
func Append(path string, axioms ...axiom.Axiom) error {
	if len(axioms) == 0 {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	// Make sure we start on a fresh line even if the file was hand edited.
	if info, err := f.Stat(); err == nil && info.Size() > 0 {
		tail := make([]byte, 1)
		if rf, err := os.Open(path); err == nil {
			rf.ReadAt(tail, info.Size()-1)
			rf.Close()
		}
		if tail[0] != '\n' {
			if _, err := f.Write([]byte{'\n'}); err != nil {
				return err
			}
		}
	}
	w := bufio.NewWriter(f)
	for _, a := range axioms {
		a.Normalise()
		if err := a.Validate(); err != nil {
			return err
		}
		b, err := json.Marshal(a)
		if err != nil {
			return err
		}
		w.Write(b)
		w.WriteByte('\n')
	}
	return w.Flush()
}

// Merge combines several logs, dropping exact duplicates by ID. Later logs
// win for the Origin of duplicate axioms so project logs shadow built-ins.
// Each axiom's Seq records its position in the merged order.
func Merge(logs ...[]axiom.Axiom) []axiom.Axiom {
	seen := map[string]int{}
	var out []axiom.Axiom
	for _, log := range logs {
		for _, a := range log {
			if i, ok := seen[a.ID]; ok {
				a.Seq = i
				out[i] = a
				continue
			}
			a.Seq = len(out)
			seen[a.ID] = len(out)
			out = append(out, a)
		}
	}
	return out
}

// Filter returns the axioms for one subject.
func Filter(axioms []axiom.Axiom, subject string) []axiom.Axiom {
	var out []axiom.Axiom
	for _, a := range axioms {
		if strings.EqualFold(a.Subject, subject) {
			out = append(out, a)
		}
	}
	return out
}
