// Package PEX implements the data structures used by BitTorrent peer exchange.
package pex

import (
	"encoding/binary"
	"net"
	"slices"
)

// Peer represents a peer known or announced over PEX.
type Peer struct {
	IP    net.IP
	Port  int
	Flags byte
}

// PEX flags
const (
	Encrypt    = 0x01
	UploadOnly = 0x02
	Outgoing   = 0x10
)

// Equal returns true if two peers have the same socket address.
func (p Peer) Equal(q Peer) bool {
	return p.IP.Equal(q.IP) && p.Port == q.Port
}

// Find find a peer in a list of peers.
func Find(p Peer, l []Peer) int {
	return slices.IndexFunc(l, func (q Peer) bool {
		return p.Equal(q)
	})
}

// ParseCompact parses a list of PEX peers in compact format.
func ParseCompact(data []byte, flags []byte, ipv6 bool) []Peer {
	l := 4
	if ipv6 {
		l = 16
	}

	if len(data)%(l+2) != 0 {
		return nil
	}
	n := len(data) / (l + 2)

	var peers = make([]Peer, 0, n)
	for i := 0; i < n; i++ {
		j := i * (l + 2)
		ip := net.IP(make([]byte, l))
		copy(ip, data[j:j+l])
		var flag byte
		if i < len(flags) {
			flag = flags[i]
		}
		port := binary.BigEndian.Uint16(data[j+l:])
		peers = append(peers,
			Peer{IP: ip, Port: int(port), Flags: flag})
	}
	return peers
}

// FormatCompact formats a list of PEX peers in compact format.
func FormatCompact(peers []Peer) (ipv4 []byte, flags4 []byte, ipv6 []byte, flags6 []byte) {
	for _, peer := range peers {
		v4 := peer.IP.To4()
		v6 := peer.IP.To16()
		port := uint16(peer.Port)
		if v4 != nil {
			ipv4 = append(ipv4, []byte(v4)...)
			ipv4 = binary.BigEndian.AppendUint16(ipv4, port)
			flags4 = append(flags4, peer.Flags)
		} else if v6 != nil {
			ipv6 = append(ipv6, []byte(v6)...)
			ipv6 = binary.BigEndian.AppendUint16(ipv6, port)
			flags6 = append(flags6, peer.Flags)
		}
	}
	return
}
