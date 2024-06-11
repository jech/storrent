package protocol

import (
	"bufio"
	"encoding/binary"
	"log"
	"net"
	"time"

	"github.com/zeebo/bencode"

	"github.com/jech/storrent/pex"
)

func sendMessage(w *bufio.Writer, tpe byte, data1, data2, data3 []byte) error {
	buf := w.AvailableBuffer()
	buf = binary.BigEndian.AppendUint32(
		buf, uint32(len(data1)+len(data2)+len(data3)+1),
	)
	buf = append(buf, tpe)
	_, err := w.Write(buf)
	if err != nil {
		return err
	}
	if data1 != nil {
		_, err = w.Write(data1)
		if err != nil {
			return err
		}
	}
	if data2 != nil {
		_, err = w.Write(data2)
		if err != nil {
			return err
		}
	}
	if data3 != nil {
		_, err = w.Write(data3)
		if err != nil {
			return err
		}
	}
	return nil
}

func sendMessage0(w *bufio.Writer, tpe byte) error {
	buf := w.AvailableBuffer()
	buf = binary.BigEndian.AppendUint32(buf, 1)
	buf = append(buf, tpe)
	_, err := w.Write(buf)
	return err
}

func sendMessageShort(w *bufio.Writer, tpe byte, v uint16) error {
	buf := w.AvailableBuffer()
	buf = binary.BigEndian.AppendUint32(buf, 3)
	buf = append(buf, tpe)
	buf = binary.BigEndian.AppendUint16(buf, v)
	_, err := w.Write(buf)
	return err
}

func sendMessage1(w *bufio.Writer, tpe byte, v uint32) error {
	buf := w.AvailableBuffer()
	buf = binary.BigEndian.AppendUint32(buf, 5)
	buf = append(buf, tpe)
	buf = binary.BigEndian.AppendUint32(buf, v)
	_, err := w.Write(buf)
	return err
}

func sendMessage3(w *bufio.Writer, tpe byte, v1, v2, v3 uint32) error {
	buf := w.AvailableBuffer()
	buf = binary.BigEndian.AppendUint32(buf, 13)
	buf = append(buf, tpe)
	buf = binary.BigEndian.AppendUint32(buf, v1)
	buf = binary.BigEndian.AppendUint32(buf, v2)
	buf = binary.BigEndian.AppendUint32(buf, v3)
	_, err := w.Write(buf)
	return err
}

func sendExtended(w *bufio.Writer, subtype byte, data1, data2 []byte) error {
	return sendMessage(w, 20, []byte{subtype}, data1, data2)
}

