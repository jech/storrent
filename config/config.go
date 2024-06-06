// Package config implements global configuration data for storrent.
package config

import (
	"fmt"
	"sync/atomic"

	"github.com/jech/storrent/hash"
)

// ProtocolPort is the port over which we accept TCP connections and UDP
// datagrams.
var ProtocolPort int

var externalTCPPort, externalUDPPort int32

// ExternalPort returns the external port used for TCP or UDP traffic for
// a given protocol family.  For IPv4, this can be different from ProtocolPort
// if we are behind a NAT.  For IPv6, this is always equal to ProtocolPort.
func ExternalPort(tcp bool, ipv6 bool) int {
	if ipv6 {
		return ProtocolPort
	}
	if tcp {
		return int(atomic.LoadInt32(&externalTCPPort))
	}
	return int(atomic.LoadInt32(&externalUDPPort))
}

// SetExternalIPv4Port records our idea of the external port.
func SetExternalIPv4Port(port int, tcp bool) {
	if tcp {
		atomic.StoreInt32(&externalTCPPort, int32(port))
		return
	}
	atomic.StoreInt32(&externalUDPPort, int32(port))
}

// HTTPAddr is the address on which
var HTTPAddr string

// DHTBootstrap is the filename of the dht.dat file.
var DHTBootstrap string

// DhtID is our DHT id.
var DhtID hash.Hash

const (
	MinPeersPerTorrent = 40
	MaxPeersPerTorrent = 50
)

var MemoryMark int64

func MemoryHighMark() int64 {
	return MemoryMark
}
func MemoryLowMark() int64 {
	return MemoryMark * 7 / 8
}

// ChunkSize defines the size of the chunks that we request.  While in
// principle BEP-3 allows arbitrary powers of two, in practice other
// BitTorrent implementations only honour requests for 16kB.
const ChunkSize uint32 = 16 * 1024

var uploadRate uint32 = 512 * 1024

// UploadRate specifies our maximum upload rate.  This is a hard limit (up
// to fluctuations due to the rate estimator).
func UploadRate() float64 {
	return float64(atomic.LoadUint32(&uploadRate))
}

func SetUploadRate(rate float64) {
	var r uint32
	if rate < 0 {
		r = 0
	} else if rate > float64(^uint32(0)) {
		r = ^uint32(0)
	} else {
		r = uint32(rate + 0.5)
	}
	atomic.StoreUint32(&uploadRate, r)
}

var PrefetchRate float64
var idleRate uint32 = 64 * 1024

// IdleRate is the download rate for which we aim when no client requests
// any data.
func IdleRate() uint32 {
	return atomic.LoadUint32(&idleRate)
}

func SetIdleRate(rate uint32) {
	atomic.StoreUint32(&idleRate, rate)
}

var defaultProxyDialer atomic.Value

func SetDefaultProxy(s string) error {
	defaultProxyDialer.Store(s)
	return nil
}

// DefaultProxy specifies the proxy through which we make both BitTorrent
// and tracker connections.  The DHT does not honour the proxy.
func DefaultProxy() string {
	return defaultProxyDialer.Load().(string)
}

type DhtMode int

const (
	DhtNone DhtMode = iota
	DhtPassive
	DhtNormal
)

func (m DhtMode) String() string {
	switch m {
	case DhtNone:
		return "none"
	case DhtPassive:
		return "passive"
	case DhtNormal:
		return "normal"
	default:
		return fmt.Sprintf("unknown (%v)", int(m))
	}
}

func ParseDhtMode(s string) (DhtMode, error) {
	switch s {
	case "none":
		return DhtNone, nil
	case "passive":
		return DhtPassive, nil
	case "normal":
		return DhtNormal, nil
	default:
		return DhtNone, fmt.Errorf("unknown DHT mode %v", s)
	}
}

// DefaultDhtMode specifies the DHT mode of newly created torrents.
var DefaultDhtMode DhtMode

// DefaultUseTrackers, DefaultUseWebseeds specify the options of newly
// created torrents.
var DefaultUseTrackers, DefaultUseWebseeds bool

// PreferEncryption and ForceEncryption specify the encryption policy for
// newly created torrents.
var PreferEncryption, ForceEncryption bool

var MultipathTCP bool

var Debug bool
