// +build cgo

package rundht

import (
	"context"
	"flag"
	"log"
	"math/rand"
	"net"
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

	flag.BoolVar(&config.DefaultUseDht, "use-dht", true,
		"Use the DHT by default.")
	flag.StringVar(&config.DHTBootstrap, "dht", dhtFile,
		"DHT bootstrap `file`.")
}

type BDHT struct {
	Id     []byte `bencode:"id,omitempty"`
	Nodes  []byte `bencode:"nodes,omitempty"`
	Nodes6 []byte `bencode:"nodes6,omitempty"`
}

func parseCompact(data []byte, ipv6 bool) []net.TCPAddr {
	peers := pex.ParseCompact(data, nil, ipv6)
	addrs := make([]net.TCPAddr, len(peers))
	for i, p := range peers {
		addrs[i].IP = p.IP
		addrs[i].Port = p.Port
	}
	return addrs
}

func formatCompact(addrs []net.TCPAddr) ([]byte, []byte) {
	peers := make([]pex.Peer, len(addrs))
	for i, a := range addrs {
		peers[i].IP = a.IP
		peers[i].Port = a.Port
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

func Read(filename string) (id []byte, nodes []net.TCPAddr, err error) {

	bdht, info, err := read(filename)

	if err != nil {
		return
	}

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

func Bootstrap(ctx context.Context, nodes []net.TCPAddr) {
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
		n := nodes[ni[i%len(nodes)]]
		dht.Ping(n.IP, uint16(n.Port))
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
			nodes = append(nodes, net.TCPAddr{IP: ip, Port: 6881})
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

		n := nodes[i%len(nodes)]
		dht.Ping(n.IP, uint16(n.Port))
		nap(2, 1)
		if ctx.Err() != nil {
			return
		}
	}
	reannounce(false)
	reannounce(true)
}

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
				t.AddKnown(event.IP, int(event.Port),
					nil, "", known.DHT)
			}
		}
	}
}

func Run(ctx context.Context, id []byte, port int) (<-chan dht.Event, error) {
	return dht.DHT(ctx, id, uint16(port))
}

func nap(n int, m int) {
	time.Sleep(time.Duration(int64(n-m/2)*int64(time.Second) +
		rand.Int63n(int64(m)*int64(time.Second))))
}

func doze() {
	time.Sleep(time.Millisecond +
		time.Duration(rand.Intn(int(time.Millisecond))))
}
