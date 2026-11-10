//go:build !unix

package main

import "errors"

// reexec isn't possible here; the service manager has to start it again.
func reexec() error {
	return errors.New("restarting in place only works on Linux and other Unix systems")
}
