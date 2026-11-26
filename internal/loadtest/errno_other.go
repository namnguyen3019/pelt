//go:build !windows

package loadtest

import "syscall"

var (
	errConnRefused  = []error{syscall.ECONNREFUSED}
	errConnReset    = []error{syscall.ECONNRESET}
	errAddrNotAvail = []error{syscall.EADDRNOTAVAIL}
	errTooManyOpen  = []error{syscall.EMFILE, syscall.ENFILE}
)
