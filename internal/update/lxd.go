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

package update

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/canonical/axiomaton/internal/proxy"
)

// LXD is a Runtime backed by the lxc CLI, running either system containers
// or virtual machines.
//
// With a Proxy, instances are attached to a dedicated bridge network that
// has no NAT and no routing, carrying a network ACL that drops all egress
// except DHCP and TCP to the proxy's port range on the host's side of the
// bridge. The proxy listens there, so as with podman it is the instance's
// only way out and nothing but HTTP and HTTPS can leave. Without a Proxy
// instances get the default profile's network, unrestricted.
type LXD struct {
	Image string
	// VM launches virtual machines instead of containers.
	VM bool
	// Binary is the lxc executable; defaults to "lxc".
	Binary string
	Proxy  *proxy.Server
	// Network is the isolated bridge's name; defaults to "axiomaton".
	Network string
	// Log receives lifecycle messages; nil discards them.
	Log func(format string, args ...any)
	// Consent is asked before the first instance is launched and, again
	// under its own key, before the isolated network is created; nil
	// grants both.
	Consent ConsentFunc
}

const (
	// ProxyPortFrom and ProxyPortTo bound the ports the proxy may use on
	// the isolated bridge; the network ACL allows exactly this range, so
	// several ax processes can each have a port.
	ProxyPortFrom, ProxyPortTo = 31280, 31299
	defaultLXDNetwork          = "axiomaton"
	vmMemory                   = "4GiB"
	vmCPUs                     = "2"
)

var (
	lxdNetMu   sync.Mutex
	lxdNetAddr = map[string]string{} // network -> host address on the bridge
)

func (l LXD) bin() string {
	if l.Binary != "" {
		return l.Binary
	}
	return "lxc"
}

func (l LXD) network() string {
	if l.Network != "" {
		return l.Network
	}
	return defaultLXDNetwork
}

func (l LXD) logf(format string, args ...any) {
	if l.Log != nil {
		l.Log(format, args...)
	}
}

