package known

import (
	"net/netip"
	"slices"
	"time"

	"github.com/jech/storrent/hash"
)

type Peer struct {
	Addr               netip.AddrPort
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

type Peers map[netip.AddrPort]*Peer

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
	when := slices.MaxFunc([]time.Time{
		kp.ActiveTime, kp.SeenTime, kp.HeardTime, kp.ActiveTime,
		kp.TrackerTime, kp.DHTTime, kp.PEXTime,
	}, func(a, b time.Time) int { return a.Compare(b) })
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

func Find(peers Peers, addr netip.AddrPort, id hash.Hash, version string, kind Kind) *Peer {

	kp := peers[addr]

	if kp != nil {
		if id != nil && kp.Id != nil && !id.Equal(kp.Id) {
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

	if !addr.Addr().IsGlobalUnicast() {
		return nil
	}
	kp = &Peer{Addr: addr, Id: id}
	kp.Update(version, kind)
	peers[addr] = kp
	return kp
}

func FindId(peers Peers, id hash.Hash, addr netip.AddrPort) *Peer {
	for _, kp := range peers {
		if (kp.Addr == addr ||
			(addr.Port() == 0 && kp.Addr.Addr() == addr.Addr())) &&
			(kp.Id != nil && id.Equal(kp.Id)) {
			return kp
		}
	}
	return nil
}
