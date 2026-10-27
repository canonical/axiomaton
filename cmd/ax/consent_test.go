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
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/canonical/axiomaton/internal/config"
	"github.com/canonical/axiomaton/internal/update"
)

func TestConsenter(t *testing.T) {
	ctx := context.Background()
	cfgPath := filepath.Join(t.TempDir(), "ax.cnf")
	req := update.ConsentRequest{Key: config.ConsentPodman, Title: "run agents in podman containers", Details: []string{"pull the image", "start containers"}}
	netReq := update.ConsentRequest{Key: config.ConsentLXDNetwork, Title: "create the isolated LXD network axiomaton", Details: []string{"create a bridge"}}
	newApp := func(in string, tty bool) (*app, *bytes.Buffer) {
		oldIn, oldTTY := stdin, interactive
		stdin, interactive = strings.NewReader(in), func() bool { return tty }
		t.Cleanup(func() { stdin, interactive = oldIn, oldTTY })
		var errOut bytes.Buffer
		return &app{configPath: cfgPath, stderr: &errOut, stdout: &errOut}, &errOut
	}
	cfg, _ := config.LoadUser(cfgPath)

	// Not a terminal and no standing consent: refused with advice.
	a, _ := newApp("", false)
	err := a.consenter(cfg).ask(ctx, req)
	if err == nil || !strings.Contains(err.Error(), "cannot ask") || !strings.Contains(err.Error(), "consent.podman: true in "+cfgPath) {
		t.Fatalf("non-interactive: %v", err)
	}

	// No: refused, nothing saved, and the prompt explained the request.
	a, errOut := newApp("n\n", true)
	if err := a.consenter(cfg).ask(ctx, req); !errors.Is(err, errRefused) {
		t.Fatalf("no: %v", err)
	}
	if out := errOut.String(); !strings.Contains(out, "ax wants to run agents in podman containers. It will:\n  - pull the image\n  - start containers\n") || !strings.Contains(out, "[y]es, [n]o, [a]lways") {
		t.Fatalf("prompt:\n%s", out)
	}
	if _, err := os.Stat(cfgPath); err == nil {
		t.Fatal("no should not write ax.cnf")
	}

	// Yes: granted for the process only; a second request for the same
	// key is not asked again, a different key is. An unknown answer asks
	// again; an empty one refuses.
	a, errOut = newApp("maybe\ny\n\n", true)
	c := a.consenter(cfg)
	if err := c.ask(ctx, req); err != nil {
		t.Fatalf("yes: %v", err)
	}
	if err := c.ask(ctx, req); err != nil {
		t.Fatalf("repeat: %v", err)
	}
	if err := c.ask(ctx, netReq); !errors.Is(err, errRefused) {
		t.Fatalf("other key: %v", err)
	}
	if out := errOut.String(); strings.Count(out, "Allow?") != 3 || !strings.Contains(out, "Please answer y, n or a.") || strings.Count(out, "ax wants to") != 2 {
		t.Fatalf("prompts:\n%s", out)
	}
	if _, err := os.Stat(cfgPath); err == nil {
		t.Fatal("yes should not write ax.cnf")
	}

	// Always: granted and saved; a fresh process with that ax.cnf does
	// not ask, even without a terminal.
	a, errOut = newApp("always\n", true)
	if err := a.consenter(cfg).ask(ctx, req); err != nil {
		t.Fatalf("always: %v", err)
	}
	if !strings.Contains(errOut.String(), "Saved consent.podman: true in "+cfgPath) {
		t.Fatalf("always output:\n%s", errOut.String())
	}
	data, err := os.ReadFile(cfgPath)
	if err != nil || !strings.Contains(string(data), "podman: true") || !strings.Contains(string(data), "lxd: false") {
		t.Fatalf("saved config: %v\n%s", err, data)
	}
	saved, err := config.LoadUser(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	a, errOut = newApp("", false)
	c = a.consenter(saved)
	if err := c.ask(ctx, req); err != nil || errOut.Len() != 0 {
		t.Fatalf("standing consent: %v\n%s", err, errOut.String())
	}
	if err := c.ask(ctx, netReq); err == nil || !strings.Contains(err.Error(), "consent.lxd-network: true") {
		t.Fatalf("standing consent is per key: %v", err)
	}
}
