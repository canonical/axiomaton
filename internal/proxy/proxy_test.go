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

package proxy

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// origin is a fake upstream counting requests per path.
type origin struct {
	hits  atomic.Int64
	index atomic.Int64
}

func (o *origin) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/pool/main/x/x_1.0.deb", func(w http.ResponseWriter, r *http.Request) {
		o.hits.Add(1)
		w.Header().Set("Content-Type", "application/octet-stream")
		io.WriteString(w, strings.Repeat("deb", 100))
	})
	mux.HandleFunc("/simple/pkg/", func(w http.ResponseWriter, r *http.Request) {
		o.index.Add(1)
		w.Header().Set("Etag", `"v1"`)
		w.Header().Set("Content-Type", "text/html")
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		io.WriteString(w, "<a>pkg-1.0.whl</a>")
	})
	mux.HandleFunc("/private", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		io.WriteString(w, "secret "+r.Header.Get("Accept-Encoding"))
	})
	mux.HandleFunc("/big.tar.gz", func(w http.ResponseWriter, r *http.Request) {
		o.hits.Add(1)
		io.WriteString(w, strings.Repeat("x", 2048))
	})
	mux.HandleFunc("/post", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		w.Write([]byte("echo:" + string(b)))
	})
	mux.HandleFunc("/missing", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	return mux
}

var upstreamPort string

func newProxy(t *testing.T, upstreamTLS *x509.CertPool) (*Server, *Cache, *http.Client) {
	t.Helper()
	dir := t.TempDir()
	ca, err := LoadOrCreateCA(filepath.Join(dir, "ca"))
	if err != nil {
		t.Fatal(err)
	}
	cache := &Cache{Dir: filepath.Join(dir, "cache"), MaxObject: 1024}
	s := &Server{CA: ca, Cache: cache, Token: NewToken(), Log: t.Logf}
	if upstreamTLS != nil {
		s.Transport = &http.Transport{TLSClientConfig: &tls.Config{RootCAs: upstreamTLS}, DisableCompression: true}
		s.ConnectPorts = append([]string{"443"}, upstreamPort)
	}
	addr, err := s.Start("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	pool := x509.NewCertPool()
	pool.AddCert(ca.Certificate())
	proxyURL, _ := url.Parse(s.URL(addr))
	if proxyURL.Host != addr || proxyURL.User.Username() != User {
		t.Fatalf("proxy url %s for %s", proxyURL, addr)
	}
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL), TLSClientConfig: &tls.Config{RootCAs: pool}, DisableCompression: true}}
	return s, cache, client
}

