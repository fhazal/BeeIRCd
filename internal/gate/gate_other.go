//go:build !linux

package gate

import (
	"errors"
	"log"
	"net"
)

// Supported: the gate runs on this system. Not here: `beeircd run` stays
// one process.
const Supported = false

var errUnsupported = errors.New("gate: only on Linux")

// Run is never called where Supported is false.
func Run(logger *log.Logger, newFront func(Deliver) Front) int {
	logger.Print(errUnsupported)
	return 1
}

// Child is the server's end of the gate; never made on this system.
type Child struct{}

// ChildFromEnv: no gate here, so never a child.
func ChildFromEnv() (*Child, error) { return nil, nil }

func (c *Child) Accept() (net.Conn, error) { return nil, errUnsupported }
func (c *Child) Sync(Spec) (Result, error) { return Result{}, errUnsupported }
func (c *Child) Gone() <-chan struct{}     { return nil }
func (c *Child) Close() error              { return nil }
func peerClosed(*net.UnixConn) bool        { return false }
