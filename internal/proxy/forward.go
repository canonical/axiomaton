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
	"errors"
	"io"
	"net"
	"sync"
	"time"
)

// Forward accepts TCP connections on listen and relays each to the unix
// socket at socket. It is what ax runs inside a container's network
// namespace so that a container with no network of its own can still reach
// the proxy on 127.0.0.1. It returns when ctx ends or when quit (usually
// the parent's stdin pipe) reaches EOF, so an orphaned forwarder exits
// with its parent.
func Forward(ctx context.Context, listen, socket string, quit io.Reader) error {
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	defer ln.Close()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if quit != nil {
		go func() {
			io.Copy(io.Discard, quit)
			cancel()
		}()
	}
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			continue
		}
		wg.Go(func() {
			relay(ctx, conn, socket)
		})
	}
}

func relay(ctx context.Context, conn net.Conn, socket string) {
	defer conn.Close()
	d := net.Dialer{Timeout: 10 * time.Second}
	up, err := d.DialContext(ctx, "unix", socket)
	if err != nil {
		return
	}
	defer up.Close()
	done := make(chan struct{}, 2)
	go func() { io.Copy(up, conn); closeWrite(up); done <- struct{}{} }()
	go func() { io.Copy(conn, up); closeWrite(conn); done <- struct{}{} }()
	select {
	case <-done:
		<-done
	case <-ctx.Done():
	}
}

func closeWrite(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		cw.CloseWrite()
	}
}