func get(t *testing.T, c *http.Client, u string) (*http.Response, string) {
	t.Helper()
	resp, err := c.Get(u)
	if err != nil {
		t.Fatalf("GET %s: %v", u, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func TestCAPersists(t *testing.T) {
	dir := t.TempDir()
	ca1, err := LoadOrCreateCA(dir)
	if err != nil {
		t.Fatal(err)
	}
	ca2, err := LoadOrCreateCA(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !ca1.Certificate().Equal(ca2.Certificate()) {
		t.Fatal("CA should be reloaded, not regenerated")
	}
	if info, _ := os.Stat(ca1.KeyPath); info.Mode().Perm() != 0o600 {
		t.Fatalf("key perms %v", info.Mode().Perm())
	}
	leaf, err := ca1.Leaf("example.com:443")
	if err != nil || leaf.Leaf.DNSNames[0] != "example.com" {
		t.Fatalf("leaf: %v %+v", err, leaf)
	}
	again, _ := ca1.Leaf("example.com")
	if again != leaf {
		t.Fatal("leaf should be cached")
	}
	ip, _ := ca1.Leaf("10.0.2.2")
	if len(ip.Leaf.IPAddresses) != 1 {
		t.Fatal("ip leaf")
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca1.Certificate())
	if _, err := leaf.Leaf.Verify(x509.VerifyOptions{DNSName: "example.com", Roots: pool}); err != nil {
		t.Fatal(err)
	}
	// A corrupt key forces regeneration.
	os.WriteFile(ca1.KeyPath, []byte("junk"), 0o600)
	ca3, err := LoadOrCreateCA(dir)
	if err != nil || ca3.Certificate().Equal(ca1.Certificate()) {
		t.Fatalf("regenerate: %v", err)
	}
}

func TestPlainHTTPCaching(t *testing.T) {
	o := &origin{}
	up := httptest.NewServer(o.handler())
	defer up.Close()
	_, cache, client := newProxy(t, nil)

	resp, body := get(t, client, up.URL+"/pool/main/x/x_1.0.deb")
	if resp.Header.Get(CacheHeader) != "MISS" || len(body) != 300 || resp.Header.Get("Content-Type") != "application/octet-stream" {
		t.Fatalf("first: %s %d", resp.Header.Get(CacheHeader), len(body))
	}
	resp, body = get(t, client, up.URL+"/pool/main/x/x_1.0.deb")
	if resp.Header.Get(CacheHeader) != "HIT" || len(body) != 300 || resp.ContentLength != 300 || o.hits.Load() != 1 {
		t.Fatalf("second: %s %d hits=%d", resp.Header.Get(CacheHeader), len(body), o.hits.Load())
	}

	// A mutable index is revalidated once stale, and served stale when the
	// origin is gone.
	resp, _ = get(t, client, up.URL+"/simple/pkg/")
	if resp.Header.Get(CacheHeader) != "MISS" {
		t.Fatal("index miss")
	}
	resp, _ = get(t, client, up.URL+"/simple/pkg/")
	if resp.Header.Get(CacheHeader) != "HIT" || o.index.Load() != 1 {
		t.Fatal("index hit within ttl")
	}
	cache.Now = func() time.Time { return time.Now().Add(time.Hour) }
	resp, body = get(t, client, up.URL+"/simple/pkg/")
	if resp.Header.Get(CacheHeader) != "REVALIDATED" || o.index.Load() != 2 || body != "<a>pkg-1.0.whl</a>" {
		t.Fatalf("revalidate: %s hits=%d body=%q", resp.Header.Get(CacheHeader), o.index.Load(), body)
	}
	cache.Now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	up.Close()
	resp, body = get(t, client, strings.Replace(up.URL, "http://", "http://", 1)+"/simple/pkg/")
	if resp.Header.Get(CacheHeader) != "STALE" || body != "<a>pkg-1.0.whl</a>" {
		t.Fatalf("stale: %s %q", resp.Header.Get(CacheHeader), body)
	}
}

func TestBypassAndLimits(t *testing.T) {
	o := &origin{}
	up := httptest.NewServer(o.handler())
	defer up.Close()
	_, _, client := newProxy(t, nil)

	resp, body := get(t, client, up.URL+"/private")
	if resp.Header.Get(CacheHeader) != "BYPASS" || !strings.HasPrefix(body, "secret") {
		t.Fatalf("no-store: %s %q", resp.Header.Get(CacheHeader), body)
	}
	if strings.Contains(body, "gzip") {
		t.Fatal("Accept-Encoding should be stripped for cacheable GETs")
	}
	// Over the size limit: served but not stored (the origin announces the
	// length, so the proxy knows up front and bypasses the cache).
	for i := 0; i < 2; i++ {
		resp, body = get(t, client, up.URL+"/big.tar.gz")
		if resp.Header.Get(CacheHeader) != "BYPASS" || len(body) != 2048 {
			t.Fatalf("big %d: %s %d", i, resp.Header.Get(CacheHeader), len(body))
		}
	}
	if o.hits.Load() != 2 {
		t.Fatalf("oversize body should not be cached: %d", o.hits.Load())
	}
	resp, _ = get(t, client, up.URL+"/missing")
	if resp.StatusCode != 404 || resp.Header.Get(CacheHeader) != "BYPASS" {
		t.Fatalf("404: %d", resp.StatusCode)
	}
	pr, err := client.Post(up.URL+"/post", "text/plain", strings.NewReader("hi"))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(pr.Body)
	if string(b) != "echo:hi" || pr.Header.Get(CacheHeader) != "BYPASS" {
		t.Fatalf("post: %q %s", b, pr.Header.Get(CacheHeader))
	}

	// Without the token the proxy refuses.
	s, _, _ := newProxy(t, nil)
	bare, _ := url.Parse("http://127.0.0.1:" + itoa(s.Port()))
	anon := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(bare)}}
	resp, err = anon.Get(up.URL + "/missing")
	if err != nil || resp.StatusCode != http.StatusProxyAuthRequired {
		t.Fatalf("anonymous: %v %v", err, resp)
	}
}

func TestHTTPSInterception(t *testing.T) {
	o := &origin{}
	up := httptest.NewTLSServer(o.handler())
	defer up.Close()
	pool := x509.NewCertPool()
	pool.AddCert(up.Certificate())
	_, upstreamPort, _ = net.SplitHostPort(strings.TrimPrefix(up.URL, "https://"))
	s, _, client := newProxy(t, pool)

	// CONNECT to anything but an HTTPS port is refused: with the proxy as
	// the container's only way out, that is what keeps SSH in.
	s.ConnectPorts = nil
	if _, err := client.Get(up.URL + "/missing"); err == nil || !strings.Contains(err.Error(), "Forbidden") {
		t.Fatalf("CONNECT to a non-443 port should be refused: %v", err)
	}
	s.ConnectPorts = []string{"443", upstreamPort}

	resp, body := get(t, client, up.URL+"/pool/main/x/x_1.0.deb")
	if resp.Header.Get(CacheHeader) != "MISS" || len(body) != 300 || resp.TLS == nil {
		t.Fatalf("https first: %s %d", resp.Header.Get(CacheHeader), len(body))
	}
	if resp.TLS.PeerCertificates[0].Issuer.CommonName != "axiomaton cache proxy CA" {
		t.Fatalf("issuer: %s", resp.TLS.PeerCertificates[0].Issuer.CommonName)
	}
	// Two requests on the intercepted connection, the second a hit.
	resp, _ = get(t, client, up.URL+"/pool/main/x/x_1.0.deb")
	if resp.Header.Get(CacheHeader) != "HIT" || o.hits.Load() != 1 {
		t.Fatalf("https hit: %s hits=%d", resp.Header.Get(CacheHeader), o.hits.Load())
	}
	// A client that does not trust the CA fails the handshake.
	distrust := &http.Client{Transport: &http.Transport{Proxy: client.Transport.(*http.Transport).Proxy}}
	if _, err := distrust.Get(up.URL + "/missing"); err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("untrusted client should fail: %v", err)
	}
}

func TestRules(t *testing.T) {
	for u, want := range map[string]bool{
		"http://archive.ubuntu.com/ubuntu/pool/main/c/curl/curl_8.5.0-2ubuntu10_amd64.deb":         true,
		"http://archive.ubuntu.com/ubuntu/dists/noble/InRelease":                                   false,
		"https://proxy.golang.org/golang.org/x/net/@v/list":                                        false,
		"https://proxy.golang.org/golang.org/x/net/@v/v0.50.0.zip":                                 true,
		"https://proxy.golang.org/golang.org/x/net/@v/v0.50.0.info":                                true,
		"https://proxy.golang.org/golang.org/x/net/@latest":                                        false,
		"https://pypi.org/simple/requests/":                                                        false,
		"https://files.pythonhosted.org/packages/ab/cd/requests-2.32.3-py3-none-any.whl":           true,
		"https://go.dev/dl/go1.27.2.linux-amd64.tar.gz":                                            true,
		"https://go.dev/dl/?mode=json":                                                             false,
		"https://ziglang.org/download/index.json":                                                  false,
		"https://ziglang.org/download/0.14.0/zig-linux-x86_64-0.14.0.tar.xz":                       true,
		"https://static.rust-lang.org/dist/channel-rust-stable.toml":                               false,
		"https://static.rust-lang.org/dist/2025-03-05/rust-1.85.0-x86_64-unknown-linux-gnu.tar.xz": true,
	} {
		if Immutable(u) != want {
			t.Errorf("Immutable(%s) = %v, want %v", u, !want, want)
		}
	}
	h := http.Header{"Cache-Control": {"public, max-age=86400"}}
	if TTL("https://pypi.org/simple/x/", h) != 24*time.Hour || TTL("https://pypi.org/simple/x/", nil) != mutableTTL || TTL("https://x/y.deb", h) != immutableTTL {
		t.Fatal("ttl")
	}
	if Cacheable("u", 200, http.Header{"Cache-Control": {"no-store"}}) || Cacheable("u", 206, nil) || !Cacheable("u", 200, nil) {
		t.Fatal("cacheable")
	}
	gw, err := ReadGateway("Iface\tDestination\tGateway \tFlags\nenp1s0\t00000000\t0102A8C0\t0003\nenp1s0\t0002A8C0\t00000000\t0001\n")
	if err != nil || gw != "192.168.2.1" {
		t.Fatalf("gateway: %s %v", gw, err)
	}
	if _, err := ReadGateway("Iface\tDestination\tGateway\n"); err == nil {
		t.Fatal("no default route should error")
	}
	hosts := "127.0.0.1\tlocalhost\n# comment\n169.254.1.2\thost.containers.internal host.docker.internal\n172.21.36.190\tca6c9e8f25cc crazy\n"
	if HostsEntry(hosts, "host.containers.internal") != "169.254.1.2" || HostsEntry(hosts, "crazy") != "172.21.36.190" || HostsEntry(hosts, "nope") != "" {
		t.Fatal("hosts entry")
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

// TestSharedCache runs two proxies over one cache directory, as two ax
// processes would, and checks that concurrent requests for the same file
// download it once and that both see each other's entries.
func TestSharedCache(t *testing.T) {
	o := &origin{}
	slow := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/pool/slow.deb", func(w http.ResponseWriter, r *http.Request) {
		o.hits.Add(1)
		<-slow
		io.WriteString(w, "payload")
	})
	mux.Handle("/", o.handler())
	up := httptest.NewServer(mux)
	defer up.Close()

	dir := t.TempDir()
	ca, _ := LoadOrCreateCA(filepath.Join(dir, "ca"))
	var clients []*http.Client
	for i := 0; i < 2; i++ {
		s := &Server{CA: ca, Cache: &Cache{Dir: filepath.Join(dir, "cache")}, Token: "t", Log: t.Logf}
		addr, err := s.Start("127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		pu, _ := url.Parse(s.URL(addr))
		clients = append(clients, &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pu), DisableCompression: true}})
	}
	results := make(chan string, 2)
	for _, c := range clients {
		go func(c *http.Client) {
			resp, body := get(t, c, up.URL+"/pool/slow.deb")
			results <- resp.Header.Get(CacheHeader) + ":" + body
		}(c)
	}
	time.Sleep(200 * time.Millisecond) // let both reach the lock
	close(slow)
	got := []string{<-results, <-results}
	sort.Strings(got)
	if strings.Join(got, " ") != "HIT:payload MISS:payload" || o.hits.Load() != 1 {
		t.Fatalf("shared download: %v hits=%d", got, o.hits.Load())
	}
	// An entry written by one proxy is a hit for the other.
	get(t, clients[0], up.URL+"/pool/main/x/x_1.0.deb")
	if resp, _ := get(t, clients[1], up.URL+"/pool/main/x/x_1.0.deb"); resp.Header.Get(CacheHeader) != "HIT" {
		t.Fatalf("second proxy should hit: %s", resp.Header.Get(CacheHeader))
	}
}

