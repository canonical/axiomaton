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

import "testing"

func TestTOML(t *testing.T) {
	text := "title = \"x # not a comment\"\n[package]\nname = 'q'\nrust-version = \"1.70\" # trailing\n[other]\nrust-version = \"9.9\"\n[tool.poetry.dependencies]\npython = \"^3.11\"\n"
	if v := TOMLString(text, "package", "rust-version"); v != "1.70" {
		t.Fatalf("got %q", v)
	}
	if v := TOMLString(text, "package", "name"); v != "q" {
		t.Fatalf("single quotes: %q", v)
	}
	if v := TOMLString(text, "missing", "rust-version"); v != "" {
		t.Fatalf("missing table: %q", v)
	}
	if v := TOMLString(text, "tool.poetry.dependencies", "python"); v != "^3.11" {
		t.Fatalf("dotted table: %q", v)
	}
	if stripComment(`a = "x # y" # z`) != `a = "x # y"` {
		t.Fatal("comment inside quotes")
	}
	if !TOMLInherits("[package]\nrust-version.workspace = true\n", "package", "rust-version") || !TOMLInherits("[package]\nrust-version = { workspace = true }\n", "package", "rust-version") {
		t.Fatal("inherits")
	}
	if TOMLInherits("[package]\nrust-version = \"1.70\"\n", "package", "rust-version") {
		t.Fatal("literal is not inherited")
	}
}
