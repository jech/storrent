package pex

import (
	"net"
)

type Peer struct {
	IP    net.IP
	Port  int
	Flags byte
}

const (
	Encrypt    = 0x01
	UploadOnly = 0x02
	Outgoing   = 0x10
)

func (p Peer) Equal(q Peer) bool {
	return p.IP.Equal(q.IP) && q.Port == q.Port
}

func Find(p Peer, l []Peer) int {
	for i, q := range l {
		if p.Equal(q) {
			return i
		}
	}
	return -1
}

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
		port := 256*uint16(data[j+l]) + uint16(data[j+l+1])
		peers = append(peers,
			Peer{IP: ip, Port: int(port), Flags: flag})
	}
	return peers
}

func FormatCompact(peers []Peer) (ipv4 []byte, flags4 []byte, ipv6 []byte, flags6 []byte) {
	for _, peer := range peers {
		v4 := peer.IP.To4()
		v6 := peer.IP.To16()
		if v4 != nil {
			ipv4 = append(ipv4, []byte(v4)...)
			ipv4 = append(ipv4, byte(peer.Port>>8))
			ipv4 = append(ipv4, byte(peer.Port&0xFF))
			flags4 = append(flags4, peer.Flags)
		} else if v6 != nil {
			ipv6 = append(ipv6, []byte(v6)...)
			ipv6 = append(ipv6, byte(peer.Port>>8))
			ipv6 = append(ipv6, byte(peer.Port&0xFF))
			flags6 = append(flags6, peer.Flags)
		}
	}
	return
}
