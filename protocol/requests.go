// Package protocol implements low-level details of the BitTorrent protocol.
package protocol

import (
	"net"

	"github.com/zeebo/bencode"

	"github.com/jech/storrent/pex"
)

// The subtypes we negotiate for reception of extended messages.  We send
// the subtypes requested by the peer, as required by the extension mechanism.
const (
	ExtPex        uint8 = 1
	ExtMetadata   uint8 = 2
	ExtDontHave   uint8 = 3
	ExtUploadOnly uint8 = 4
)

// bootOrString is a boolean, but it can unmarshal a string.  This works
// around some buggy peers.
type boolOrString bool

func (bs boolOrString) MarshalBencode() ([]byte, error) {
	return bencode.EncodeBytes(bool(bs))
}

func (bs *boolOrString) UnmarshalBencode(buf []byte) error {
	var b bool
	err1 := bencode.DecodeBytes(buf, &b)
	if err1 == nil {
		*bs = boolOrString(b)
		return nil
	}
	var s string
	err2 := bencode.DecodeBytes(buf, &s)
	if err2 == nil {
		switch s {
		case "0":
			*bs = false
			return nil
		case "1":
			*bs = true
			return nil
		}
	}
	return err1
}

type extensionInfo struct {
	Version      string           `bencode:"v,omitempty"`
	IPv4         []byte           `bencode:"ipv4,omitempty"`
	IPv6         []byte           `bencode:"ipv6,omitempty"`
	Port         uint16           `bencode:"p,omitempty"`
	ReqQ         uint32           `bencode:"reqq,omitempty"`
	MetadataSize uint32           `bencode:"metadata_size,omitempty"`
	Messages     map[string]uint8 `bencode:"m"`
	UploadOnly   boolOrString     `bencode:"upload_only"`
	Encrypt      boolOrString     `bencode:"e,omitempty"`
}

type pexInfo struct {
	Added    []byte `bencode:"added,omitempty"`
	AddedF   []byte `bencode:"added.f,omitempty"`
	Added6   []byte `bencode:"added6,omitempty"`
	Added6F  []byte `bencode:"added6.f,omitempty"`
	Dropped  []byte `bencode:"dropped,omitempty"`
	Dropped6 []byte `bencode:"dropped6,omitempty"`
}

type metadataInfo struct {
	Type      *uint8  `bencode:"msg_type"`
	Piece     *uint32 `bencode:"piece"`
	TotalSize *uint32 `bencode:"total_size"`
}

type Message interface{}
type Error struct {
	Error error
}
type Flush struct{}
type KeepAlive struct{}
type Choke struct{}
type Unchoke struct{}
type Interested struct{}
type NotInterested struct{}
type Have struct {
	Index uint32
}
type Bitfield struct {
	Bitfield []byte
}
type Request struct {
	Index, Begin, Length uint32
}
type Piece struct {
	Index, Begin uint32
	Data         []byte
}
type Cancel struct {
	Index, Begin, Length uint32
}
type Port struct {
	Port uint16
}
type SuggestPiece struct {
	Index uint32
}
type RejectRequest struct {
	Index, Begin, Length uint32
}
type AllowedFast struct {
	Index uint32
}
type HaveAll struct{}
type HaveNone struct{}

type Extended0 struct {
	Version      string
	Port         uint16
	ReqQ         uint32
	IPv4         net.IP
	IPv6         net.IP
	MetadataSize uint32
	Messages     map[string]uint8
	UploadOnly   bool
	Encrypt      bool
}

type ExtendedPex struct {
	Subtype uint8
	Added   []pex.Peer
	Dropped []pex.Peer
}

type ExtendedMetadata struct {
	Subtype   uint8
	Type      uint8
	Piece     uint32
	TotalSize uint32
	Data      []byte
}

type ExtendedDontHave struct {
	Subtype uint8
	Index   uint32
}

type ExtendedUploadOnly struct {
	Subtype uint8
	Value   bool
}

type ExtendedUnknown struct {
	Subtype uint8
}
type Unknown struct{ tpe uint8 }