func TestCAConcurrentCreate(t *testing.T) {
	dir := t.TempDir()
	cas := make(chan *CA, 4)
	for i := 0; i < 4; i++ {
		go func() {
			ca, err := LoadOrCreateCA(dir)
			if err != nil {
				t.Error(err)
			}
			cas <- ca
		}()
	}
	first := <-cas
	for i := 0; i < 3; i++ {
		if ca := <-cas; ca == nil || first == nil || !ca.Certificate().Equal(first.Certificate()) {
			t.Fatal("concurrent creation should yield one CA")
		}
	}
}

func TestForward(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "p.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { io.Copy(c, c); c.Close() }()
		}
	}()
	// Pick a free port, then forward it to the socket.
	tl, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := tl.Addr().String()
	tl.Close()
	quitR, quitW := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- Forward(context.Background(), addr, sock, quitR) }()
	var conn net.Conn
	for i := 0; i < 50 && conn == nil; i++ {
		conn, err = net.Dial("tcp", addr)
		if err != nil {
			time.Sleep(20 * time.Millisecond)
		}
	}
	if conn == nil {
		t.Fatal(err)
	}
	io.WriteString(conn, "ping")
	buf := make([]byte, 4)
	if _, err := io.ReadFull(conn, buf); err != nil || string(buf) != "ping" {
		t.Fatalf("relay: %q %v", buf, err)
	}
	conn.Close()
	// Closing the quit pipe (the parent going away) stops the forwarder.
	quitW.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("forwarder did not exit")
	}
}

