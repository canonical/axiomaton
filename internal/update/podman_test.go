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
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/canonical/axiomaton/internal/proxy"
)

// TestPodmanIntegration starts a real container. It runs only when
// AX_PODMAN_TEST=1 because it needs podman and network access.
func TestPodmanIntegration(t *testing.T) {
	if os.Getenv("AX_PODMAN_TEST") != "1" {
		t.Skip("set AX_PODMAN_TEST=1 to run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	rt := Podman{Image: "public.ecr.aws/ubuntu/ubuntu:latest", Log: t.Logf}
	c, err := rt.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := c.Close(ctx); err != nil {
			t.Error(err)
		}
	}()
	res, err := c.Exec(ctx, "echo hello > f && cat f && pwd && id -u", 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 || !strings.Contains(res.Stdout, "hello") || !strings.Contains(res.Stdout, "/work") || !strings.Contains(res.Stdout, "0") {
		t.Fatalf("exec: %+v", res)
	}
	res, err = c.Exec(ctx, "cat f; exit 3", 30*time.Second)
	if err != nil || res.ExitCode != 3 || !strings.Contains(res.Stdout, "hello") {
		t.Fatalf("state should persist, exit code should propagate: %+v %v", res, err)
	}
	res, err = c.Exec(ctx, "sleep 10", time.Second)
	if err != nil || !res.TimedOut {
		t.Fatalf("timeout: %+v %v", res, err)
	}
}

// TestPodmanProxyIntegration drives apt, curl and the Go module proxy
// through the cache proxy from inside a real container. It runs only when
// AX_PODMAN_TEST=1.
func TestPodmanProxyIntegration(t *testing.T) {
	if os.Getenv("AX_PODMAN_TEST") != "1" {
		t.Skip("set AX_PODMAN_TEST=1 to run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	dir := t.TempDir()
	ca, err := proxy.LoadOrCreateCA(filepath.Join(dir, "ca"))
	if err != nil {
		t.Fatal(err)
	}
	var hits, misses atomic.Int64
	srv := &proxy.Server{CA: ca, Cache: &proxy.Cache{Dir: filepath.Join(dir, "cache")}, Token: proxy.NewToken(), Log: func(f string, a ...any) {
		line := fmt.Sprintf(f, a...)
		switch {
		case strings.Contains(line, "proxy HIT"):
			hits.Add(1)
		case strings.Contains(line, "proxy MISS"):
			misses.Add(1)
		}
		t.Log(line)
	}}
	defer srv.Close()
	// The forwarder is the ax binary; build one for the test.
	self := filepath.Join(dir, "ax")
	if out, err := exec.Command("go", "build", "-o", self, "../../cmd/ax").CombinedOutput(); err != nil {
		t.Fatalf("build ax: %v\n%s", err, out)
	}
	rt := Podman{Image: "public.ecr.aws/ubuntu/ubuntu:latest", Proxy: srv, Self: self, Log: t.Logf}
	c, err := rt.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(ctx)
	if c.(*podmanContainer).env == nil {
		t.Fatal("proxy environment was not attached")
	}
	res, err := c.Exec(ctx, "test -s "+CAPath+" && grep -c Proxy /etc/apt/apt.conf.d/99axiomaton-proxy && echo $HTTPS_PROXY | sed 's/:[^:@]*@/:***@/'", 30*time.Second)
	if err != nil || res.ExitCode != 0 || !strings.Contains(res.Stdout, "@127.0.0.1:3128") {
		t.Fatalf("setup: %+v %v", res, err)
	}
	t.Logf("env: %s", res.Stdout)
	// The container has no network of its own: direct egress, SSH
	// included, is impossible, and the proxy refuses CONNECT to port 22.
	res, err = c.Exec(ctx, "timeout 5 bash -c 'exec 3<>/dev/tcp/140.82.112.3/22'", 30*time.Second)
	if err != nil || res.ExitCode == 0 {
		t.Fatalf("direct egress to port 22 should fail: %+v %v", res, err)
	}
	res, err = c.Exec(ctx, "apt-get update -q && apt-get install -y -q curl ca-certificates git >/dev/null", 5*time.Minute)
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("apt through proxy: %+v %v", res, err)
	}
	res, err = c.Exec(ctx, "curl -sS -o /dev/null -w '%{http_code}' -x $HTTPS_PROXY --cacert "+CAPath+" https://github.com:22/ 2>&1; echo", 30*time.Second)
	if err != nil || !strings.Contains(res.Stdout, "403") {
		t.Fatalf("CONNECT to 22 should be refused: %+v %v", res, err)
	}
	if misses.Load() == 0 {
		t.Fatal("apt traffic did not go through the proxy")
	}
	// HTTPS through the interception, twice: the second is a hit. curl
	// trusts the CA via CURL_CA_BUNDLE and, after ca-certificates, the
	// system store too.
	const u = "https://proxy.golang.org/golang.org/x/text/@v/v0.3.0.info"
	for i, want := range []string{"MISS", "HIT"} {
		res, err = c.Exec(ctx, "curl -sS -D - -o /dev/null "+u+" | grep -i "+proxy.CacheHeader, time.Minute)
		if err != nil || res.ExitCode != 0 || !strings.Contains(res.Stdout, want) {
			t.Fatalf("https fetch %d: %+v %v", i, res, err)
		}
	}
	res, err = c.Exec(ctx, "env -u CURL_CA_BUNDLE -u SSL_CERT_FILE curl -sS -o /dev/null -w '%{http_code}' "+u, time.Minute)
	if err != nil || res.ExitCode != 0 || res.Stdout != "200" {
		t.Fatalf("system trust store should include the CA: %+v %v", res, err)
	}
	// git over HTTPS is served from a mirror the proxy keeps; the second
	// clone does not touch the origin.
	var gitStates atomic.Int64
	prev := srv.Log
	srv.Log = func(f string, a ...any) {
		if strings.Contains(fmt.Sprintf(f, a...), "proxy GIT-") {
			gitStates.Add(1)
		}
		prev(f, a...)
	}
	for i := range 2 {
		res, err = c.Exec(ctx, fmt.Sprintf("rm -rf s%d && git clone -q https://github.com/golang/sync s%d && ls s%d/errgroup", i, i, i), 3*time.Minute)
		if err != nil || res.ExitCode != 0 {
			t.Fatalf("git clone %d through proxy: %+v %v", i, res, err)
		}
	}
	if gitStates.Load() == 0 {
		t.Fatal("git traffic was not served from the mirror")
	}
	if _, err := os.Stat(filepath.Join(dir, "cache", "git")); err != nil {
		t.Fatal("no git mirror directory")
	}
}
