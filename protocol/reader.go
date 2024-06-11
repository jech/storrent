package protocol

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"log"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/zeebo/bencode"

	"github.com/jech/storrent/config"
	"github.com/jech/storrent/pex"
)

var ErrParse = errors.New("parse error")

var pool sync.Pool = sync.Pool{
	New: func() interface{} {
		return make([]byte, config.ChunkSize)
	},
}

// GetBuffer gets a buffer suitable for storing a chunk of BitTorrent data.
func GetBuffer(length int) []byte {
	if length == int(config.ChunkSize) {
		buf := pool.Get().([]byte)
		return buf
	}
	return make([]byte, length)
}

// PutBuffer releases a buffer obtained by GetBuffer.  The buffer must not
// be used again.
func PutBuffer(buf []byte) {
	if len(buf) == int(config.ChunkSize) {
		pool.Put(buf)
	}
}

func readUint16(r *bufio.Reader) (uint16, error) {
	var v uint16
	err := binary.Read(r, binary.BigEndian, &v)
	return v, err
}

func readUint32(r *bufio.Reader) (uint32, error) {
	var v uint32
	err := binary.Read(r, binary.BigEndian, &v)
	return v, err
}

// Read reads a single BitTorrent message from r.  If l is not nil, then
// the message is logged.
func Read(r *bufio.Reader, l *log.Logger) (Message, error) {
	debugf := func(format string, v ...interface{}) {
		if l != nil {
			l.Printf(format, v...)
		}
	}
	length, err := readUint32(r)
	if err != nil {
		return nil, err
	}

	if length == 0 {
		debugf("<- KeepAlive")
		return KeepAlive{}, nil
	}

	if length > 1024*1024 {
		return nil, errors.New("TLV too long")
	}

	tpe, err := r.ReadByte()
	if err != nil {
		return nil, err
	}

	switch tpe {
	case 0:
		if length != 1 {
			return nil, ErrParse
		}
		debugf("<- Choke")
		return Choke{}, nil
	case 1:
		if length != 1 {
			return nil, ErrParse
		}
		debugf("<- Unchoke")
		return Unchoke{}, nil
	case 2:
		if length != 1 {
			return nil, ErrParse
		}
		debugf("<- Interested")
		return Interested{}, nil
	case 3:
		if length != 1 {
			return nil, ErrParse
		}
		debugf("<- NotInterested")
		return NotInterested{}, nil
	case 4:
		if length != 5 {
			return nil, ErrParse
		}
		index, err := readUint32(r)
		if err != nil {
			return nil, err
		}
		debugf("<- Have %v", index)
		return Have{index}, nil
	case 5:
		if length < 1 {
			return nil, ErrParse
		}
		bf := make([]byte, length-1)
		_, err := io.ReadFull(r, bf)
		if err != nil {
			return nil, err
		}
		debugf("<- Bitfield %v", len(bf))
		return Bitfield{bf}, nil
	case 6, 8, 16:
		if length != 13 {
			return nil, ErrParse
		}
		index, err := readUint32(r)
		if err != nil {
			return nil, err
		}
		begin, err := readUint32(r)
		if err != nil {
			return nil, err
		}
		length, err := readUint32(r)
		if err != nil {
			return nil, err
		}
		if tpe == 6 {
			debugf("<- Request %v %v %v", index, begin, length)
			return Request{index, begin, length}, nil
		} else if tpe == 8 {
			debugf("<- Cancel %v %v %v", index, begin, length)
			return Cancel{index, begin, length}, nil
		} else if tpe == 16 {
			debugf("<- RejectRequest %v %v %v",
				index, begin, length)
			return RejectRequest{index, begin, length}, nil
		}
	case 7:
		if length < 9 {
			return nil, ErrParse
		}
		index, err := readUint32(r)
		if err != nil {
			return nil, err
		}
		begin, err := readUint32(r)
		if err != nil {
			return nil, err
		}
		if l != nil {
			debugf("<- Piece %v %v %v", index, begin, length-9)
		}
		data := GetBuffer(int(length - 9))
		_, err = io.ReadFull(r, data)
		if err != nil {
			PutBuffer(data)
			return nil, err
		}
		return Piece{index, begin, data}, nil
	case 9:
		if length != 3 {
			return nil, ErrParse
		}
		port, err := readUint16(r)
		if err != nil {
			return nil, err
		}
		debugf("<- Port %v", port)
		return Port{port}, nil
	case 13, 17:
		if length != 5 {
			return nil, ErrParse
		}
		index, err := readUint32(r)
		if err != nil {
			return nil, err
		}
		if tpe == 13 {
			debugf("<- SuggestPiece %v", index)
			return SuggestPiece{index}, nil
		} else {
			debugf("<- AllowedFast %v", index)
			return AllowedFast{index}, nil
		}
	case 14, 15:
		if length != 1 {
			return nil, err
		}
		if tpe == 14 {
			debugf("<- HaveAll")
			return HaveAll{}, nil
		} else {
			debugf("<- HaveNone")
			return HaveNone{}, nil
		}
	case 20:
		subtype, err := r.ReadByte()
		if err != nil {
			return nil, err
		}

		switch subtype {
		case 0:
			var ext extensionInfo
			lr := io.LimitReader(r, int64(length-2))
			decoder := bencode.NewDecoder(lr)
			err = decoder.Decode(&ext)
			if err != nil {
				return nil, err
			}
			_, err = io.Copy(io.Discard, lr)
			if err != nil {
				return nil, err
			}
			m := Extended0{}

			m.Version = ext.Version
			m.Port = ext.Port
			m.ReqQ = ext.ReqQ
			ip, ok := netip.AddrFromSlice(ext.IPv4)
			if ok && ip.Is4() {
				m.IPv4 = ip
			}
			ip, ok = netip.AddrFromSlice(ext.IPv6)
			if ok && ip.Is6() {
				m.IPv6 = ip
			}
			m.MetadataSize = ext.MetadataSize
			if len(ext.Messages) > 0 {
				m.Messages = ext.Messages
			}
			m.UploadOnly = bool(ext.UploadOnly)
			m.Encrypt = bool(ext.Encrypt)
			debugf("<- Extended0 %v", m.Version)
			return m, nil
		case ExtPex:
			var info pexInfo
			lr := io.LimitReader(r, int64(length-2))
			decoder := bencode.NewDecoder(lr)
			err := decoder.Decode(&info)
			if err != nil {
				return nil, err
			}
			_, err = io.Copy(io.Discard, lr)
			if err != nil {
				return nil, err
			}
			var added, dropped []pex.Peer
			if info.Added != nil && len(info.Added)%6 == 0 {
				added = append(added,
					pex.ParseCompact(info.Added,
						info.AddedF, false)...)
			}
			if info.Added6 != nil && len(info.Added6)%18 == 0 {
				added = append(added,
					pex.ParseCompact([]byte(info.Added6),
						info.Added6F, true)...)
			}
			if info.Dropped != nil && len(info.Dropped)%6 == 0 {
				dropped = append(dropped,
					pex.ParseCompact(info.Dropped,
						nil, false)...)
			}
			if info.Dropped6 != nil && len(info.Dropped6)%18 == 0 {
				dropped = append(dropped,
					pex.ParseCompact(info.Dropped6,
						nil, true)...)
			}
			debugf("<- ExtendedPex %v %v",
				len(added), len(dropped))
			return ExtendedPex{ExtPex, added, dropped}, nil
		case ExtMetadata:
			var m metadataInfo
			data := make([]byte, length-2)
			_, err := io.ReadFull(r, data)
			if err != nil {
				return nil, err
			}
			decoder := bencode.NewDecoder(bytes.NewReader(data))
			err = decoder.Decode(&m)
			if err != nil {
				return nil, err
			}
			var tpe uint8
			var index, totalSize uint32
			if m.Type == nil || m.Piece == nil {
				return nil, ErrParse
			}
			tpe = *m.Type
			index = *m.Piece
			if m.TotalSize != nil {
				totalSize = *m.TotalSize
			}
			var metadata []byte
			count := decoder.BytesParsed()
			if count < len(data) {
				metadata = make([]byte, len(data)-count)
				copy(metadata, data[count:])
			}
			debugf("<- ExtendedMetadata %v %v", tpe, index)
			return ExtendedMetadata{ExtMetadata,
				tpe, index, totalSize, metadata}, nil
		case ExtDontHave:
			if length-2 != 4 {
				return nil, ErrParse
			}
			index, err := readUint32(r)
			if err != nil {
				return nil, err
			}
			debugf("<- ExtendedDontHave %v", index)
			return ExtendedDontHave{ExtDontHave, index}, nil
		case ExtUploadOnly:
			if length-2 != 1 {
				return nil, ErrParse
			}
			v, err := r.ReadByte()
			if err != nil || (v != 0 && v != 1) {
				return nil, ErrParse
			}
			debugf("<- ExtendedUploadOnly %v", v)
			return ExtendedUploadOnly{ExtUploadOnly, v == 1}, nil
		default:
			debugf("<- ExtendedUnknown %v %v", subtype, length-2)
			_, err := r.Discard(int(length - 2))
			if err != nil {
				return nil, err
			}
			return ExtendedUnknown{subtype}, nil
		}
	}
	_, err = r.Discard(int(length) - 1)
	if err != nil {
		return nil, err
	}
	return Unknown{tpe}, nil
}

// Reader reads BitTorrent messages from c until it is closed.  The
// parameter init contains data that is prepended to the data received
// from c.  If l is not nil, then all messages read are logged.
func Reader(c net.Conn, init []byte, l *log.Logger, ch chan<- Message, done <-chan struct{}) {
	defer close(ch)

	var r *bufio.Reader
	if len(init) == 0 {
		r = bufio.NewReader(c)
	} else {
		r = bufio.NewReader(io.MultiReader(bytes.NewReader(init), c))
	}
	for {
		var m Message
		err := c.SetReadDeadline(time.Now().Add(6 * time.Minute))
		if err == nil {
			m, err = Read(r, l)
		}
		if err != nil {
			m = Error{err}
		}
		select {
		case ch <- m:
		case <-done:
			return
		}
	}
}
