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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Cache stores responses on disk, one metadata file and one body file per
// URL.
type Cache struct {
	Dir string
	// MaxObject is the largest body stored, in bytes; zero means 1 GiB.
	MaxObject int64
	// Now is the clock, replaceable in tests.
	Now func() time.Time
}

// Entry is a cached response.
type Entry struct {
	URL    string      `json:"url"`
	Status int         `json:"status"`
	Header http.Header `json:"header"`
	Stored time.Time   `json:"stored"`
	Size   int64       `json:"size"`
}

const (
	defaultMaxObject = 1 << 30
	// immutableTTL applies to artefacts whose content never changes once
	// published: packages, module zips, release tarballs.
	immutableTTL = 30 * 24 * time.Hour
	// mutableTTL applies to indexes and metadata, which are revalidated
	// with the origin once it has passed.
	mutableTTL = 5 * time.Minute
)

func (c *Cache) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Cache) maxObject() int64 {
	if c.MaxObject > 0 {
		return c.MaxObject
	}
	return defaultMaxObject
}

func (c *Cache) paths(url string) (meta, body string) {
	sum := sha256.Sum256([]byte(url))
	key := hex.EncodeToString(sum[:])
	return filepath.Join(c.Dir, key[:2], key+".json"), filepath.Join(c.Dir, key[:2], key+".body")
}

// Lock serialises fetching url across every process sharing the cache
// directory. It blocks until held and returns the release function.
func (c *Cache) Lock(url string) (func(), error) {
	meta, _ := c.paths(url)
	return lockFile(strings.TrimSuffix(meta, ".json") + ".lock")
}

// writeAtomic replaces path with data so readers in other processes see
// either the old file or the new one, never a partial write.
func writeAtomic(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

// Get returns the cached entry and its body for url, or nil when there is
// none. The caller closes the body.
func (c *Cache) Get(url string) (*Entry, *os.File, error) {
	meta, body := c.paths(url)
	data, err := os.ReadFile(meta)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var e Entry
	if err := json.Unmarshal(data, &e); err != nil || e.URL != url {
		return nil, nil, nil
	}
	f, err := os.Open(body)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if info, err := f.Stat(); err != nil || info.Size() != e.Size {
		f.Close()
		return nil, nil, nil
	}
	return &e, f, nil
}

// Fresh reports whether the entry can be served without asking the origin.
func (c *Cache) Fresh(e *Entry) bool {
	return c.now().Sub(e.Stored) < TTL(e.URL, e.Header)
}

// Writer streams a response body into the cache while it is being served.
type Writer struct {
	c     *Cache
	entry Entry
	tmp   *os.File
	meta  string
	body  string
	n     int64
	limit int64
	over  bool
	err   error
}

// NewWriter prepares to store a response. It returns nil when the response
// must not be cached (see Cacheable).
func (c *Cache) NewWriter(url string, status int, header http.Header) (*Writer, error) {
	if !Cacheable(url, status, header) {
		return nil, nil
	}
	if n, err := strconv.ParseInt(header.Get("Content-Length"), 10, 64); err == nil && n > c.maxObject() {
		return nil, nil
	}
	meta, body := c.paths(url)
	if err := os.MkdirAll(filepath.Dir(meta), 0o755); err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(meta), ".part-*")
	if err != nil {
		return nil, err
	}
	return &Writer{c: c, entry: Entry{URL: url, Status: status, Header: storedHeaders(header)}, tmp: tmp, meta: meta, body: body, limit: c.maxObject()}, nil
}

// Write implements io.Writer. Once the body exceeds the size limit the
// rest is discarded and the entry is not committed.
func (w *Writer) Write(p []byte) (int, error) {
	if w.over || w.err != nil {
		return len(p), nil
	}
	if w.n+int64(len(p)) > w.limit {
		w.over = true
		return len(p), nil
	}
	n, err := w.tmp.Write(p)
	w.n += int64(n)
	if err != nil {
		w.err = err
	}
	return len(p), nil
}

// Commit stores the entry if the whole body arrived and fitted.
func (w *Writer) Commit(complete bool) error {
	name := w.tmp.Name()
	closeErr := w.tmp.Close()
	if !complete || w.over || w.err != nil || closeErr != nil {
		os.Remove(name)
		return nil
	}
	w.entry.Stored = w.c.now()
	w.entry.Size = w.n
	data, err := json.Marshal(w.entry)
	if err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, w.body); err != nil {
		os.Remove(name)
		return err
	}
	return writeAtomic(w.meta, data, 0o644)
}

