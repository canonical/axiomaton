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

package detect

import (
	"regexp"
	"strings"
)

var (
	tomlHeader = regexp.MustCompile(`^\[\s*([^\]]+?)\s*\]`)
	tomlPair   = regexp.MustCompile(`^([A-Za-z0-9_.-]+)\s*=\s*(.+?)\s*$`)
)

// TOMLString returns the string value of key in table, or "". It
// understands the subset of TOML these manifests use: [table] headers,
// key = "value" pairs and # comments. Dotted keys and inline tables are
// handled only as far as TOMLInherits needs.
func TOMLString(text, table, key string) string {
	section := ""
	for line := range strings.SplitSeq(text, "\n") {
		line = stripComment(line)
		if line == "" {
			continue
		}
		if m := tomlHeader.FindStringSubmatch(line); m != nil {
			section = m[1]
			continue
		}
		if section != table {
			continue
		}
		m := tomlPair.FindStringSubmatch(line)
		if m == nil || m[1] != key {
			continue
		}
		v := m[2]
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			return v[1 : len(v)-1]
		}
		return ""
	}
	return ""
}

// TOMLInherits reports whether key in table is set from the workspace,
// written either as key.workspace = true or key = { workspace = true }.
func TOMLInherits(text, table, key string) bool {
	section := ""
	for line := range strings.SplitSeq(text, "\n") {
		line = stripComment(line)
		if line == "" {
			continue
		}
		if m := tomlHeader.FindStringSubmatch(line); m != nil {
			section = m[1]
			continue
		}
		if section != table {
			continue
		}
		m := tomlPair.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		if m[1] == key+".workspace" && m[2] == "true" {
			return true
		}
		if m[1] == key && strings.HasPrefix(m[2], "{") && strings.Contains(strings.ReplaceAll(m[2], " ", ""), "workspace=true") {
			return true
		}
	}
	return false
}

// stripComment removes a trailing # comment that is outside quotes.
func stripComment(line string) string {
	inQuote := byte(0)
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case inQuote != 0:
			if c == inQuote {
				inQuote = 0
			}
		case c == '"' || c == '\'':
			inQuote = c
		case c == '#':
			return strings.TrimSpace(line[:i])
		}
	}
	return strings.TrimSpace(line)
}
