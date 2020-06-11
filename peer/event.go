package peer

import (
	"net"
	"time"

	"github.com/jech/storrent/bitmap"
	"github.com/jech/storrent/hash"
	"github.com/jech/storrent/known"
	"github.com/jech/storrent/pex"
)

type TorEvent interface{}

type TorAddPeer struct {
	Peer *Peer
	Init []byte
}

type TorPeerExtended struct {
	Peer         *Peer
	MetadataSize uint32
}

type TorAddKnown struct {
	Peer    *Peer
	IP      net.IP
	Port    int
	Id      hash.Hash
	Version string
	Kind    known.Kind
}

type TorBadPeer struct {
	Peer uint32
	Bad  bool
}

type TorStats struct {
	Length      int64
	PieceLength uint32
	NumPeers    int
	NumKnown    int
	NumTrackers int
	NumWebseeds int
}

type TorGetStats struct {
	Ch chan<- *TorStats
}

type TorGetAvailable struct {
	Ch chan<- []uint16
}

type TorDropPeer struct {
	Ch chan<- bool
}

type TorGetPeer struct {
	Id hash.Hash
	Ch chan<- *Peer
}

type TorGetPeers struct {
	Ch chan<- []*Peer
}

type TorGetKnown struct {
	Id   hash.Hash
	IP   net.IP
	Port int
	Ch   chan<- known.Peer
}

type TorGetKnowns struct {
	Ch chan<- []known.Peer
}

type TorPeerGoaway struct {
	Peer *Peer
}

type TorData struct {
	Peer                 *Peer
	Index, Begin, Length uint32
	Complete             bool
}

type TorDrop struct {
	Index, Begin, Length uint32
}

type TorRequest struct {
	Index    uint32
	Priority int8
	Request  bool
	Ch       chan<- <-chan struct{}
}

type TorMetaData struct {
	Peer        *Peer
	Size, Index uint32
	Data        []byte
}

type TorPeerBitmap struct {
	Peer   *Peer
	Bitmap bitmap.Bitmap
	Have   bool
}

type TorPeerHave struct {
	Peer  *Peer
	Index uint32
	Have  bool
}

type TorHave struct {
	Index uint32
	Have  bool
}

type TorPeerInterested struct {
	Peer       *Peer
	Interested bool
}

type TorPeerUnchoke struct {
	Peer    *Peer
	Unchoke bool
}

type TorGoAway struct {
}

type TorAnnounce struct {
	IPv6 bool
}

type ConfValue int

const (
	ConfUnknown ConfValue = 0
	ConfFalse   ConfValue = 1
	ConfTrue    ConfValue = 2
)

func ConfGet(v bool) ConfValue {
	if v {
		return ConfTrue
	}
	return ConfFalse
}

func ConfSet(dst *bool, v ConfValue) {
	switch v {
	case ConfTrue:
		*dst = true
	case ConfFalse:
		*dst = false
	}
}

type TorConf struct {
	UseDht      ConfValue
	DhtPassive  ConfValue
	UseTrackers ConfValue
	UseWebseeds ConfValue
}

type TorSetConf struct {
	Conf TorConf
	Ch   chan<- struct{}
}

type TorGetConf struct {
	Ch chan<- TorConf
}

type PeerEvent interface{}

type PeerMetadataComplete struct {
	Info []byte
}

type PeerRequest struct {
	Chunks []uint32
}

type PeerHave struct {
	Index uint32
	Have  bool
}

type PeerCancel struct {
	Chunk uint32
}

type PeerCancelPiece struct {
	Index uint32
}

type PeerInterested struct {
	Interested bool
}

type PeerGetMetadata struct {
	Index uint32
}

type PeerPex struct {
	Peers []pex.Peer
	Add   bool
}

type PeerStatus struct {
	Unchoked     bool
	Interested   bool
	AmUnchoking  bool
	AmInterested bool
	Seed         bool
	UploadOnly   bool
	Qlen         int
	Download     float64
	AvgDownload  float64
	UnchokeTime  time.Time
}

type PeerGetStatus struct {
	Ch chan<- PeerStatus
}

type PeerStats struct {
	Unchoked     bool
	Interested   bool
	AmUnchoking  bool
	AmInterested bool
	Seed         bool
	UploadOnly   bool
	HasProxy     bool
	Download     float64
	AvgDownload  float64
	Upload       float64
	Rtt          time.Duration
	Rttvar       time.Duration
	UnchokeTime  time.Time
	Rlen         int
	Qlen         int
	Ulen         int
	PieceCount   int
	NumPex       int
}

type PeerGetPex struct {
	Ch chan<- pex.Peer
}
type PeerGetBitmap struct {
	Ch chan<- bitmap.Bitmap
}

type PeerGetHave struct {
	Index uint32
	Ch    chan<- bool
}

type PeerGetStats struct {
	Ch chan<- PeerStats
}

type PeerGetFast struct {
	Ch chan<- []uint32
}

type PeerUnchoke struct {
	Unchoke bool
}

type PeerDone struct {
}