// Abort discards a partial body.
func (w *Writer) Abort() {
	name := w.tmp.Name()
	w.tmp.Close()
	os.Remove(name)
}

// Touch records that the origin confirmed the entry is still current,
// adopting any refreshed headers it sent.
func (c *Cache) Touch(e *Entry, refreshed http.Header) error {
	maps.Copy(e.Header, storedHeaders(refreshed))
	e.Stored = c.now()
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	meta, _ := c.paths(e.URL)
	return writeAtomic(meta, data, 0o644)
}

// Cacheable reports whether a response may be stored: a complete 200 to a
// plain GET that the origin has not marked no-store.
func Cacheable(url string, status int, header http.Header) bool {
	if status != http.StatusOK {
		return false
	}
	cc := strings.ToLower(header.Get("Cache-Control"))
	if strings.Contains(cc, "no-store") || strings.Contains(cc, "private") {
		return false
	}
	if header.Get("Content-Range") != "" {
		return false
	}
	return true
}

// TTL is how long a stored response for url is served without revalidation:
// the longer of the origin's max-age and the heuristic for the kind of
// artefact (immutable files for a month, indexes and metadata for minutes).
func TTL(url string, header http.Header) time.Duration {
	ttl := mutableTTL
	if Immutable(url) {
		ttl = immutableTTL
	}
	if cc := header.Get("Cache-Control"); cc != "" {
		for d := range strings.SplitSeq(cc, ",") {
			d = strings.TrimSpace(strings.ToLower(d))
			if v, ok := strings.CutPrefix(d, "max-age="); ok {
				if secs, err := strconv.Atoi(v); err == nil && time.Duration(secs)*time.Second > ttl {
					ttl = time.Duration(secs) * time.Second
				}
			}
			if d == "immutable" && immutableTTL > ttl {
				ttl = immutableTTL
			}
		}
	}
	return ttl
}

var immutableSuffixes = []string{
	".deb", ".ddeb", ".udeb", ".rpm", ".apk",
	".whl", ".egg", ".crate", ".gem", ".jar", ".nupkg",
	".zip", ".mod", ".info", ".tar", ".tar.gz", ".tgz", ".tar.xz", ".txz", ".tar.bz2", ".tar.zst", ".7z",
	".sig", ".minisig",
}

var immutablePaths = []string{
	"/pool/",             // apt package pools
	"/packages/",         // files.pythonhosted.org
	"/@v/v",              // go module proxy versioned files
	"/dist/20",           // static.rust-lang.org dated directories
	"/download/0.",       // ziglang.org per-version tarballs
	"/dl/go1.",           // go.dev toolchain tarballs
	"/releases/download", // github release assets
}

// Immutable reports whether the URL names an artefact whose content does
// not change once published.
func Immutable(url string) bool {
	path := url
	if i := strings.Index(path, "?"); i >= 0 {
		path = path[:i]
	}
	if i := strings.Index(path, "://"); i >= 0 {
		path = path[i+3:]
	}
	if i := strings.Index(path, "/"); i >= 0 {
		path = path[i:]
	} else {
		path = "/"
	}
	lower := strings.ToLower(path)
	for _, suf := range immutableSuffixes {
		if strings.HasSuffix(lower, suf) {
			return true
		}
	}
	for _, p := range immutablePaths {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

// hopByHop are headers that describe one connection and are never
// forwarded or stored.
var hopByHop = map[string]bool{
	"Connection": true, "Keep-Alive": true, "Proxy-Authenticate": true, "Proxy-Authorization": true,
	"Proxy-Connection": true, "Te": true, "Trailer": true, "Transfer-Encoding": true, "Upgrade": true,
}

func storedHeaders(h http.Header) http.Header {
	out := http.Header{}
	for k, v := range h {
		if hopByHop[http.CanonicalHeaderKey(k)] || k == "Content-Length" {
			continue
		}
		out[k] = append([]string(nil), v...)
	}
	return out
}

// copyBody copies src to dst and reports whether it completed.
func copyBody(dst io.Writer, src io.Reader) (int64, bool) {
	n, err := io.Copy(dst, src)
	return n, err == nil
}