func TestUnixSocket(t *testing.T) {
	o := &origin{}
	up := httptest.NewServer(o.handler())
	defer up.Close()
	dir := t.TempDir()
	ca, _ := LoadOrCreateCA(filepath.Join(dir, "ca"))
	s := &Server{CA: ca, Cache: &Cache{Dir: filepath.Join(dir, "cache")}, Token: NewToken(), Log: t.Logf}
	sock := filepath.Join(dir, "p.sock")
	if err := s.StartUnix(sock); err != nil {
		t.Fatal(err)
	}
	if s.SocketPath() != sock || s.Port() != 0 {
		t.Fatalf("socket %q port %d", s.SocketPath(), s.Port())
	}
	if info, _ := os.Stat(sock); info.Mode().Perm() != 0o600 {
		t.Fatalf("socket perms %v", info.Mode().Perm())
	}
	tl, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := tl.Addr().String()
	tl.Close()
	go Forward(context.Background(), addr, sock, nil)
	proxyURL, _ := url.Parse(s.URL(addr))
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}
	var resp *http.Response
	var err error
	for i := 0; i < 50; i++ {
		if resp, err = client.Get(up.URL + "/pool/main/x/x_1.0.deb"); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil || resp.Header.Get(CacheHeader) != "MISS" {
		t.Fatalf("through socket: %v", err)
	}
	s.Close()
	if _, err := os.Stat(sock); err == nil {
		t.Fatal("socket should be removed on close")
	}
}

