//go:build cgo
// +build cgo

// Package rundht implements the interface between storrent and the DHT.
// The DHT implementation itself is in package dht.
package rundht

import (
	"context"
	"flag"
	"log"
	"math/rand/v2"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"time"

	"github.com/zeebo/bencode"

	"github.com/jech/storrent/config"
	"github.com/jech/storrent/dht"
	"github.com/jech/storrent/hash"
	"github.com/jech/storrent/known"
	"github.com/jech/storrent/pex"
	"github.com/jech/storrent/tor"
)

func init() {
	dhtFile := ""
	configDir, err := os.UserConfigDir()
	if err != nil {
		log.Printf("Couldn't determine config dir: %v", err)
	} else {
		dhtFile = filepath.Join(
			filepath.Join(configDir, "storrent"),
			"dht.dat",
		)
	}

	flag.StringVar(&config.DHTBootstrap, "dht-bootstrap", dhtFile,
		"DHT bootstrap `filename`")
}

// BDHT represents the contents of the dht.dat file.
type BDHT struct {
	Id     []byte `bencode:"id,omitempty"`
	Nodes  []byte `bencode:"nodes,omitempty"`
	Nodes6 []byte `bencode:"nodes6,omitempty"`
}

func parseCompact(data []byte, ipv6 bool) []netip.AddrPort {
	peers := pex.ParseCompact(data, nil, ipv6)
	addrs := make([]netip.AddrPort, len(peers))
	for i, p := range peers {
		addrs[i] = p.Addr
	}
	return addrs
}

func formatCompact(addrs []netip.AddrPort) ([]byte, []byte) {
	peers := make([]pex.Peer, len(addrs))
	for i, a := range addrs {
		peers[i].Addr = a
	}
	n4, _, n6, _ := pex.FormatCompact(peers)
	return n4, n6
}

func read(filename string) (bdht *BDHT, info os.FileInfo, err error) {
	var r *os.File
	r, err = os.Open(filename)
	if err != nil {
		return
	}
	defer r.Close()

	info, err = r.Stat()
	if err != nil {
		return
	}

	decoder := bencode.NewDecoder(r)
	err = decoder.Decode(&bdht)
	return
}

// Read reads the dht.dat file and returns a set of potential nodes.
func Read(filename string) (id []byte, nodes []netip.AddrPort, err error) {
	bdht, info, err := read(filename)

	if err != nil {
		return
	}

	// for privacy reasons, don't honour an old id.
	if time.Since(info.ModTime()) < time.Hour && len(bdht.Id) == 20 {
		id = bdht.Id
	}
	if bdht.Nodes != nil {
		nodes = append(nodes, parseCompact(bdht.Nodes, false)...)
	}
	if bdht.Nodes6 != nil {
		nodes = append(nodes, parseCompact(bdht.Nodes6, true)...)
	}
	return
}

// Write writes the dht.dat file.
func Write(filename string, id []byte) error {
	addrs, err := dht.GetNodes()
	if err != nil {
		return err
	}
	if len(addrs) < 8 {
		return nil
	}
	var bdht BDHT
	if len(id) == 20 {
		bdht.Id = id
	}
	bdht.Nodes, bdht.Nodes6 = formatCompact(addrs)

	w, err := os.Create(filename)
	if err != nil {
		return err
	}

	encoder := bencode.NewEncoder(w)
	err = encoder.Encode(&bdht)
	err2 := w.Close()
	if err == nil {
		err = err2
	}
	if err != nil {
		os.Remove(filename)
		return err
	}
	return nil
}

// Bootstrap bootstraps the DHT.
func Bootstrap(ctx context.Context, nodes []netip.AddrPort) {
	bootstrap := []string{"dht.transmissionbt.com", "router.bittorrent.com"}

	reannounced4 := false
	reannounced6 := false
	reannounce := func(ipv6 bool) {
		reannounced := reannounced4
		if ipv6 {
			reannounced = reannounced6
		}
		if !reannounced {
			tor.Range(
				func(h hash.Hash, t *tor.Torrent) bool {
					tor.Announce(h, ipv6)
					return true
				})
		}
		if !ipv6 {
			reannounced4 = true
		} else {
			reannounced6 = true
		}
	}

	log.Printf("Bootstrapping DHT from %v nodes", len(nodes))
	defer func() {
		g4, g6, d4, d6, _, _ := dht.Count()
		log.Printf("DHT bootstrapped (%v/%v %v/%v confirmed nodes)",
			g4, g4+d4, g6, g6+d6)
	}()

	ni := rand.Perm(len(nodes))

	for i := 0; i < len(nodes)*3; i++ {
		c4, c6, _, _, _, _ := dht.Count()
		if c4 >= 9 {
			reannounce(false)
		}
		if c6 >= 9 {
			reannounce(true)
		}
		if reannounced4 && reannounced6 && i >= len(nodes) {
			return
		}
		dht.Ping(nodes[ni[i%len(nodes)]])
		if i < 16 && i < len(nodes) {
			doze()
		} else {
			nap(2, 1)
		}
		if ctx.Err() != nil {
			return
		}
	}

	nodes = nil
	ni = nil

	for _, name := range bootstrap {
		ips, err := net.LookupIP(name)
		if err != nil {
			log.Printf("Couldn't resolve %v: %v", name, err)
			continue
		}
		for _, ip := range ips {
			ipp, ok := netip.AddrFromSlice(ip)
			if ok {
				nodes = append(nodes,
					netip.AddrPortFrom(ipp, 6881),
				)
			}
		}
	}

	log.Printf("Bootstrapping DHT from %v fallback nodes", len(nodes))

	for i := 0; i < len(nodes)*3; i++ {
		c4, c6, _, _, _, _ := dht.Count()
		if c4 >= 9 {
			reannounce(false)
		}
		if c6 >= 9 {
			reannounce(true)
		}
		if reannounced4 && reannounced6 {
			return
		}

		dht.Ping(nodes[i%len(nodes)])
		nap(2, 1)
		if ctx.Err() != nil {
			return
		}
	}
	reannounce(false)
	reannounce(true)
}

// Handle reacts to events sent by the DHT.
func Handle(dhtevent <-chan dht.Event) {
	for {
		event, ok := <-dhtevent
		if !ok {
			return
		}
		switch event := event.(type) {
		case dht.ValueEvent:
			t := tor.Get(event.Hash)
			if t != nil {
				t.AddKnown(event.Addr, nil, "", known.DHT)
			}
		}
	}
}

// Run starts the DHT.
func Run(ctx context.Context, id []byte, port int) (<-chan dht.Event, error) {
	return dht.DHT(ctx, id, uint16(port))
}

// nap is a long doze.
func nap(n int, m int) {
	time.Sleep(time.Duration(n-m/2)*time.Second +
		rand.N(time.Duration(m)*time.Second))
}

// doze is a short nap.
func doze() {
	time.Sleep(time.Millisecond + rand.N(time.Millisecond))
}
