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
	"bufio"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/canonical/axiomaton/internal/config"
	"github.com/canonical/axiomaton/internal/update"
)

// consenter answers the runtimes' consent requests: a standing "always"
// in ax.cnf grants without asking, otherwise the user is asked once per
// process for each distinct request and may answer yes, no or always,
// the last of which is saved to ax.cnf. Requests arrive from the
// goroutines that start containers, so answering is serialised.
type consenter struct {
	cfg  config.User
	path string
	p    prompter
	save func(config.User) error

	mu      sync.Mutex
	granted map[string]bool
}

func (a *app) consenter(cfg config.User) *consenter {
	if a.consent == nil {
		a.consent = &consenter{
			cfg:     cfg,
			path:    a.configPath,
			p:       prompter{raw: stdin, in: bufio.NewReader(stdin), out: a.stderr, ask: interactive()},
			save:    func(u config.User) error { return config.WriteUser(a.configPath, u, time.Now()) },
			granted: map[string]bool{},
		}
	}
	return a.consent
}

// errRefused is the error behind every consent the user did not give.
var errRefused = errors.New("not allowed by the user")

func (c *consenter) ask(ctx context.Context, req update.ConsentRequest) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.granted[req.Key] || c.cfg.Consent[req.Key] {
		return nil
	}
	if !c.p.ask {
		return fmt.Errorf("ax needs permission to %s but cannot ask (stdin is not a terminal); run it interactively once and answer \"always\", or set consent.%s: true in %s", req.Title, req.Key, c.path)
	}
	fmt.Fprintf(c.p.out, "\nax wants to %s. It will:\n", req.Title)
	for _, d := range req.Details {
		fmt.Fprintf(c.p.out, "  - %s\n", d)
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		fmt.Fprintf(c.p.out, "Allow? [y]es, [n]o, [a]lways (saves consent.%s: true in %s) [n]: ", req.Key, c.path)
		line, err := c.p.line()
		if err != nil {
			return err
		}
		switch strings.ToLower(line) {
		case "y", "yes":
			c.granted[req.Key] = true
			return nil
		case "a", "always":
			c.granted[req.Key] = true
			if c.cfg.Consent == nil {
				c.cfg.Consent = map[string]bool{}
			}
			c.cfg.Consent[req.Key] = true
			if err := c.save(c.cfg); err != nil {
				return fmt.Errorf("saving consent.%s to %s: %w", req.Key, c.path, err)
			}
			fmt.Fprintf(c.p.out, "Saved consent.%s: true in %s.\n", req.Key, c.path)
			return nil
		case "", "n", "no":
			return errRefused
		}
		fmt.Fprintln(c.p.out, "Please answer y, n or a.")
	}
}
