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
	"bufio"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Server is the caching forward proxy. Plain HTTP requests arrive with
// absolute URLs; HTTPS arrives as CONNECT and is intercepted with a
// certificate from the CA so the traffic can be cached as well.
type Server struct {
	CA    *CA
	Cache *Cache
	// Token is required in Proxy-Authorization (user "ax"); empty means
	// unauthenticated.
	Token string
	// Transport reaches origins; nil means a default that honours the
	// host's own proxy environment.
	Transport http.RoundTripper
	// Log receives one line per request; nil discards them.
	Log func(format string, args ...any)
	// ConnectPorts overrides DefaultConnectPorts.
	ConnectPorts []string

	mu    sync.Mutex
	lns   []net.Listener
	srv   *http.Server
	locks sync.Map // url -> *sync.Mutex, one download at a time per URL
}

// User is the user name in proxy URLs.
const User = "ax"

// CacheHeader names the response header that reports HIT, MISS,
// REVALIDATED, STALE or BYPASS.
const CacheHeader = "X-Axiomaton-Cache"

// NewToken returns a random proxy token.
func NewToken() string {
	var b [24]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func (s *Server) logf(format string, args ...any) {
	if s.Log != nil {
		s.Log(format, args...)
	}
}

func (s *Server) transport() http.RoundTripper {
	if s.Transport != nil {
		return s.Transport
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Transport == nil {
		s.Transport = &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          100,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   15 * time.Second,
			ExpectContinueTimeout: time.Second,
			DisableCompression:    true,
		}
	}
	return s.Transport
}

// Start listens on a TCP addr (host:port, port 0 for any) and serves in
// the background. It returns the bound address. A server may listen on
// several addresses and sockets at once.
func (s *Server) Start(addr string) (string, error) {
	ln, err := s.listen("tcp", addr)
	if err != nil {
		return "", err
	}
	return ln.Addr().String(), nil
}

// StartRange listens on host at the first free port in [from, to], so
// that a fixed firewall rule can cover every ax process on the machine.
func (s *Server) StartRange(host string, from, to int) (string, error) {
	var last error
	for port := from; port <= to; port++ {
		addr, err := s.Start(net.JoinHostPort(host, strconv.Itoa(port)))
		if err == nil {
			return addr, nil
		}
		last = err
	}
	return "", fmt.Errorf("no free port on %s between %d and %d: %w", host, from, to, last)
}

// StartUnix serves on a unix socket at path, which containers reach
// through a forwarder in their own network namespace (see Forward).
func (s *Server) StartUnix(path string) error {
	if s.SocketPath() != "" {
		return nil
	}
	os.Remove(path)
	_, err := s.listen("unix", path)
	return err
}

func (s *Server) listen(network, addr string) (net.Listener, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ln := range s.lns {
		if ln.Addr().Network() == network && ln.Addr().String() == addr {
			return ln, nil
		}
	}
	ln, err := net.Listen(network, addr)
	if err != nil {
		return nil, fmt.Errorf("proxy listen %s: %w", addr, err)
	}
	if network == "unix" {
		os.Chmod(addr, 0o600)
	}
	if s.srv == nil {
		s.srv = &http.Server{Handler: s, ReadHeaderTimeout: 30 * time.Second}
	}
	s.lns = append(s.lns, ln)
	go s.srv.Serve(ln)
	return ln, nil
}

// Port is the first listening TCP port, or 0 when there is none.
func (s *Server) Port() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ln := range s.lns {
		if a, ok := ln.Addr().(*net.TCPAddr); ok {
			return a.Port
		}
	}
	return 0
}

// SocketPath is the unix socket path, or "" when there is none.
func (s *Server) SocketPath() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ln := range s.lns {
		if a, ok := ln.Addr().(*net.UnixAddr); ok {
			return a.Name
		}
	}
	return ""
}

// URL is the proxy URL for a client that reaches the proxy at hostPort.
func (s *Server) URL(hostPort string) string {
	u := &url.URL{Scheme: "http", Host: hostPort}
	if s.Token != "" {
		u.User = url.UserPassword(User, s.Token)
	}
	return u.String()
}

// Close stops serving and removes any unix socket.
func (s *Server) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.srv == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := s.srv.Shutdown(ctx)
	for _, ln := range s.lns {
		if a, ok := ln.Addr().(*net.UnixAddr); ok {
			os.Remove(a.Name)
		}
	}
	s.srv, s.lns = nil, nil
	return err
}

// DefaultConnectPorts are the destination ports CONNECT may name unless
// the server says otherwise: HTTPS only.
var DefaultConnectPorts = []string{"443"}

// connectAllowed reports whether a CONNECT to port may proceed. The proxy
// is a container's sole way out, so this is what keeps SSH and every other
// non-HTTP protocol in: CONNECT to anything else is refused, and even an
// allowed CONNECT is terminated here and only ever carries HTTP.
func (s *Server) connectAllowed(port string) bool {
	ports := s.ConnectPorts
	if ports == nil {
		ports = DefaultConnectPorts
	}
	return slices.Contains(ports, port)
}

