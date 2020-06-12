package config

import (
	"sync/atomic"

	"github.com/jech/storrent/hash"
)

var ProtocolPort int
var externalTCPPort int32
var externalUDPPort int32

func ExternalPort(tcp bool, ipv6 bool) int {
	if ipv6 {
		return ProtocolPort
	}
	if tcp {
		return int(atomic.LoadInt32(&externalTCPPort))
	}
	return int(atomic.LoadInt32(&externalUDPPort))
}

func SetExternalIPv4Port(port int, tcp bool) {
	if tcp {
		atomic.StoreInt32(&externalTCPPort, int32(port))
		return
	}
	atomic.StoreInt32(&externalUDPPort, int32(port))
}

var HTTPAddr string

var DHTBootstrap string
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

const ChunkSize uint32 = 16 * 1024

var uploadRate uint32 = 512 * 1024

func UploadRate() float64 {
	return float64(atomic.LoadUint32(&uploadRate))
}

func SetUploadRate(rate float64) {
	var r uint32
	if rate < 0 {
		r = 0
	} else 	if rate > float64(^uint32(0)) {
		r = ^uint32(0)
	} else {
		r = uint32(rate + 0.5)
	}
	atomic.StoreUint32(&uploadRate, r)
}

var PrefetchRate float64
var idleRate uint32 = 64 * 1024

func IdleRate() float64 {
	return float64(atomic.LoadUint32(&idleRate))
}
func SetIdleRate(rate float64) {
	if rate < 0 {
		rate = 0
	}
	if rate > float64(^uint32(0)) {
		rate = float64(^uint32(0))
	}
	atomic.StoreUint32(&idleRate, uint32(rate+0.5))
}

var defaultProxyDialer atomic.Value

func SetDefaultProxy(s string) error {
	defaultProxyDialer.Store(s)
	return nil
}

func DefaultProxy() string {
	return defaultProxyDialer.Load().(string)
}

var DefaultUseDht, DefaultDhtPassive, DefaultUseTrackers, DefaultUseWebseeds bool
var DefaultEncryption int

var Debug bool
