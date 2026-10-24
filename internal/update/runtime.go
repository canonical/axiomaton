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

// Package update runs containerised agents that extend the axiomlog for
// built-in subjects and confirm what they propose.
package update

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// ExecResult is the outcome of a command run inside a container.
type ExecResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
	TimedOut bool
}

// Container is a running sandbox that commands can be executed in.
type Container interface {
	// Exec runs a shell command, bounded by timeout.
	Exec(ctx context.Context, command string, timeout time.Duration) (ExecResult, error)
	// Close stops and removes the container.
	Close(ctx context.Context) error
}

// Runtime starts fresh containers.
type Runtime interface {
	Start(ctx context.Context) (Container, error)
}

// Podman is a Runtime backed by the podman CLI.
type Podman struct {
	Image string
	// Binary is the podman executable; defaults to "podman".
	Binary string
	// Log receives lifecycle messages; nil discards them.
	Log func(format string, args ...any)
}

func (p Podman) bin() string {
	if p.Binary != "" {
		return p.Binary
	}
	return "podman"
}

func (p Podman) logf(format string, args ...any) {
	if p.Log != nil {
		p.Log(format, args...)
	}
}

// Start creates a detached container that idles until closed.
func (p Podman) Start(ctx context.Context) (Container, error) {
	if p.Image == "" {
		return nil, errors.New("podman: no image configured")
	}
	var suffix [6]byte
	rand.Read(suffix[:])
	name := "axiomaton-" + hex.EncodeToString(suffix[:])
	args := []string{"run", "-d", "--rm", "--name", name,
		"-e", "DEBIAN_FRONTEND=noninteractive",
		p.Image, "sleep", "infinity"}
	p.logf("starting container %s from %s", name, p.Image)
	cmd := exec.CommandContext(ctx, p.bin(), args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("podman run %s: %w: %s", p.Image, err, strings.TrimSpace(stderr.String()))
	}
	c := &podmanContainer{bin: p.bin(), name: name, log: p.logf}
	// Create the working directory every exec runs in; the image has none.
	if res, err := c.exec(ctx, "", "mkdir -p /work", 30*time.Second); err != nil || res.ExitCode != 0 {
		c.Close(ctx)
		if err == nil {
			err = fmt.Errorf("mkdir /work in %s failed: %s", name, strings.TrimSpace(res.Stderr))
		}
		return nil, err
	}
	return c, nil
}

type podmanContainer struct {
	bin  string
	name string
	log  func(format string, args ...any)
}

func (c *podmanContainer) Exec(ctx context.Context, command string, timeout time.Duration) (ExecResult, error) {
	return c.exec(ctx, "/work", command, timeout)
}

func (c *podmanContainer) exec(ctx context.Context, workdir, command string, timeout time.Duration) (ExecResult, error) {
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	args := []string{"exec"}
	if workdir != "" {
		args = append(args, "-w", workdir)
	}
	args = append(args, c.name, "bash", "-lc", command)
	cmd := exec.CommandContext(ctx, c.bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	res := ExecResult{Stdout: stdout.String(), Stderr: stderr.String()}
	if ctx.Err() == context.DeadlineExceeded {
		res.TimedOut = true
		res.ExitCode = -1
		return res, nil
	}
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		res.ExitCode = ee.ExitCode()
	default:
		return res, fmt.Errorf("podman exec: %w", err)
	}
	return res, nil
}

func (c *podmanContainer) Close(ctx context.Context) error {
	c.log("removing container %s", c.name)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, c.bin, "rm", "-f", c.name).CombinedOutput()
	if err != nil {
		return fmt.Errorf("podman rm %s: %w: %s", c.name, err, strings.TrimSpace(string(out)))
	}
	return nil
}
