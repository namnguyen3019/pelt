package loadtest

import "syscall"

// On Windows, socket errors arrive as Winsock codes, which syscall's
// ECONNREFUSED and friends do not match.
var (
	errConnRefused  = []error{syscall.ECONNREFUSED, syscall.Errno(10061)}                        // WSAECONNREFUSED
	errConnReset    = []error{syscall.ECONNRESET, syscall.Errno(10054), syscall.Errno(10053)}    // WSAECONNRESET, WSAECONNABORTED
	errAddrNotAvail = []error{syscall.EADDRNOTAVAIL, syscall.Errno(10049), syscall.Errno(10055)} // WSAEADDRNOTAVAIL, WSAENOBUFS
	errTooManyOpen  = []error{syscall.EMFILE, syscall.ENFILE, syscall.Errno(10024)}              // WSAEMFILE
)