// gitUpstream serves a bare repository over smart HTTP with git
// http-backend, standing in for a hosting service.
func gitUpstream(t *testing.T) (*httptest.Server, string, func(msg string)) {
	t.Helper()
	git := gitBinary()
	if git == "" {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	work := filepath.Join(root, "work")
	bare := filepath.Join(root, "repo.git")
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command(git, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@x", "GIT_CONFIG_NOSYSTEM=1", "HOME="+root)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run(root, "init", "-q", "-b", "main", work)
	os.WriteFile(filepath.Join(work, "a.txt"), []byte("one\n"), 0o644)
	run(work, "add", "a.txt")
	run(work, "commit", "-q", "-m", "one")
	run(root, "clone", "-q", "--bare", work, bare)
	commit := func(msg string) {
		os.WriteFile(filepath.Join(work, "a.txt"), []byte(msg+"\n"), 0o644)
		run(work, "commit", "-q", "-am", msg)
		run(work, "push", "-q", bare, "main")
	}
	srv := httptest.NewServer(&cgi.Handler{Path: git, Args: []string{"http-backend"}, Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"}})
	return srv, srv.URL + "/repo.git", commit
}

func TestGitMirror(t *testing.T) {
	up, repoURL, commit := gitUpstream(t)
	defer up.Close()
	s, cache, _ := newProxy(t, nil)
	var (
		states   []string
		statesMu sync.Mutex
	)
	s.Log = func(f string, a ...any) {
		line := fmt.Sprintf(f, a...)
		t.Log(line)
		if strings.Contains(line, "proxy GIT-") {
			statesMu.Lock()
			states = append(states, strings.Fields(line)[1])
			statesMu.Unlock()
		}
	}
	proxyURL := s.URL("127.0.0.1:" + itoa(s.Port()))
	home := t.TempDir()
	clone := func(dst string) ([]byte, error) {
		cmd := exec.Command(gitBinary(), "clone", "-q", repoURL, dst)
		cmd.Env = append(os.Environ(), "http_proxy="+proxyURL, "HTTP_PROXY="+proxyURL, "GIT_CONFIG_NOSYSTEM=1", "HOME="+home)
		return cmd.CombinedOutput()
	}
	// Two proxies on one cache (two ax processes) cloning at once: one
	// mirror, both clones succeed.
	s2 := &Server{CA: s.CA, Cache: cache, Token: "t2", Log: s.Log}
	addr2, _ := s2.Start("127.0.0.1:0")
	defer s2.Close()
	errs := make(chan error, 2)
	for i, pu := range []string{proxyURL, s2.URL(addr2)} {
		go func(i int, pu string) {
			cmd := exec.Command(gitBinary(), "clone", "-q", repoURL, filepath.Join(home, fmt.Sprintf("p%d", i)))
			cmd.Env = append(os.Environ(), "http_proxy="+pu, "GIT_CONFIG_NOSYSTEM=1", "HOME="+home)
			out, err := cmd.CombinedOutput()
			if err != nil {
				err = fmt.Errorf("%v: %s", err, out)
			}
			errs <- err
		}(i, pu)
	}
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent clone: %v", err)
		}
	}
	if n := strings.Count(strings.Join(states, " "), "GIT-MISS"); n != 1 {
		t.Fatalf("expected one mirror clone, got %d: %v", n, states)
	}
	if out, err := clone(filepath.Join(home, "c1")); err != nil {
		t.Fatalf("clone via proxy: %v\n%s", err, out)
	}
	mirror := filepath.Join(cache.Dir, "git", mirrorKey(repoURL))
	if _, err := os.Stat(filepath.Join(mirror, "HEAD")); err != nil {
		t.Fatalf("mirror missing: %v", err)
	}
	if out, err := clone(filepath.Join(home, "c2")); err != nil {
		t.Fatalf("second clone: %v\n%s", err, out)
	}
	// A new upstream commit is picked up once the mirror is stale.
	commit("two")
	cache.Now = func() time.Time { return time.Now().Add(time.Hour) }
	if out, err := clone(filepath.Join(home, "c3")); err != nil {
		t.Fatalf("third clone: %v\n%s", err, out)
	}
	if b, _ := os.ReadFile(filepath.Join(home, "c3", "a.txt")); string(b) != "two\n" {
		t.Fatalf("refreshed mirror should have the new commit, got %q", b)
	}
	// With the origin gone the stale mirror still serves clones.
	up.Close()
	cache.Now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	if out, err := clone(filepath.Join(home, "c4")); err != nil {
		t.Fatalf("offline clone: %v\n%s", err, out)
	}
	joined := strings.Join(states, " ")
	for _, want := range []string{"GIT-MISS", "GIT-HIT", "GIT-REVALIDATED", "GIT-STALE"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("states %q lack %s", joined, want)
		}
	}
	if mirrorKey("https://github.com/golang/net.git") != mirrorKey("http://GitHub.com/golang/net/") {
		t.Fatal("mirror key should normalise scheme, case and .git")
	}
	if r, suf, ok := gitRequest(httptest.NewRequest("GET", "https://h/o/r.git/info/refs?service=git-upload-pack", nil)); !ok || r != "/o/r.git" || suf != "/info/refs" {
		t.Fatalf("gitRequest: %q %q %v", r, suf, ok)
	}
	if _, _, ok := gitRequest(httptest.NewRequest("GET", "https://h/o/r.git/info/refs?service=git-receive-pack", nil)); ok {
		t.Fatal("pushes are not mirrored")
	}
}