func (s *Server) authorised(r *http.Request) bool {
	if s.Token == "" {
		return true
	}
	raw, ok := strings.CutPrefix(r.Header.Get("Proxy-Authorization"), "Basic ")
	if !ok {
		return false
	}
	dec, err := base64.StdEncoding.DecodeString(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	want := User + ":" + s.Token
	return subtle.ConstantTimeCompare(dec, []byte(want)) == 1
}

// ServeHTTP implements http.Handler for the outer, plain-text listener.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !s.authorised(r) {
		w.Header().Set("Proxy-Authenticate", `Basic realm="axiomaton"`)
		http.Error(w, "proxy authentication required", http.StatusProxyAuthRequired)
		return
	}
	if r.Method == http.MethodConnect {
		s.connect(w, r)
		return
	}
	if !r.URL.IsAbs() {
		http.Error(w, "this is a proxy; use an absolute URL", http.StatusBadRequest)
		return
	}
	s.proxy(w, r)
}

// connect answers a CONNECT by terminating TLS itself with a certificate
// for the requested host and serving the requests inside as HTTPS to the
// origin.
func (s *Server) connect(w http.ResponseWriter, r *http.Request) {
	host := r.Host
	if _, _, err := net.SplitHostPort(host); err != nil {
		host = net.JoinHostPort(host, "443")
	}
	if _, port, _ := net.SplitHostPort(host); !s.connectAllowed(port) {
		s.logf("proxy REFUSED CONNECT %s (only HTTPS may leave the container)", host)
		http.Error(w, "CONNECT is only allowed to HTTPS ports", http.StatusForbidden)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "cannot hijack", http.StatusInternalServerError)
		return
	}
	conn, _, err := hj.Hijack()
	if err != nil {
		return
	}
	if _, err := io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		conn.Close()
		return
	}
	tlsConn := tls.Server(conn, &tls.Config{
		NextProtos: []string{"http/1.1"},
		GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			name := hello.ServerName
			if name == "" {
				name = host
			}
			return s.CA.Leaf(name)
		},
	})
	inner := &http.Server{
		ReadHeaderTimeout: 30 * time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			req.URL.Scheme = "https"
			req.URL.Host = req.Host
			if req.URL.Host == "" {
				req.URL.Host = host
			}
			req.URL.Host = stripDefaultPort(req.URL.Host, "443")
			s.proxy(w, req)
		}),
	}
	// Serve returns once the single connection is done.
	inner.Serve(&oneConnListener{conn: tlsConn, done: make(chan struct{})})
}

func stripDefaultPort(host, port string) string {
	if h, p, err := net.SplitHostPort(host); err == nil && p == port {
		return h
	}
	return host
}

// oneConnListener hands out a single connection, then blocks Accept until
// that connection closes so the HTTP server's Serve loop ends cleanly.
type oneConnListener struct {
	once sync.Once
	conn net.Conn
	done chan struct{}
}

func (l *oneConnListener) Accept() (net.Conn, error) {
	var c net.Conn
	l.once.Do(func() { c = &notifyConn{Conn: l.conn, done: l.done} })
	if c != nil {
		return c, nil
	}
	<-l.done
	return nil, net.ErrClosed
}

func (l *oneConnListener) Close() error   { return nil }
func (l *oneConnListener) Addr() net.Addr { return l.conn.LocalAddr() }

type notifyConn struct {
	net.Conn
	once sync.Once
	done chan struct{}
}

func (c *notifyConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() { close(c.done) })
	return err
}

