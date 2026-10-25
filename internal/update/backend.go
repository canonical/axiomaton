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
	"fmt"
	"os/exec"
	"strings"
)

// Backend names a way of running agents.
type Backend string

const (
	// BackendAuto picks podman when it is installed, otherwise LXD.
	BackendAuto Backend = "auto"
	// BackendPodman runs OCI containers with podman.
	BackendPodman Backend = "podman"
	// BackendLXD runs LXD system containers.
	BackendLXD Backend = "lxd"
	// BackendLXDVM runs LXD virtual machines.
	BackendLXDVM Backend = "lxd-vm"
)

// lookPath is exec.LookPath, replaceable in tests.
var lookPath = exec.LookPath

// ResolveBackend turns a configured backend into a concrete one. With
// needVM the result is always BackendLXDVM, since only LXD runs virtual
// machines. An unknown name or a missing tool is an error that says what
// to install.
func ResolveBackend(configured string, needVM bool) (Backend, error) {
	if needVM {
		if _, err := lookPath("lxc"); err != nil {
			return "", fmt.Errorf("this subject's agents need a virtual machine, which needs LXD (lxc not found on PATH)")
		}
		return BackendLXDVM, nil
	}
	switch b := Backend(strings.ToLower(strings.TrimSpace(configured))); b {
	case "", BackendAuto:
		if _, err := lookPath("podman"); err == nil {
			return BackendPodman, nil
		}
		if _, err := lookPath("lxc"); err == nil {
			return BackendLXD, nil
		}
		return "", fmt.Errorf("neither podman nor lxc is on PATH; install podman or LXD to run agents")
	case BackendPodman:
		if _, err := lookPath("podman"); err != nil {
			return "", fmt.Errorf("update.backend is podman but podman is not on PATH")
		}
		return b, nil
	case BackendLXD, BackendLXDVM:
		if _, err := lookPath("lxc"); err != nil {
			return "", fmt.Errorf("update.backend is %s but lxc is not on PATH", b)
		}
		return b, nil
	}
	return "", fmt.Errorf("unknown update.backend %q (use auto, podman, lxd or lxd-vm)", configured)
}
