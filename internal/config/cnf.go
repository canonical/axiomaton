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

package config

import (
	"encoding/json"
	"regexp"
	"strconv"

	"github.com/canonical/go-cnf/cnfast"
	"github.com/canonical/go-cnf/cnfmap"
)

// Decode reads a cnf document into v through its JSON form: cnf maps one
// to one onto JSON, so the struct tags are json tags and the usual
// encoding/json rules apply.
func Decode(data []byte, v any) error {
	tree, err := cnfmap.Unmarshal(data)
	if err != nil {
		return err
	}
	j, err := json.Marshal(tree)
	if err != nil {
		return err
	}
	return json.Unmarshal(j, v)
}

// Indent is the step block values are written at.
const Indent = "    "

var bareKey = regexp.MustCompile(`^[-_a-zA-Z0-9]+$`)

// Key spells s as a cnf map key: bare when the grammar allows, quoted
// otherwise. A dot would otherwise read as a path through nested maps.
func Key(s string) []*cnfast.Str {
	k := &cnfast.Str{Data: s}
	if !bareKey.MatchString(s) {
		k.Quote = cnfast.DoubleQuoted
	}
	return []*cnfast.Str{k}
}

// Str is a one-line double-quoted string value.
func Str(s string) *cnfast.Str { return &cnfast.Str{Data: s, Quote: cnfast.DoubleQuoted} }

// Int is an integer value.
func Int(n int) *cnfast.Num { return &cnfast.Num{Text: strconv.Itoa(n)} }

// Float is a number value spelled without an exponent.
func Float(f float64) *cnfast.Num { return &cnfast.Num{Text: strconv.FormatFloat(f, 'f', -1, 64)} }

// Bool is a true or false value.
func Bool(b bool) *cnfast.Scalar { return &cnfast.Scalar{Text: strconv.FormatBool(b)} }

// Pair is one key: value item. A comment, when given, follows the value
// on its line. A block map or list that turned out empty is written
// inline, since a block holds at least one entry.
func Pair(key string, value cnfast.Value, comment string) *cnfast.Item {
	item := &cnfast.Item{Key: Key(key), Value: value}
	if comment != "" {
		item.InCmt = Comment(comment)
	}
	switch v := value.(type) {
	case *cnfast.Map:
		v.Block = v.Block && len(v.Items) > 0
		if v.Block {
			item.Indent = Indent
		}
	case *cnfast.List:
		v.Block = v.Block && len(v.Items) > 0
		v.Dashed = v.Block
		if v.Block {
			item.Indent = Indent
		}
	}
	return item
}

// Comment spells one trailing comment for a value's line.
func Comment(text string) cnfast.Cmt {
	return cnfast.Cmt{Text: []string{"  # " + text}}
}

// Lines spells standalone comment lines, each given without its leading
// "# ". An empty line is a blank one.
func Lines(lines ...string) cnfast.Cmt {
	c := cnfast.Cmt{Text: make([]string, len(lines))}
	for i, l := range lines {
		switch l {
		case "":
			c.Text[i] = "#"
		default:
			c.Text[i] = "# " + l
		}
	}
	return c
}

// InlineMap is a one-line {k: v, ...} map.
func InlineMap(items ...*cnfast.Item) *cnfast.Map { return &cnfast.Map{Items: items} }

// BlockMap is a map with one pair per line. Items may be appended before
// it is given to Pair or Document.
func BlockMap(items ...*cnfast.Item) *cnfast.Map {
	return &cnfast.Map{Items: items, Block: true}
}

// DashedList is a list with one "- " item per line. Items may be appended
// before it is given to Pair.
func DashedList(items ...*cnfast.Item) *cnfast.List {
	return &cnfast.List{Items: items, Block: true, Dashed: true}
}

// Document renders root as a file, with header as the comment run above
// it separated by a blank line.
func Document(header cnfast.Cmt, root *cnfast.Map) ([]byte, error) {
	root.Block = len(root.Items) > 0
	return cnfast.Render(&cnfast.Item{HeadCmt: header, Value: root}, nil)
}