// proxy serves one request to an origin, through the cache when the
// request is a plain GET.
func (s *Server) proxy(w http.ResponseWriter, r *http.Request) {
	target := r.URL.String()
	if s.serveGit(w, r) {
		return
	}
	if r.Method != http.MethodGet || r.Header.Get("Range") != "" {
		s.passThrough(w, r, target, "BYPASS")
		return
	}
	// One fetch per URL at a time: a second request for a file that is
	// being downloaded waits and then hits the cache.
	lock, _ := s.locks.LoadOrStore(target, &sync.Mutex{})
	mu := lock.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()

	entry, body, err := s.Cache.Get(target)
	if err != nil {
		s.logf("proxy cache read %s: %v", target, err)
	}
	if entry != nil && s.Cache.Fresh(entry) {
		defer body.Close()
		s.serveCached(w, entry, body, "HIT")
		return
	}
	// Fetching: take the cross-process lock, then look again, since
	// another ax may have fetched it while we waited.
	if body != nil {
		body.Close()
	}
	unlock, err := s.Cache.Lock(target)
	if err != nil {
		s.logf("proxy cache lock %s: %v", target, err)
		unlock = func() {}
	}
	defer unlock()
	entry, body, err = s.Cache.Get(target)
	if err != nil {
		s.logf("proxy cache read %s: %v", target, err)
	}
	if entry != nil && s.Cache.Fresh(entry) {
		defer body.Close()
		s.serveCached(w, entry, body, "HIT")
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, target, nil)
	if err != nil {
		if body != nil {
			body.Close()
		}
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	copyRequestHeaders(req.Header, r.Header)
	// Store and serve identity encodings so one copy suits every client.
	req.Header.Del("Accept-Encoding")
	if entry != nil {
		if et := entry.Header.Get("Etag"); et != "" {
			req.Header.Set("If-None-Match", et)
		}
		if lm := entry.Header.Get("Last-Modified"); lm != "" {
			req.Header.Set("If-Modified-Since", lm)
		}
	}
	resp, err := s.transport().RoundTrip(req)
	if err != nil {
		if entry != nil {
			defer body.Close()
			s.logf("proxy %s unreachable (%v); serving stale copy", target, err)
			s.serveCached(w, entry, body, "STALE")
			return
		}
		http.Error(w, "upstream: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if entry != nil && resp.StatusCode == http.StatusNotModified {
		defer body.Close()
		if err := s.Cache.Touch(entry, resp.Header); err != nil {
			s.logf("proxy cache touch %s: %v", target, err)
		}
		s.serveCached(w, entry, body, "REVALIDATED")
		return
	}
	if body != nil {
		body.Close()
	}
	writer, err := s.Cache.NewWriter(target, resp.StatusCode, resp.Header)
	if err != nil {
		s.logf("proxy cache write %s: %v", target, err)
		writer = nil
	}
	state := "MISS"
	if writer == nil {
		state = "BYPASS"
	}
	s.logf("proxy %s GET %s -> %d", state, target, resp.StatusCode)
	copyResponseHeaders(w.Header(), resp.Header)
	w.Header().Set(CacheHeader, state)
	w.WriteHeader(resp.StatusCode)
	var dst io.Writer = w
	if writer != nil {
		dst = io.MultiWriter(w, writer)
	}
	_, complete := copyBody(dst, resp.Body)
	if writer != nil {
		if err := writer.Commit(complete); err != nil {
			s.logf("proxy cache commit %s: %v", target, err)
		}
	}
}

func (s *Server) serveCached(w http.ResponseWriter, e *Entry, body io.Reader, state string) {
	s.logf("proxy %s GET %s", state, e.URL)
	copyResponseHeaders(w.Header(), e.Header)
	w.Header().Set("Content-Length", strconv.FormatInt(e.Size, 10))
	w.Header().Set(CacheHeader, state)
	w.WriteHeader(e.Status)
	io.Copy(w, body)
}

// passThrough relays a request without caching.
func (s *Server) passThrough(w http.ResponseWriter, r *http.Request, target, state string) {
	req, err := http.NewRequestWithContext(r.Context(), r.Method, target, r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	req.ContentLength = r.ContentLength
	copyRequestHeaders(req.Header, r.Header)
	resp, err := s.transport().RoundTrip(req)
	if err != nil {
		http.Error(w, "upstream: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	s.logf("proxy %s %s %s -> %d", state, r.Method, target, resp.StatusCode)
	copyResponseHeaders(w.Header(), resp.Header)
	w.Header().Set(CacheHeader, state)
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

func copyRequestHeaders(dst, src http.Header) {
	for k, v := range src {
		if hopByHop[http.CanonicalHeaderKey(k)] {
			continue
		}
		dst[k] = append([]string(nil), v...)
	}
}

func copyResponseHeaders(dst, src http.Header) {
	for k, v := range src {
		if hopByHop[http.CanonicalHeaderKey(k)] {
			continue
		}
		dst[k] = append([]string(nil), v...)
	}
}

// HostsEntry returns the address an /etc/hosts file gives name, or "".
func HostsEntry(hosts, name string) string {
	sc := bufio.NewScanner(strings.NewReader(hosts))
	for sc.Scan() {
		line := sc.Text()
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		f := strings.Fields(line)
		if len(f) < 2 || net.ParseIP(f[0]) == nil {
			continue
		}
		if slices.Contains(f[1:], name) {
			return f[0]
		}
	}
	return ""
}

// ReadGateway parses the default gateway out of the contents of a Linux
// /proc/net/route, which lists addresses as little-endian hex.
func ReadGateway(route string) (string, error) {
	sc := bufio.NewScanner(strings.NewReader(route))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 3 || f[1] != "00000000" || len(f[2]) != 8 {
			continue
		}
		b, err := hex.DecodeString(f[2])
		if err != nil {
			continue
		}
		return net.IPv4(b[3], b[2], b[1], b[0]).String(), nil
	}
	return "", errors.New("no default route")
}
