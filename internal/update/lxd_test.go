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
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/canonical/axiomaton/internal/proxy"
	"github.com/canonical/axiomaton/internal/subjects"
)

func TestResolveBackend(t *testing.T) {
	defer func() { lookPath = exec.LookPath }()
	have := map[string]bool{}
	lookPath = func(name string) (string, error) {
		if have[name] {
			return "/bin/" + name, nil
		}
		return "", errors.New("not found")
	}
	type tc struct {
		podman, lxc bool
		configured  string
		vm          bool
		want        Backend
		errPart     string
	}
	for _, c := range []tc{
		{true, true, "auto", false, BackendPodman, ""},
		{false, true, "auto", false, BackendLXD, ""},
		{false, true, "", false, BackendLXD, ""},
		{false, false, "auto", false, "", "neither podman nor lxc"},
		{true, true, "lxd", false, BackendLXD, ""},
		{true, true, "LXD-VM", false, BackendLXDVM, ""},
		{true, false, "lxd", false, "", "lxc is not on PATH"},
		{false, true, "podman", false, "", "podman is not on PATH"},
		{true, true, "docker", false, "", "unknown update.backend"},
		{true, true, "podman", true, BackendLXDVM, ""},
		{true, false, "podman", true, "", "need a virtual machine"},
	} {
		have["podman"], have["lxc"] = c.podman, c.lxc
		got, err := ResolveBackend(c.configured, c.vm)
		if c.errPart != "" {
			if err == nil || !strings.Contains(err.Error(), c.errPart) {
				t.Errorf("%+v: err %v", c, err)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%+v: got %q %v", c, got, err)
		}
	}
	if subjects.RequiresVM(vmSubject{}) != true {
		t.Fatal("RequiresVM")
	}
	if b, _ := subjects.Lookup("go"); subjects.RequiresVM(b) {
		t.Fatal("go does not need a VM")
	}
}

// vmSubject is a built-in that asks for a virtual machine.
type vmSubject struct{ subjects.Builtin }

func (vmSubject) RequiresVM() bool { return true }

// fakeLXC writes a stand-in lxc script that records its arguments, runs
// exec'd commands on the host inside a fake root, pretends the isolated
// network sits on 127.0.0.1 and copies pushed files into the fake root.
func fakeLXC(t *testing.T) (bin, root, log string) {
	t.Helper()
	dir := t.TempDir()
	root = filepath.Join(dir, "root")
	os.MkdirAll(root, 0o755)
	log = filepath.Join(dir, "lxc.log")
	bin = filepath.Join(dir, "lxc")
	script := `#!/bin/bash
echo "$*" >> "$FAKE_LXC_LOG"
R="$FAKE_LXC_ROOT"
case "$1" in
  launch|delete) exit 0 ;;
  network)
    case "$2" in
      show) [ -n "$FAKE_LXC_NETWORK_EXISTS" ] ;;
      get) echo "127.0.0.1/24" ;;
      acl) if [ "$3" = show ]; then [ -n "$FAKE_LXC_NETWORK_EXISTS" ]; else exit 0; fi ;;
      *) exit 0 ;;
    esac ;;
  file)
    src="${@: -2:1}"; dst="${@: -1}"; path="${dst#*/}"
    mkdir -p "$R/$(dirname "$path")" && cp "$src" "$R/$path" ;;
  exec)
    shift 2; cwd=""; envs=()
    while [ "$1" != "--" ]; do
      case "$1" in --cwd) cwd="$2"; shift 2 ;; --env) envs+=("$2"); shift 2 ;; *) shift ;; esac
    done
    shift; cmd="$3"
    case "$cmd" in *1.1.1.1*) exit 1 ;; esac   # the fake instance has no internet
    cmd="${cmd//\/work/$R/work}"; cmd="${cmd//\/etc\//$R/etc/}"; cmd="${cmd//\/usr\/local\/share/$R/usr/local/share}"
    if [ -n "$cwd" ]; then mkdir -p "$R$cwd"; cd "$R$cwd"; fi
    env "${envs[@]}" bash -c "$cmd" ;;
  *) echo "unexpected: $*" >&2; exit 2 ;;
esac
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_LXC_LOG", log)
	t.Setenv("FAKE_LXC_ROOT", root)
	return bin, root, log
}

func TestLXDRuntime(t *testing.T) {
	bin, root, logPath := fakeLXC(t)
	ctx := context.Background()

	// Without a proxy: plain launch, exec with cwd and env, delete.
	rt := LXD{Image: "ubuntu:24.04", Binary: bin, Log: t.Logf}
	c, err := rt.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	res, err := c.Exec(ctx, "pwd; echo $DEBIAN_FRONTEND; exit 3", 10*time.Second)
	if err != nil || res.ExitCode != 3 || !strings.HasSuffix(strings.Fields(res.Stdout)[0], "/work") || !strings.Contains(res.Stdout, "noninteractive") {
		t.Fatalf("exec: %+v %v", res, err)
	}
	if res, _ := c.Exec(ctx, "sleep 5", 200*time.Millisecond); !res.TimedOut {
		t.Fatal("timeout not reported")
	}
	if err := c.Close(ctx); err != nil {
		t.Fatal(err)
	}
	log, _ := os.ReadFile(logPath)
	if !strings.Contains(string(log), "launch ubuntu:24.04 axiomaton-") || strings.Contains(string(log), "--vm") || strings.Contains(string(log), "--network") || !strings.Contains(string(log), "delete --force axiomaton-") {
		t.Fatalf("lxc calls:\n%s", log)
	}

	// A VM launch carries --vm and resource limits.
	os.WriteFile(logPath, nil, 0o644)
	vm := LXD{Image: "ubuntu:24.04", VM: true, Binary: bin}
	if c, err := vm.Start(ctx); err != nil {
		t.Fatal(err)
	} else {
		c.Close(ctx)
	}
	log, _ = os.ReadFile(logPath)
	if !strings.Contains(string(log), "--vm -c limits.memory=4GiB -c limits.cpu=2") {
		t.Fatalf("vm launch:\n%s", log)
	}

	// With a proxy: the isolated network and ACL are created once, the
	// instance is launched on it, the CA is pushed and every exec carries
	// the proxy environment.
	os.WriteFile(logPath, nil, 0o644)
	lxdNetAddr = map[string]string{}
	ca, _ := proxy.LoadOrCreateCA(filepath.Join(t.TempDir(), "ca"))
	srv := &proxy.Server{CA: ca, Cache: &proxy.Cache{Dir: t.TempDir()}, Token: "tok"}
	defer srv.Close()
	iso := LXD{Image: "ubuntu:24.04", Binary: bin, Proxy: srv, Log: t.Logf}
	c, err = iso.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(ctx)
	log, _ = os.ReadFile(logPath)
	for _, want := range []string{
		"network create axiomaton ipv4.address=auto ipv4.nat=false ipv4.routing=false ipv6.address=none",
		"network acl create axiomaton",
		"network acl rule add axiomaton egress action=allow protocol=tcp destination=127.0.0.1/32 destination_port=31280-31299",
		"network set axiomaton security.acls=axiomaton security.acls.default.egress.action=drop",
		"--network axiomaton",
		"file push --create-dirs --mode 0644",
	} {
		if !strings.Contains(string(log), want) {
			t.Fatalf("missing %q in:\n%s", want, log)
		}
	}
	if port := srv.Port(); port < ProxyPortFrom || port > ProxyPortTo {
		t.Fatalf("proxy port %d outside the ACL range", port)
	}
	res, err = c.Exec(ctx, "echo $HTTPS_PROXY; echo $SSL_CERT_FILE; cat /etc/apt/apt.conf.d/99axiomaton-proxy", 10*time.Second)
	if err != nil || res.ExitCode != 0 || !strings.Contains(res.Stdout, "http://ax:tok@127.0.0.1:") || !strings.Contains(res.Stdout, CAPath) || !strings.Contains(res.Stdout, "Acquire::https::Proxy") {
		t.Fatalf("proxy env: %+v %v", res, err)
	}
	if b, err := os.ReadFile(filepath.Join(root, CAPath)); err != nil || string(b) != string(ca.CertPEM) {
		t.Fatalf("CA not installed: %v", err)
	}
	// The network is set up once per process.
	os.WriteFile(logPath, nil, 0o644)
	if c2, err := iso.Start(ctx); err != nil {
		t.Fatal(err)
	} else {
		c2.Close(ctx)
	}
	log, _ = os.ReadFile(logPath)
	if strings.Contains(string(log), "network show") || strings.Contains(string(log), "network create") || strings.Contains(string(log), "network acl") {
		t.Fatalf("network setup should run once per process:\n%s", log)
	}
}

func TestLXDConsent(t *testing.T) {
	bin, _, logPath := fakeLXC(t)
	ctx := context.Background()
	ca, _ := proxy.LoadOrCreateCA(filepath.Join(t.TempDir(), "ca"))
	srv := &proxy.Server{CA: ca, Cache: &proxy.Cache{Dir: t.TempDir()}, Token: "tok"}
	defer srv.Close()

	// Denied: nothing is launched and no network is touched.
	lxdNetAddr = map[string]string{}
	var asked []ConsentRequest
	deny := func(_ context.Context, req ConsentRequest) error {
		asked = append(asked, req)
		return errors.New("no")
	}
	rt := LXD{Image: "ubuntu:24.04", VM: true, Binary: bin, Proxy: srv, Consent: deny}
	if _, err := rt.Start(ctx); err == nil || !strings.Contains(err.Error(), "run agents in LXD virtual machines: no") {
		t.Fatalf("denied start: %v", err)
	}
	if log, _ := os.ReadFile(logPath); len(log) != 0 {
		t.Fatalf("lxc ran without consent:\n%s", log)
	}
	if len(asked) != 1 || asked[0].Key != "lxd" || !strings.Contains(strings.Join(asked[0].Details, "\n"), "4GiB of memory") {
		t.Fatalf("asked: %+v", asked)
	}

	// Granted for instances, denied for the network: the network is not
	// created and the instance is not launched.
	asked = nil
	rt.Consent = func(ctx context.Context, req ConsentRequest) error {
		asked = append(asked, req)
		if req.Key == "lxd-network" {
			return errors.New("not the network")
		}
		return nil
	}
	if _, err := rt.Start(ctx); err == nil || !strings.Contains(err.Error(), "create the isolated LXD network axiomaton: not the network") {
		t.Fatalf("denied network: %v", err)
	}
	log, _ := os.ReadFile(logPath)
	if strings.Contains(string(log), "network create") || strings.Contains(string(log), "launch") || !strings.Contains(string(log), "network show axiomaton") {
		t.Fatalf("lxc calls after network denial:\n%s", log)
	}
	if len(asked) != 2 || asked[0].Key != "lxd" || asked[1].Key != "lxd-network" || !strings.Contains(strings.Join(asked[1].Details, "\n"), "ports 31280-31299") {
		t.Fatalf("asked: %+v", asked)
	}

	// Both granted: each key is asked before the command it covers; an
	// existing network is not asked about at all.
	os.WriteFile(logPath, nil, 0o644)
	asked = nil
	rt.Consent = func(_ context.Context, req ConsentRequest) error {
		log, _ := os.ReadFile(logPath)
		if (req.Key == "lxd" && len(log) != 0) || strings.Contains(string(log), "network create") || strings.Contains(string(log), "launch") {
			t.Errorf("%s asked too late:\n%s", req.Key, log)
		}
		asked = append(asked, req)
		return nil
	}
	rt.VM = false
	c, err := rt.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	c.Close(ctx)
	if len(asked) != 2 || asked[0].Key != "lxd" || asked[1].Key != "lxd-network" {
		t.Fatalf("asked: %+v", asked)
	}
	lxdNetAddr = map[string]string{}
	t.Setenv("FAKE_LXC_NETWORK_EXISTS", "1")
	os.WriteFile(logPath, nil, 0o644)
	asked = nil
	if c, err := rt.Start(ctx); err != nil {
		t.Fatal(err)
	} else {
		c.Close(ctx)
	}
	if len(asked) != 1 || asked[0].Key != "lxd" {
		t.Fatalf("existing network should not be asked about: %+v", asked)
	}

	// Without a proxy the request says so and no network is involved.
	os.WriteFile(logPath, nil, 0o644)
	asked = nil
	plain := LXD{Image: "ubuntu:24.04", Binary: bin, Consent: rt.Consent}
	if c, err := plain.Start(ctx); err != nil {
		t.Fatal(err)
	} else {
		c.Close(ctx)
	}
	if len(asked) != 1 || !strings.Contains(strings.Join(asked[0].Details, "\n"), "unrestricted") {
		t.Fatalf("plain: %+v", asked)
	}
}

func TestPodmanConsent(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "podman")
	os.WriteFile(bin, []byte("#!/bin/sh\necho \"$*\" >> \"$FAKE_PODMAN_LOG\"; exit 1\n"), 0o755)
	logPath := filepath.Join(dir, "podman.log")
	t.Setenv("FAKE_PODMAN_LOG", logPath)
	var asked []ConsentRequest
	rt := Podman{Image: "img:1", Binary: bin, Consent: func(_ context.Context, req ConsentRequest) error {
		asked = append(asked, req)
		return errors.New("no")
	}}
	if _, err := rt.Start(context.Background()); err == nil || !strings.Contains(err.Error(), "run agents in podman containers: no") {
		t.Fatalf("denied: %v", err)
	}
	if _, err := os.Stat(logPath); err == nil {
		t.Fatal("podman ran without consent")
	}
	if len(asked) != 1 || asked[0].Key != "podman" || !strings.Contains(strings.Join(asked[0].Details, "\n"), "pull the image img:1") || !strings.Contains(strings.Join(asked[0].Details, "\n"), "unrestricted") {
		t.Fatalf("asked: %+v", asked)
	}
	// Granted: podman is run (and here fails, which is the fake's doing).
	rt.Consent = func(context.Context, ConsentRequest) error { return nil }
	if _, err := rt.Start(context.Background()); err == nil || !strings.Contains(err.Error(), "podman run img:1") {
		t.Fatalf("granted: %v", err)
	}
	if log, _ := os.ReadFile(logPath); !strings.Contains(string(log), "run -d --rm --name axiomaton-") {
		t.Fatalf("podman log:\n%s", log)
	}
}

// TestLXDIntegration launches a real LXD container (AX_LXD_TEST=1) or
// virtual machine (AX_LXD_VM_TEST=1) on the isolated network and checks
// that only the proxy is reachable from it.
func TestLXDIntegration(t *testing.T) {
	vm := os.Getenv("AX_LXD_VM_TEST") == "1"
	if os.Getenv("AX_LXD_TEST") != "1" && !vm {
		t.Skip("set AX_LXD_TEST=1 or AX_LXD_VM_TEST=1 to run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	dir := t.TempDir()
	ca, err := proxy.LoadOrCreateCA(filepath.Join(dir, "ca"))
	if err != nil {
		t.Fatal(err)
	}
	srv := &proxy.Server{CA: ca, Cache: &proxy.Cache{Dir: filepath.Join(dir, "cache")}, Token: proxy.NewToken(), Log: t.Logf}
	defer srv.Close()
	rt := LXD{Image: "ubuntu:24.04", VM: vm, Proxy: srv, Log: t.Logf}
	c, err := rt.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(ctx)
	res, err := c.Exec(ctx, "timeout 5 bash -c 'exec 3<>/dev/tcp/140.82.112.3/22'", 30*time.Second)
	if err != nil || res.ExitCode == 0 {
		t.Fatalf("direct egress to port 22 should fail: %+v %v", res, err)
	}
	res, err = c.Exec(ctx, "apt-get update -q >/dev/null && apt-get install -y -q curl >/dev/null && curl -sS -D - -o /dev/null https://proxy.golang.org/golang.org/x/text/@v/v0.3.0.info | grep -i "+proxy.CacheHeader, 5*time.Minute)
	if err != nil || res.ExitCode != 0 || !strings.Contains(res.Stdout, "MISS") && !strings.Contains(res.Stdout, "HIT") {
		t.Fatalf("apt and https through proxy: %+v %v", res, err)
	}
	res, err = c.Exec(ctx, "curl -sS -o /dev/null -w '%{http_code}' https://github.com:22/ 2>&1; echo", 30*time.Second)
	if err != nil || !strings.Contains(res.Stdout, "403") {
		t.Fatalf("CONNECT to 22 should be refused: %+v %v", res, err)
	}
}
