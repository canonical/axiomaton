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
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/cgi"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Git smart HTTP cannot be cached response by response: fetches are POSTs
// whose answers depend on what the client already has. Instead the proxy
// keeps a bare mirror of each repository that is fetched through it and
// answers the protocol from the mirror with git http-backend, refreshing
// the mirror from the origin when it is older than mutableTTL. Clients see
// an ordinary smart HTTP server.

// gitUploadPack is the service for fetches and clones; pushes
// (git-receive-pack) are not mirrored and pass through.
const gitUploadPack = "git-upload-pack"

var (
	gitOnce sync.Once
	gitPath string
)

func gitBinary() string {
	gitOnce.Do(func() { gitPath, _ = exec.LookPath("git") })
	return gitPath
}

// gitRequest reports whether r is a smart HTTP fetch request and, if so,
// the repository URL and the path git http-backend should see.
func gitRequest(r *http.Request) (repo, suffix string, ok bool) {
	p := r.URL.Path
	switch {
	case r.Method == http.MethodGet && strings.HasSuffix(p, "/info/refs") && r.URL.Query().Get("service") == gitUploadPack:
		return strings.TrimSuffix(p, "/info/refs"), "/info/refs", true
	case r.Method == http.MethodPost && strings.HasSuffix(p, "/"+gitUploadPack):
		return strings.TrimSuffix(p, "/"+gitUploadPack), "/" + gitUploadPack, true
	}
	return "", "", false
}

// mirrorKey is the directory name for a repository URL. The scheme and a
// trailing .git do not make a different repository.
func mirrorKey(repoURL string) string {
	norm := strings.TrimSuffix(strings.TrimSuffix(repoURL, "/"), ".git")
	if i := strings.Index(norm, "://"); i >= 0 {
		norm = norm[i+3:]
	}
	sum := sha256.Sum256([]byte(strings.ToLower(norm)))
	return hex.EncodeToString(sum[:16]) + ".git"
}

// gitMirror brings the mirror of repoURL up to date and returns its
// directory and how it was obtained. A refresh that fails leaves the stale
// mirror in use.
func (s *Server) gitMirror(ctx context.Context, repoURL string) (dir, state string, err error) {
	git := gitBinary()
	if git == "" {
		return "", "", errors.New("git is not installed on the host")
	}
	root := filepath.Join(s.Cache.Dir, "git")
	dir = filepath.Join(root, mirrorKey(repoURL))
	lock, _ := s.locks.LoadOrStore("git:"+dir, &sync.Mutex{})
	mu := lock.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()
	// Other ax processes share the mirror; one updates it at a time.
	unlock, err := lockFile(dir + ".lock")
	if err != nil {
		return "", "", err
	}
	defer unlock()

	marker := filepath.Join(dir, "axiomaton-fetched")
	run := func(args ...string) error {
		cmd := exec.CommandContext(ctx, git, args...)
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("%s: %w: %s", strings.Join(args[:2], " "), err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	info, statErr := os.Stat(marker)
	switch {
	case statErr != nil:
		if err := os.MkdirAll(root, 0o755); err != nil {
			return "", "", err
		}
		os.RemoveAll(dir)
		if err := run("clone", "--mirror", "--quiet", repoURL, dir); err != nil {
			os.RemoveAll(dir)
			return "", "", err
		}
		state = "GIT-MISS"
	case s.Cache.now().Sub(info.ModTime()) < mutableTTL:
		return dir, "GIT-HIT", nil
	default:
		if err := run("-C", dir, "fetch", "--prune", "--quiet", "origin"); err != nil {
			s.logf("proxy git refresh %s failed (%v); serving the stale mirror", repoURL, err)
			return dir, "GIT-STALE", nil
		}
		state = "GIT-REVALIDATED"
	}
	now := s.Cache.now()
	if err := os.WriteFile(marker, []byte(now.Format(time.RFC3339)+"\n"), 0o644); err != nil {
		return "", "", err
	}
	os.Chtimes(marker, now, now)
	return dir, state, nil
}

// serveGit answers a smart HTTP fetch from the mirror. It returns false
// when the request should be passed to the origin instead: the request
// carries credentials, git is missing, or the repository cannot be
// mirrored.
func (s *Server) serveGit(w http.ResponseWriter, r *http.Request) bool {
	repoPath, suffix, ok := gitRequest(r)
	if !ok || r.Header.Get("Authorization") != "" {
		return false
	}
	repoURL := r.URL.Scheme + "://" + r.URL.Host + repoPath
	dir, state, err := s.gitMirror(r.Context(), repoURL)
	if err != nil {
		s.logf("proxy git %s: %v; passing through", repoURL, err)
		return false
	}
	s.logf("proxy %s %s %s", state, r.Method, repoURL+suffix)
	h := &cgi.Handler{
		Path: gitBinary(),
		Args: []string{"http-backend"},
		Dir:  dir,
		Env:  []string{"GIT_PROJECT_ROOT=" + filepath.Dir(dir), "GIT_HTTP_EXPORT_ALL=1"},
	}
	req := r.Clone(r.Context())
	req.URL.Path = "/" + filepath.Base(dir) + suffix
	req.URL.RawPath = ""
	w.Header().Set(CacheHeader, state)
	h.ServeHTTP(w, req)
	return true
}