func (l LXD) run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, l.bin(), args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("lxc %s: %w: %s", strings.Join(args[:min(2, len(args))], " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// Start launches an instance, waits until commands run in it and, with a
// proxy, attaches it to the isolated network and installs the CA.
func (l LXD) Start(ctx context.Context) (Container, error) {
	if l.Image == "" {
		return nil, errors.New("lxd: no image configured")
	}
	if err := askConsent(ctx, l.Consent, lxdConsent(l.Image, l.network(), l.VM, l.Proxy != nil)); err != nil {
		return nil, err
	}
	var suffix [6]byte
	rand.Read(suffix[:])
	name := "axiomaton-" + hex.EncodeToString(suffix[:])
	kind := "container"
	args := []string{"launch", l.Image, name}
	if l.VM {
		kind = "virtual machine"
		args = append(args, "--vm", "-c", "limits.memory="+vmMemory, "-c", "limits.cpu="+vmCPUs)
	}
	var hostIP string
	if l.Proxy != nil {
		ip, err := l.ensureNetwork(ctx)
		if err != nil {
			return nil, err
		}
		hostIP = ip
		args = append(args, "--network", l.network())
	}
	l.logf("starting LXD %s %s from %s", kind, name, l.Image)
	if _, err := l.run(ctx, args...); err != nil {
		return nil, err
	}
	c := &lxdContainer{bin: l.bin(), name: name, log: l.logf}
	fail := func(err error) (Container, error) {
		c.Close(ctx)
		return nil, err
	}
	// Containers answer at once; a virtual machine needs its agent up.
	wait := 90 * time.Second
	if l.VM {
		wait = 5 * time.Minute
	}
	if err := c.waitReady(ctx, wait); err != nil {
		return fail(err)
	}
	if res, err := c.exec(ctx, "", nil, "mkdir -p /work", 30*time.Second); err != nil || res.ExitCode != 0 {
		if err == nil {
			err = fmt.Errorf("mkdir /work in %s failed: %s", name, strings.TrimSpace(res.Stderr))
		}
		return fail(err)
	}
	if l.Proxy != nil {
		if err := l.attachProxy(ctx, c, hostIP); err != nil {
			return fail(fmt.Errorf("attaching the cache proxy to %s: %w (set proxy.disabled: true in ax.cnf to run instances with ordinary, unrestricted networking)", name, err))
		}
	}
	return c, nil
}

// ensureNetwork creates the isolated bridge and its ACL if they do not
// exist and returns the host's address on the bridge.
func (l LXD) ensureNetwork(ctx context.Context) (string, error) {
	lxdNetMu.Lock()
	defer lxdNetMu.Unlock()
	netName := l.network()
	if ip, ok := lxdNetAddr[netName]; ok {
		return ip, nil
	}
	// Creating the network or its ACL is asked about once, before the
	// first of them that is missing.
	consented := false
	consent := func() error {
		if consented {
			return nil
		}
		consented = true
		return askConsent(ctx, l.Consent, lxdNetworkConsent(netName))
	}
	if _, err := l.run(ctx, "network", "show", netName); err != nil {
		if err := consent(); err != nil {
			return "", err
		}
		l.logf("creating isolated LXD network %s", netName)
		if _, err := l.run(ctx, "network", "create", netName, "ipv4.address=auto", "ipv4.nat=false", "ipv4.routing=false", "ipv6.address=none"); err != nil {
			// Another ax may have created it in the meantime.
			if _, again := l.run(ctx, "network", "show", netName); again != nil {
				return "", err
			}
		}
	}
	out, err := l.run(ctx, "network", "get", netName, "ipv4.address")
	if err != nil {
		return "", err
	}
	ip, _, err := net.ParseCIDR(strings.TrimSpace(out))
	if err != nil {
		return "", fmt.Errorf("network %s has no usable ipv4.address (%q)", netName, strings.TrimSpace(out))
	}
	hostIP := ip.String()
	if _, err := l.run(ctx, "network", "acl", "show", netName); err != nil {
		if err := consent(); err != nil {
			return "", err
		}
		l.logf("creating LXD network ACL %s: egress only to the proxy on %s", netName, hostIP)
		if _, err := l.run(ctx, "network", "acl", "create", netName); err != nil {
			if _, again := l.run(ctx, "network", "acl", "show", netName); again == nil {
				// Created concurrently; its creator finishes the rules.
				lxdNetAddr[netName] = hostIP
				return hostIP, nil
			}
			return "", err
		}
		rules := [][]string{
			{"egress", "action=allow", "protocol=tcp", "destination=" + hostIP + "/32", fmt.Sprintf("destination_port=%d-%d", ProxyPortFrom, ProxyPortTo)},
			{"egress", "action=allow", "protocol=udp", "destination_port=67"},
		}
		for _, r := range rules {
			if _, err := l.run(ctx, append([]string{"network", "acl", "rule", "add", netName}, r...)...); err != nil {
				return "", err
			}
		}
		if _, err := l.run(ctx, "network", "set", netName, "security.acls="+netName, "security.acls.default.egress.action=drop"); err != nil {
			return "", err
		}
	}
	lxdNetAddr[netName] = hostIP
	return hostIP, nil
}

// attachProxy starts the proxy on the bridge if needed, checks the
// instance can reach it, installs the CA and sets the exec environment.
func (l LXD) attachProxy(ctx context.Context, c *lxdContainer, hostIP string) error {
	hostPort, err := l.Proxy.StartRange(hostIP, ProxyPortFrom, ProxyPortTo)
	if err != nil {
		return err
	}
	host, port, _ := net.SplitHostPort(hostPort)
	// DHCP takes a moment; wait until the proxy answers from inside.
	probe := fmt.Sprintf("for i in $(seq 1 120); do timeout 1 bash -c 'exec 3<>/dev/tcp/%s/%s' 2>/dev/null && exit 0; sleep 0.5; done; exit 1", host, port)
	if res, err := c.exec(ctx, "", nil, probe, 90*time.Second); err != nil || res.ExitCode != 0 {
		return fmt.Errorf("the proxy at %s is not reachable from the instance: %v %s", hostPort, err, strings.TrimSpace(res.Stderr))
	}
	// Direct egress must be impossible; refuse to continue if it is not.
	if res, err := c.exec(ctx, "", nil, "timeout 3 bash -c 'exec 3<>/dev/tcp/1.1.1.1/443'", 15*time.Second); err == nil && res.ExitCode == 0 {
		return errors.New("the instance can reach the internet directly; the network ACL is not in force")
	}
	certFile, err := os.CreateTemp("", "ax-ca-*.crt")
	if err != nil {
		return err
	}
	defer os.Remove(certFile.Name())
	certFile.Write(l.Proxy.CA.CertPEM)
	certFile.Close()
	for _, dst := range []string{CAPath, "/usr/local/share/ca-certificates/axiomaton.crt"} {
		if _, err := l.run(ctx, "file", "push", "--create-dirs", "--mode", "0644", certFile.Name(), c.name+dst); err != nil {
			return fmt.Errorf("installing CA at %s: %w", dst, err)
		}
	}
	url := l.Proxy.URL(hostPort)
	apt := fmt.Sprintf("Acquire::http::Proxy %q;\nAcquire::https::Proxy %q;\nAcquire::https::CAInfo %q;\n", url, url, CAPath)
	if res, err := c.exec(ctx, "", nil, "mkdir -p /etc/apt/apt.conf.d && printf '%s' "+shellQuote(apt)+" > /etc/apt/apt.conf.d/99axiomaton-proxy", 30*time.Second); err != nil || res.ExitCode != 0 {
		return fmt.Errorf("writing apt proxy configuration: %v %s", err, strings.TrimSpace(res.Stderr))
	}
	c.env = ProxyEnv(url, CAPath)
	l.logf("instance %s is on the isolated network; all traffic goes through the cache proxy at %s", c.name, hostPort)
	return nil
}

type lxdContainer struct {
	bin  string
	name string
	env  []string
	log  func(format string, args ...any)
}

// waitReady polls until a command runs inside the instance.
func (c *lxdContainer) waitReady(ctx context.Context, limit time.Duration) error {
	deadline := time.Now().Add(limit)
	for {
		res, err := c.exec(ctx, "", nil, "true", 20*time.Second)
		if err == nil && res.ExitCode == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("instance %s did not become ready within %s", c.name, limit)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		time.Sleep(2 * time.Second)
	}
}

func (c *lxdContainer) Exec(ctx context.Context, command string, timeout time.Duration) (ExecResult, error) {
	return c.exec(ctx, "/work", c.env, command, timeout)
}

func (c *lxdContainer) exec(ctx context.Context, workdir string, env []string, command string, timeout time.Duration) (ExecResult, error) {
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	args := []string{"exec", c.name, "--env", "DEBIAN_FRONTEND=noninteractive"}
	if workdir != "" {
		args = append(args, "--cwd", workdir)
	}
	for _, kv := range env {
		args = append(args, "--env", kv)
	}
	args = append(args, "--", "bash", "-lc", command)
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
		return res, fmt.Errorf("lxc exec: %w", err)
	}
	return res, nil
}

func (c *lxdContainer) Push(ctx context.Context, local, remote string) error {
	out, err := exec.CommandContext(ctx, c.bin, "file", "push", "--create-dirs", "--mode", "0644", local, c.name+remote).CombinedOutput()
	if err != nil {
		return fmt.Errorf("lxc file push %s: %w: %s", remote, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (c *lxdContainer) Close(ctx context.Context) error {
	c.log("deleting LXD instance %s", c.name)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, c.bin, "delete", "--force", c.name).CombinedOutput()
	if err != nil {
		return fmt.Errorf("lxc delete %s: %w: %s", c.name, err, strings.TrimSpace(string(out)))
	}
	return nil
}
