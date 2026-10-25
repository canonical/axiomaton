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
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/canonical/axiomaton/internal/proxy"
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
	// Proxy, when set, is the cache proxy that is a container's only way to
	// the network: the container is created without a network, ax runs a
	// forwarder inside its network namespace that relays 127.0.0.1:3128 to
	// the proxy's unix socket, the proxy CA is installed and every command
	// runs with the proxy and CA environment set. Without it containers
	// get podman's default network, unrestricted.
	Proxy *proxy.Server
	// SocketPath is where the proxy's unix socket is created; empty means
	// a path under the runtime directory.
	SocketPath string
	// Self is the ax binary to run as the in-namespace forwarder; empty
	// means the running executable.
	Self string
	// Log receives lifecycle messages; nil discards them.
	Log func(format string, args ...any)
}

// CAPath is where the proxy CA certificate is placed in containers. A
// copy also goes under /usr/local/share/ca-certificates so that installing
// the ca-certificates package picks it up for the system store.
const CAPath = "/etc/axiomaton/ca.crt"

// ProxyPort is the loopback port inside a container that reaches the proxy.
const ProxyPort = 3128

// ForwardCommand is the hidden ax subcommand that runs the in-namespace
// forwarder: ax ForwardCommand <socket> <listen address>.
const ForwardCommand = "_netns-forward"

var (
	rootlessOnce sync.Once
	rootless     bool
)

// isRootless reports whether podman runs rootless, in which case entering
// a container's network namespace needs podman unshare.
func isRootless(bin string) bool {
	rootlessOnce.Do(func() {
		out, err := exec.Command(bin, "info", "--format", "{{.Host.Security.Rootless}}").Output()
		rootless = err != nil || strings.TrimSpace(string(out)) != "false"
	})
	return rootless
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
		"-e", "DEBIAN_FRONTEND=noninteractive"}
	if p.Proxy != nil {
		// No network of its own: everything goes through the proxy.
		args = append(args, "--network", "none")
	}
	args = append(args, p.Image, "sleep", "infinity")
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
	if p.Proxy != nil {
		if err := p.attachProxy(ctx, c); err != nil {
			// The container has no network without the proxy, so this is
			// fatal rather than a degraded start.
			c.Close(ctx)
			return nil, fmt.Errorf("attaching the cache proxy to %s: %w (set proxy.disabled: true in ax.cnf to run containers with ordinary, unrestricted networking)", name, err)
		}
	}
	return c, nil
}

// attachProxy starts the proxy if needed, runs the forwarder inside the
// container's network namespace, installs the CA in the container and sets
// the environment every exec runs with.
func (p Podman) attachProxy(ctx context.Context, c *podmanContainer) error {
	sock := p.SocketPath
	if sock == "" {
		sock = defaultSocketPath()
	}
	if err := p.Proxy.StartUnix(sock); err != nil {
		return err
	}
	if err := p.startForwarder(ctx, c, p.Proxy.SocketPath()); err != nil {
		return err
	}
	if res, err := c.exec(ctx, "", "mkdir -p "+filepath.Dir(CAPath)+" /usr/local/share/ca-certificates /etc/apt/apt.conf.d", 30*time.Second); err != nil || res.ExitCode != 0 {
		return fmt.Errorf("preparing CA directories: %v %s", err, strings.TrimSpace(res.Stderr))
	}
	// Copy the certificate this proxy signs with, not the file on disk,
	// which another ax process could have regenerated meanwhile.
	certFile, err := os.CreateTemp("", "ax-ca-*.crt")
	if err != nil {
		return err
	}
	defer os.Remove(certFile.Name())
	if _, err := certFile.Write(p.Proxy.CA.CertPEM); err != nil {
		certFile.Close()
		return err
	}
	certFile.Close()
	for _, dst := range []string{CAPath, "/usr/local/share/ca-certificates/axiomaton.crt"} {
		out, err := exec.CommandContext(ctx, c.bin, "cp", certFile.Name(), c.name+":"+dst).CombinedOutput()
		if err != nil {
			return fmt.Errorf("installing CA at %s: %w: %s", dst, err, strings.TrimSpace(string(out)))
		}
	}
	url := p.Proxy.URL(fmt.Sprintf("127.0.0.1:%d", ProxyPort))
	apt := fmt.Sprintf("Acquire::http::Proxy %q;\nAcquire::https::Proxy %q;\nAcquire::https::CAInfo %q;\n", url, url, CAPath)
	if res, err := c.exec(ctx, "", "printf '%s' "+shellQuote(apt)+" > /etc/apt/apt.conf.d/99axiomaton-proxy", 30*time.Second); err != nil || res.ExitCode != 0 {
		return fmt.Errorf("writing apt proxy configuration: %v %s", err, strings.TrimSpace(res.Stderr))
	}
	c.env = ProxyEnv(url, CAPath)
	p.logf("container %s has no network; all traffic goes through the cache proxy at 127.0.0.1:%d", c.name, ProxyPort)
	return nil
}

