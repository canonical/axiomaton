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
	"os"
	"path/filepath"
)

const appDir = "axiomaton"

func xdg(env, fallback string) string {
	if v := os.Getenv(env); v != "" {
		return filepath.Join(v, appDir)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, fallback, appDir)
}

// ConfigDir is $XDG_CONFIG_HOME/axiomaton.
func ConfigDir() string { return xdg("XDG_CONFIG_HOME", ".config") }

// CacheDir is $XDG_CACHE_HOME/axiomaton.
func CacheDir() string { return xdg("XDG_CACHE_HOME", ".cache") }

// DataDir is $XDG_DATA_HOME/axiomaton.
func DataDir() string { return xdg("XDG_DATA_HOME", filepath.Join(".local", "share")) }

// UserConfigPath is the default location of ax.cnf.
func UserConfigPath() string { return filepath.Join(ConfigDir(), "ax.cnf") }

// UserLogPath is the user wide fallback axiomlog.
func UserLogPath() string { return filepath.Join(DataDir(), "axiomlog") }
