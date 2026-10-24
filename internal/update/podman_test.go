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
	"os"
	"strings"
	"testing"
	"time"
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
