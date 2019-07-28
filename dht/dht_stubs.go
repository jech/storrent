// +build !cgo

package dht

import (
	"net"
)

func Available() bool {
	return false
}

func Ping(ip net.IP, port uint16) error {
	return nil
}

func Announce(id []byte, ipv6 bool, port uint16) error {
	return nil
}

func Count() (good4 int, good6 int,
	dubious4 int, dubious6 int,
	incoming4 int, incoming6 int) {
	return
}