// defaultSocketPath is a short path (unix sockets are limited to about a
// hundred bytes) private to this process.
func defaultSocketPath() string {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = os.TempDir()
	}
	return filepath.Join(dir, fmt.Sprintf("ax-proxy-%d.sock", os.Getpid()))
}

// startForwarder runs this binary's hidden forwarder subcommand inside the
// container's network namespace, listening on 127.0.0.1:ProxyPort there
// and relaying to the proxy socket on the host. The forwarder holds a pipe
// from us and exits when it closes, so it never outlives this process.
func (p Podman) startForwarder(ctx context.Context, c *podmanContainer, socket string) error {
	self := p.Self
	if self == "" {
		var err error
		if self, err = os.Executable(); err != nil {
			return fmt.Errorf("locating the ax binary: %w", err)
		}
	}
	out, err := exec.CommandContext(ctx, c.bin, "inspect", "--format", "{{.State.Pid}}", c.name).Output()
	if err != nil {
		return fmt.Errorf("finding the container's pid: %w", err)
	}
	pid := strings.TrimSpace(string(out))
	if pid == "" || pid == "0" {
		return errors.New("the container has no pid")
	}
	listen := fmt.Sprintf("127.0.0.1:%d", ProxyPort)
	args := []string{"nsenter", "--net=/proc/" + pid + "/ns/net", "--", self, ForwardCommand, socket, listen}
	if isRootless(c.bin) {
		// The namespace belongs to podman's user namespace; enter that first.
		args = append([]string{c.bin, "unshare"}, args...)
	}
	// Not bound to ctx: the forwarder lives as long as the container.
	cmd := exec.Command(args[0], args[1:]...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting the network forwarder (%s): %w", args[0], err)
	}
	c.forwarder, c.forwarderStdin = cmd, stdin
	// Wait until the port answers inside the container.
	probe := fmt.Sprintf("for i in $(seq 1 50); do timeout 1 bash -c 'exec 3<>/dev/tcp/127.0.0.1/%d' 2>/dev/null && exit 0; sleep 0.1; done; exit 1", ProxyPort)
	res, err := c.exec(ctx, "", probe, 30*time.Second)
	if err != nil || res.ExitCode != 0 {
		c.stopForwarder()
		return fmt.Errorf("the forwarder did not come up inside the container: %v %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// ProxyEnv is the environment that points common tools at the proxy and
// its CA: Go, curl, git, pip, uv, cargo, rustup, Node and anything that
// honours the usual proxy variables.
func ProxyEnv(proxyURL, caPath string) []string {
	return []string{
		"HTTP_PROXY=" + proxyURL, "HTTPS_PROXY=" + proxyURL,
		"http_proxy=" + proxyURL, "https_proxy=" + proxyURL,
		"NO_PROXY=localhost,127.0.0.1,::1", "no_proxy=localhost,127.0.0.1,::1",
		"SSL_CERT_FILE=" + caPath, "CURL_CA_BUNDLE=" + caPath, "GIT_SSL_CAINFO=" + caPath,
		"REQUESTS_CA_BUNDLE=" + caPath, "PIP_CERT=" + caPath, "CARGO_HTTP_CAINFO=" + caPath,
		"NODE_EXTRA_CA_CERTS=" + caPath, "AXIOMATON_PROXY=1",
	}
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

type podmanContainer struct {
	bin  string
	name string
	env  []string
	log  func(format string, args ...any)

	forwarder      *exec.Cmd
	forwarderStdin io.WriteCloser
}

func (c *podmanContainer) stopForwarder() {
	if c.forwarder == nil {
		return
	}
	c.forwarderStdin.Close()
	done := make(chan struct{})
	go func() { c.forwarder.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		c.forwarder.Process.Kill()
		<-done
	}
	c.forwarder = nil
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
	for _, kv := range c.env {
		args = append(args, "--env", kv)
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
	c.stopForwarder()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, c.bin, "rm", "-f", c.name).CombinedOutput()
	if err != nil {
		return fmt.Errorf("podman rm %s: %w: %s", c.name, err, strings.TrimSpace(string(out)))
	}
	return nil
}
