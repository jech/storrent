// Package PEX implements the data structures used by BitTorrent peer exchange.
package pex

import (
	"encoding/binary"
	"net/netip"
	"slices"
)

// Peer represents a peer known or announced over PEX.
type Peer struct {
	Addr  netip.AddrPort
	Flags byte
}

// PEX flags
const (
	Encrypt    = 0x01
	UploadOnly = 0x02
	Outgoing   = 0x10
)

// Find find a peer in a list of peers.
func Find(p Peer, l []Peer) int {
	return slices.IndexFunc(l, func(q Peer) bool {
		return p.Addr == q.Addr
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
		ip, ok := netip.AddrFromSlice(data[j : j+l])
		if !ok {
			continue
		}
		var flag byte
		if i < len(flags) {
			flag = flags[i]
		}
		port := binary.BigEndian.Uint16(data[j+l:])
		addr := netip.AddrPortFrom(ip, port)
		peers = append(peers, Peer{Addr: addr, Flags: flag})
	}
	return peers
}

// FormatCompact formats a list of PEX peers in compact format.
func FormatCompact(peers []Peer) (ipv4 []byte, flags4 []byte, ipv6 []byte, flags6 []byte) {
	for _, peer := range peers {
		if peer.Addr.Addr().Is4() {
			v4 := peer.Addr.Addr().As4()
			ipv4 = append(ipv4, v4[:]...)
			ipv4 = binary.BigEndian.AppendUint16(
				ipv4, peer.Addr.Port(),
			)
			flags4 = append(flags4, peer.Flags)
		} else {
			v6 := peer.Addr.Addr().As16()
			ipv6 = append(ipv6, v6[:]...)
			ipv6 = binary.BigEndian.AppendUint16(
				ipv6, peer.Addr.Port(),
			)
			flags6 = append(flags6, peer.Flags)
		}
	}
	return
}
