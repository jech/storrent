package known

import (
	"net"
	"time"

	"github.com/jech/storrent/hash"
)

type Peer struct {
	Addr               net.TCPAddr
	Id                 hash.Hash
	TrackerTime        time.Time
	DHTTime            time.Time
	PEXTime            time.Time
	HeardTime          time.Time
	SeenTime           time.Time
	ActiveTime         time.Time
	ConnectAttemptTime time.Time
	BadTime            time.Time
	Attempts           uint
	Badness            int
	Version            string
}

type key struct {
	ip   [16]byte
	port uint16
}

type Peers map[key]*Peer

type Kind int

const (
	None Kind = iota
	Tracker
	DHT
	PEX
	Heard
	Seen
	Active
	ActiveNoReset
	ConnectAttempt
	Good
	Bad
)

func (kp *Peer) Update(version string, kind Kind) {
	switch kind {
	case None:

	case Tracker:
		kp.TrackerTime = time.Now()
	case DHT:
		kp.DHTTime = time.Now()
	case PEX:
		kp.PEXTime = time.Now()
	case Heard:
		kp.HeardTime = time.Now()
	case Seen:
		kp.SeenTime = time.Now()
	case Active:
		kp.ActiveTime = time.Now()
		kp.Attempts = 0
	case ActiveNoReset:
		kp.ActiveTime = time.Now()
	case ConnectAttempt:
		kp.ConnectAttemptTime = time.Now()
		kp.Attempts++
	case Good:
		if kp.Badness > 0 {
			kp.Badness--
		}
	case Bad:
		kp.BadTime = time.Now()
		kp.Badness += 5
	default:
		panic("Unknown known type")
	}
	if version != "" {
		kp.Version = version
	}
}

func (kp *Peer) Recent() bool {
	if time.Since(kp.ActiveTime) < time.Hour {
		return true
	}
	if time.Since(kp.SeenTime) < time.Hour {
		return true
	}
	if time.Since(kp.DHTTime) < 35*time.Minute {
		return true
	}
	if time.Since(kp.TrackerTime) < 35*time.Minute {
		return true
	}
	if time.Since(kp.HeardTime) < time.Hour {
		return true
	}
	if time.Since(kp.PEXTime) < time.Hour {
		return true
	}
	return false
}

func (kp *Peer) Bad() bool {
	return kp.Badness > 20
}

func (kp *Peer) ReallyBad() bool {
	return kp.Badness > 50
}

func (kp *Peer) age() time.Duration {
	when := kp.ActiveTime
	if kp.SeenTime.After(when) {
		when = kp.SeenTime
	}
	if kp.HeardTime.After(when) {
		when = kp.HeardTime
	}
	if kp.ActiveTime.After(when) {
		when = kp.ActiveTime
	}
	if kp.TrackerTime.After(when) {
		when = kp.TrackerTime
	}
	if kp.DHTTime.After(when) {
		when = kp.DHTTime
	}
	if kp.PEXTime.After(when) {
		when = kp.PEXTime
	}
	return time.Since(when)
}

func (ps Peers) Count() int {
	return len(ps)
}

func (ps Peers) Expire() {
	for k, p := range ps {
		if p.age() > time.Hour {
			delete(ps, k)
			continue
		}
		if p.Badness > 0 && time.Since(p.BadTime) > 15*time.Minute {
			p.Badness = 0
		}
	}
}

func toKey(ip net.IP, port int) key {
	k := key{}
	copy(k.ip[:], ip.To16())
	k.port = uint16(port)
	return k
}

func Find(peers Peers, ip net.IP, port int, id hash.Hash, version string,
	kind Kind) *Peer {

	if port < 0 || port > 0xFFFF {
		return nil
	}

	key := toKey(ip, port)
	kp := peers[key]

	if kp != nil {
		if id != nil && kp.Id != nil && !id.Equals(kp.Id) {
			kp.Id = nil
		}
		if kp.Id == nil {
			kp.Id = id
		}
		kp.Update(version, kind)
		return kp
	}

	if kind == None {
		return nil
	}

	if !ip.IsGlobalUnicast() {
		return nil
	}
	kp = &Peer{Addr: net.TCPAddr{IP: ip, Port: port}, Id: id}
	kp.Update(version, kind)
	peers[key] = kp
	return kp
}

func FindId(peers Peers, id hash.Hash, ip net.IP, port int) *Peer {
	for _, kp := range peers {
		if kp.Addr.IP.Equal(ip) &&
			(port == 0 || kp.Addr.Port == port) &&
			kp.Id != nil && id.Equals(kp.Id) {
			return kp
		}
	}
	return nil
}
