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

	"github.com/canonical/axiomaton/internal/config"
)

// ConsentRequest describes something ax is about to do to the host for
// the first time and wants permission for: the key it is remembered
// under in ax.cnf, a short title that completes "ax wants to ..." and
// the details of exactly what will happen.
type ConsentRequest struct {
	Key     string
	Title   string
	Details []string
}

// ConsentFunc decides whether the action described by req may go ahead;
// a non-nil error stops it. A runtime calls it before the first command
// that changes the host (pulling an image, launching an instance,
// creating a network), possibly from several goroutines at once, and
// may ask for the same key more than once, so implementations serialise
// and remember answers. A nil ConsentFunc on a runtime grants everything.
type ConsentFunc func(ctx context.Context, req ConsentRequest) error

func askConsent(ctx context.Context, f ConsentFunc, req ConsentRequest) error {
	if f == nil {
		return nil
	}
	if err := f(ctx, req); err != nil {
		return fmt.Errorf("%s: %w", req.Title, err)
	}
	return nil
}

func podmanConsent(image string, proxied bool) ConsentRequest {
	req := ConsentRequest{
		Key:   config.ConsentPodman,
		Title: "run agents in podman containers",
		Details: []string{
			fmt.Sprintf("pull the image %s if it is not already present", image),
			"start containers named axiomaton-* from it; each is removed when its agent finishes",
		},
	}
	if proxied {
		req.Details = append(req.Details,
			"start each container without a network and run nsenter (through podman unshare when podman is rootless) to place a forwarder in its network namespace, so its only way out is the cache proxy on this host",
			"install the proxy's CA certificate inside each container",
		)
	} else {
		req.Details = append(req.Details, "containers use podman's default network, unrestricted (proxy.disabled is set)")
	}
	return req
}

func lxdConsent(image, network string, vm, proxied bool) ConsentRequest {
	kind, limits := "containers", ""
	if vm {
		kind, limits = "virtual machines", fmt.Sprintf(" with %s of memory and %s CPUs each", vmMemory, vmCPUs)
	}
	req := ConsentRequest{
		Key:   config.ConsentLXD,
		Title: "run agents in LXD " + kind,
		Details: []string{
			fmt.Sprintf("launch %s named axiomaton-* from the image %s%s, downloading the image if needed", kind, image, limits),
			"delete each instance when its agent finishes",
		},
	}
	if proxied {
		req.Details = append(req.Details,
			fmt.Sprintf("attach each instance to the isolated network %s (creating it is asked about separately) and install the proxy's CA certificate inside it", network),
		)
	} else {
		req.Details = append(req.Details, "instances use the default profile's network, unrestricted (proxy.disabled is set)")
	}
	return req
}

func lxdNetworkConsent(network string) ConsentRequest {
	return ConsentRequest{
		Key:   config.ConsentLXDNetwork,
		Title: "create the isolated LXD network " + network,
		Details: []string{
			fmt.Sprintf("create a bridge network named %s with ipv4.address=auto, ipv4.nat=false, ipv4.routing=false and ipv6.address=none", network),
			fmt.Sprintf("create a network ACL named %s that drops all egress except DHCP and TCP to this host on ports %d-%d, and apply it to the network", network, ProxyPortFrom, ProxyPortTo),
			fmt.Sprintf("leave both in place for later runs (remove them with lxc network delete %s and lxc network acl delete %s)", network, network),
		},
	}
}