// Write writes a single BitTorrent message to w.  If l is not nil, then
// the message is logged.
func Write(w *bufio.Writer, m Message, l *log.Logger) error {
	debugf := func(format string, v ...interface{}) {
		if l != nil {
			l.Printf(format, v...)
		}
	}
	switch m := m.(type) {
	case KeepAlive:
		debugf("-> KeepAlive")
		_, err := w.Write([]byte{0, 0, 0, 0})
		return err
	case Choke:
		debugf("-> Choke")
		return sendMessage0(w, 0)
	case Unchoke:
		debugf("-> Unchoke")
		return sendMessage0(w, 1)
	case Interested:
		debugf("-> Interested")
		return sendMessage0(w, 2)
	case NotInterested:
		debugf("-> NotInterested")
		return sendMessage0(w, 3)
	case Have:
		debugf("-> Have %v", m.Index)
		return sendMessage1(w, 4, m.Index)
	case Bitfield:
		debugf("-> Bitfield %v", len(m.Bitfield))
		return sendMessage(w, 5, m.Bitfield, nil, nil)
	case Request:
		debugf("-> Request %v %v %v", m.Index, m.Begin, m.Length)
		return sendMessage3(w, 6, m.Index, m.Begin, m.Length)
	case Piece:
		debugf("-> Piece %v %v %v", m.Index, m.Begin, len(m.Data))
		buf := w.AvailableBuffer()
		buf = binary.BigEndian.AppendUint32(buf,
			uint32(1+8+len(m.Data)))
		buf = append(buf, 7)
		buf = binary.BigEndian.AppendUint32(buf, m.Index)
		buf = binary.BigEndian.AppendUint32(buf, m.Begin)
		_, err := w.Write(buf)
		if err == nil {
			_, err = w.Write(m.Data)
		}
		PutBuffer(m.Data)
		m.Data = nil
		return err
	case Cancel:
		debugf("-> Cancel %v %v %v", m.Index, m.Begin, m.Length)
		return sendMessage3(w, 8, m.Index, m.Begin, m.Length)
	case Port:
		debugf("-> Port %v", m.Port)
		return sendMessageShort(w, 9, m.Port)
	case SuggestPiece:
		debugf("-> SuggestPiece %v", m.Index)
		return sendMessage1(w, 13, m.Index)
	case HaveAll:
		debugf("-> HaveAll")
		return sendMessage0(w, 14)
	case HaveNone:
		debugf("-> HaveNone")
		return sendMessage0(w, 15)
	case RejectRequest:
		debugf("-> RejectRequest %v %v %v",
			m.Index, m.Begin, m.Length)
		return sendMessage3(w, 16, m.Index, m.Begin, m.Length)
	case AllowedFast:
		debugf("-> AllowedFast %v", m.Index)
		return sendMessage1(w, 17, m.Index)
	case Extended0:
		debugf("-> Extended0")
		var f extensionInfo
		f.Version = m.Version
		if m.IPv6.IsValid() {
			f.IPv6 = m.IPv6.AsSlice()
		}
		if m.IPv4.IsValid() {
			f.IPv4 = m.IPv4.AsSlice()
		}
		f.Port = m.Port
		f.ReqQ = m.ReqQ
		f.MetadataSize = m.MetadataSize
		f.Messages = m.Messages
		f.UploadOnly = boolOrString(m.UploadOnly)
		f.Encrypt = boolOrString(m.Encrypt)
		b, err := bencode.EncodeBytes(f)
		if err != nil {
			return err
		}
		return sendExtended(w, 0, b, nil)
	case ExtendedMetadata:
		debugf("-> ExtendedMetadata %v %v", m.Type, m.Piece)
		tpe := m.Type
		piece := m.Piece
		info := &metadataInfo{Type: &tpe, Piece: &piece}
		if m.TotalSize > 0 {
			totalsize := m.TotalSize
			info.TotalSize = &totalsize
		}
		b, err := bencode.EncodeBytes(info)
		if err != nil {
			return err
		}
		if m.Subtype == 0 {
			panic("ExtendedMetadata subtype is 0")
		}
		return sendExtended(w, m.Subtype, b, m.Data)
	case ExtendedPex:
		debugf("-> ExtendedPex %v %v", len(m.Added), len(m.Dropped))
		a4, f4, a6, f6 := pex.FormatCompact(m.Added)
		d4, _, d6, _ := pex.FormatCompact(m.Dropped)
		info := pexInfo{
			Added:    a4,
			AddedF:   f4,
			Added6:   a6,
			Added6F:  f6,
			Dropped:  d4,
			Dropped6: d6,
		}
		b, err := bencode.EncodeBytes(info)
		if err != nil {
			return err
		}
		return sendExtended(w, m.Subtype, b, nil)
	case ExtendedDontHave:
		debugf("-> ExtendedDontHave %v", m.Index)
		b := make([]byte, 4)
		binary.BigEndian.PutUint32(b, m.Index)
		if m.Subtype == 0 {
			panic("ExtendedDontHave subtype is 0")
		}
		return sendExtended(w, m.Subtype, b, nil)
	default:
		panic("Unknown message")
	}
}

// Writer writes BitTorrent messages to conn until either conn or ch is
// closed.  To closes done when it's done.  If l is not nil, then all
// messages written are logged.
func Writer(conn net.Conn, l *log.Logger, ch <-chan Message, done chan<- struct{}) error {
	defer close(done)

	w := bufio.NewWriter(conn)

	write := func(m Message) error {
		err := conn.SetWriteDeadline(time.Now().Add(time.Minute))
		if err != nil {
			return err
		}
		return Write(w, m, l)
	}

	flush := func() error {
		err := conn.SetWriteDeadline(time.Now().Add(time.Minute))
		if err != nil {
			return err
		}
		return w.Flush()
	}

	for {
		m, ok := <-ch
		if !ok {
			return nil
		}
		err := write(m)
		if err != nil {
			return err
		}
	inner:
		for {
			select {
			case m, ok := <-ch:
				if !ok {
					return nil
				}
				err := write(m)
				if err != nil {
					return err
				}
			default:
				break inner
			}
		}

		err = flush()
		if err != nil {
			return err
		}
	}
}
