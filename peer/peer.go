package peer

import (
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"os"
	"sync/atomic"
	"time"

	"github.com/jech/storrent/bitmap"
	"github.com/jech/storrent/config"
	"github.com/jech/storrent/crypto"
	"github.com/jech/storrent/dht"
	"github.com/jech/storrent/hash"
	"github.com/jech/storrent/known"
	"github.com/jech/storrent/peer/requests"
	"github.com/jech/storrent/pex"
	"github.com/jech/storrent/protocol"
	"github.com/jech/storrent/rate"
	"github.com/jech/storrent/tor/piece"
)

var peerCounter uint32

var UploadEstimator, DownloadEstimator rate.AtomicEstimator

var numUnchoking int32

func NumUnchoking() int {
	return int(atomic.LoadInt32(&numUnchoking))
}

const reqQ = 250

type Requested struct {
	Index, Begin, Length uint32
}

type pexState struct {
	pending    []pex.Peer
	pendingDel []pex.Peer
	sent       []pex.Peer
	sentTime   time.Time
}

type Peer struct {
	proxy             string
	infoHash          hash.Hash
	Counter           uint32
	Id                hash.Hash
	IP                net.IP
	Port              uint32
	Incoming          bool
	conn              net.Conn
	Info              []byte
	Pieces            *piece.Pieces
	myBitmap          bitmap.Bitmap
	bitmap            bitmap.Bitmap
	isSeed            bool
	unchoked          uint32
	interested        uint32
	amUnchoking       uint32
	shouldInterested  bool
	amInterested      bool
	gotExtended       bool
	canDHT            bool
	canFast           bool
	canExtended       bool
	pexExt            uint32
	metadataExt       uint32
	dontHaveExt       uint32
	uploadOnlyExt     uint32
	prefersEncryption bool
	uploadOnly        bool
	Event             chan PeerEvent
	Done              chan struct{}
	torEvent          chan<- TorEvent
	torDone           <-chan struct{}
	events            []TorEvent
	writer            chan protocol.Message
	writerDone        <-chan struct{}
	reqQ              int
	requests          requests.Requests
	requested         []Requested
	time              time.Time
	writeTime         time.Time
	pex               []pex.Peer
	pexTorTime        time.Time
	pexState          pexState
	download          rate.Estimator
	avgDownload       rate.Estimator
	upload            rate.Estimator
	rtt               time.Duration
	rttvar            time.Duration
	Log               *log.Logger
	uploadInterval    time.Duration
	uploadTicker      *time.Ticker
	hasFast           uint32
	fast              []uint32
	unchokeTime       time.Time
	lastActive        time.Time
}

var ErrCongested = errors.New("peer is congested")
var ErrMetadataIncomplete = errors.New("metadata incomplete")
var ErrCannotFast = errors.New("peer doesn't implement Fast extension")
var ErrRange = errors.New("value out of range")

func New(proxy string, conn net.Conn, ip net.IP, port int,
	incoming bool, result protocol.HandshakeResult) *Peer {
	counter := atomic.AddUint32(&peerCounter, 1)
	if counter == 0 {
		panic("Eek!")
	}
	prefix := fmt.Sprintf("%4v ", counter)
	peer := &Peer{
		proxy:       proxy,
		infoHash:    result.Hash,
		Counter:     counter,
		Id:          result.Id,
		IP:          ip,
		Port:        uint32(port),
		conn:        conn,
		Incoming:    incoming,
		canDHT:      result.Dht,
		canFast:     result.Fast,
		canExtended: result.Extended,
		Event:       make(chan PeerEvent, 256),
		Done:        make(chan struct{}),
		Log:         log.New(os.Stderr, prefix, log.LstdFlags),
	}
	peer.download.Init(3 * time.Second)
	peer.avgDownload.Init(10 * time.Second)
	peer.avgDownload.Start()
	peer.upload.Init(5 * time.Second)
	return peer
}

func toChunk(peer *Peer, index uint32, begin uint32) uint32 {
	cpp := peer.Pieces.PieceSize() / config.ChunkSize
	if index > math.MaxUint32/cpp {
		return 0
	}
	return index*cpp + begin/config.ChunkSize
}

func fromChunk(peer *Peer, chunk uint32) (uint32, uint32) {
	ps := peer.Pieces.PieceSize()
	index := chunk / (ps / config.ChunkSize)
	begin := (chunk * config.ChunkSize) % ps
	return index, begin
}

func chunkSize(peer *Peer, chunk uint32) uint32 {
	l := peer.Pieces.Length()
	if chunk < uint32(l/int64(config.ChunkSize)) {
		return config.ChunkSize
	} else {
		return uint32(l % int64(config.ChunkSize))
	}
}

func numPieces(peer *Peer) int {
	ps := int64(peer.Pieces.PieceSize())
	return int((peer.Pieces.Length() + ps - 1) / ps)
}

func Run(peer *Peer, torEvent chan<- TorEvent, torDone <-chan struct{},
	info []byte, bitmap bitmap.Bitmap, init []byte) error {
	peer.torEvent = torEvent
	peer.torDone = torDone
	peer.Info = info
	peer.myBitmap = bitmap

	defer func() {
		if config.Debug {
			peer.Log.Printf("Close")
		}
		peer.conn.Close()
		if peer.amUnchoking != 0 {
			n := atomic.AddInt32(&numUnchoking, -1)
			if n < 0 {
				panic("NumUnchoking is negative")
			}
		}
	}()

	readTimeout := func(to time.Duration) error {
		return peer.conn.SetReadDeadline(time.Now().Add(to))
	}

	writeTimeout := func(to time.Duration) error {
		return peer.conn.SetWriteDeadline(time.Now().Add(to))
	}

	logger := peer.Log
	if !config.Debug {
		logger = nil
	}

	reader := make(chan protocol.Message, 32)
	go protocol.Reader(peer.conn, init, logger, reader, peer.Done)

	peer.writer = make(chan protocol.Message, 64)
	writerDone := make(chan struct{})
	peer.writerDone = writerDone
	defer func() {
		close(peer.writer)
		writeTimeout(time.Microsecond)
	}()
	go func() {
		err := protocol.Writer(peer.conn, logger,
			peer.writer, writerDone)
		if err != nil {
			peer.Log.Printf("write: %v", err)
		}
	}()

	peer.reqQ = 128
	peer.time = time.Now()
	peer.writeTime = time.Now()
	if peer.Port > 0 {
		writeEvent(peer, TorAddKnown{peer,
			peer.IP, int(peer.Port), peer.Id, "",
			known.ActiveNoReset,
		})
	}

	if peer.canDHT && !hasProxy(peer) {
		write(peer,
			protocol.Port{uint16(config.ExternalPort(false,
				peer.IP.To4() == nil))})
	}

	if peer.canExtended {
		var version string
		var port uint16
		var ipv6 net.IP
		if !hasProxy(peer) {
			version = "STorrent 0.0"
			port = uint16(config.ExternalPort(true,
				peer.IP.To4() == nil))
			ipv6 = getIPv6()
		}
		err := write(peer, protocol.Extended0{
			Version:      version,
			Port:         port,
			ReqQ:         reqQ,
			IPv6:         ipv6,
			MetadataSize: uint32(len(peer.Info)),
			Messages: map[string]uint8{
				"ut_pex":      protocol.ExtPex,
				"ut_metadata": protocol.ExtMetadata,
				"lt_donthave": protocol.ExtDontHave,
				"upload_only": protocol.ExtUploadOnly,
			},
			Encrypt: crypto.DefaultOptions(
				config.PreferEncryption,
				config.ForceEncryption,
			).PreferCryptoHandshake,
		})
		if err != nil {
			return err
		}
	}

	if peer.myBitmap.Empty() {
		if peer.canFast {
			err := write(peer, protocol.HaveNone{})
			if err != nil {
				return err
			}
		}
	} else {
		if peer.canFast && amSeed(peer) {
			err := write(peer, protocol.HaveAll{})
			if err != nil {
				return err
			}
		} else {
			count := peer.myBitmap.Count()
			num := numPieces(peer)
			if count < num/72 {
				if peer.canFast {
					err := write(peer, protocol.HaveNone{})
					if err != nil {
						return err
					}
				}
				var err error
				peer.myBitmap.Range(func(i int) bool {
					err = write(peer,
						protocol.Have{uint32(i)})
					return err == nil
				})
				if err != nil {
					return err
				}
			} else {
				bitmap := peer.myBitmap.Copy()
				bitmap.Extend(num)
				err := write(peer, protocol.Bitfield{bitmap})
				if err != nil {
					return err
				}
			}
		}
	}

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	defer peer.stopUpload()

	defer func() {
		close(peer.Done)

		peer.requests.Clear(true, func(index uint32) {
			drop(peer, index)
		})
		writeEvent(peer, TorPeerBitmap{peer, peer.bitmap.Copy(), false})
		writeEvent(peer, TorPeerGoaway{peer})
		for len(peer.events) > 0 {
			select {
			case peer.torEvent <- peer.events[0]:
				peer.events = peer.events[1:]
				if len(peer.events) == 0 {
					peer.events = nil
				}
			case <-peer.torDone:
				return
			}
		}
	}()

	for {

		var torEvent chan<- TorEvent
		var event TorEvent
		if len(peer.events) != 0 {
			torEvent = peer.torEvent
			event = peer.events[0]
		}

		var upload <-chan time.Time
		if peer.uploadTicker != nil {
			upload = peer.uploadTicker.C
		}
		select {
		case <-peer.writerDone:
			return nil
		case <-peer.torDone:
			return nil
		case c, ok := <-peer.Event:
			if !ok {
				return nil
			}
			err := handleEvent(peer, c)
			if err != nil {
				peer.Log.Printf("handleEvent: %v", err)
				readTimeout(0)
				return nil
			}
		case m, ok := <-reader:
			if !ok {
				return nil
			}
			peer.time = time.Now()
			err := handleMessage(peer, m)
			if err != nil {
				if err != io.EOF {
					peer.Log.Printf("handleMessage: %v", err)
				}
				readTimeout(0)
				return nil
			}
		case torEvent <- event:
			peer.events = peer.events[1:]
			if len(peer.events) == 0 {
				peer.events = nil
			}
		case <-upload:
			err := scheduleUpload(peer, true)
			if err != nil {
				return err
			}
		case <-ticker.C:
			expired := expireRequests(peer)
			if expired {
				maybeRequest(peer)
			}

			if time.Since(peer.pexState.sentTime) >= time.Minute {
				sendPex(peer)
				peer.pexState.sentTime = time.Now()
			}

			if time.Since(peer.pexTorTime) >= 20*time.Minute {
				for _, p := range peer.pex {
					writeEvent(peer, TorAddKnown{peer,
						p.IP, p.Port, nil, "", known.PEX,
					})
				}
				peer.pexTorTime = time.Now()
			}

			if time.Since(peer.writeTime) > 110*time.Second {
				write(peer, protocol.KeepAlive{})
			}

			if time.Since(peer.time) > 5*time.Minute {
				peer.Log.Printf("Timing out peer")
				readTimeout(0)
				return nil
			}

		}
	}
}

// expireRequests is called periodically to prune any requests that we
// have cancelled or that have been in flight for too long.
func expireRequests(peer *Peer) bool {
	if peer.requests.Requested() == 0 {
		return false
	}

	// Drop any requests that have been in the queue for too long.
	// This works around peers with leaky queues.
	t0 := time.Now().Add(-30 * time.Second)

	// Drop cancelled requests.
	to := rto(peer)
	if to > 5*time.Second {
		to = 5 * time.Second
	}
	// If the peer implements Fast, it should in principle explicitly
	// reject any dropped requests.  But some implementations are buggy.
	if peer.canFast {
		to += 2 * time.Second
	}
	t1 := time.Now().Add(-to)

	dropped := peer.requests.Expire(
		t0, t1,
		func(index uint32) {
			drop(peer, index)
		},
		func(index uint32) {
			docancel(peer, index)
		},
	)
	return dropped
}

func handleEvent(peer *Peer, c PeerEvent) error {
	switch c := c.(type) {
	case PeerMetadataComplete:
		if peer.Info != nil {
			return errors.New("duplicate metadata")
		}
		peer.Info = c.Info
		if peer.isSeed {
			if peer.bitmap != nil {
				return errors.New("inconsistent bitmap " +
					"with incomplete metadata")
			}
			p := numPieces(peer)
			peer.bitmap.SetMultiple(p)
			writeEvent(peer,
				TorPeerBitmap{peer, peer.bitmap.Copy(), true})
		} else {
			if peer.bitmap.Len() > numPieces(peer) {
				return errors.New("overlong bitfield")
			}
		}
		maybeInterested(peer)
	case PeerRequest:
		if peer.Info == nil {
			return ErrMetadataIncomplete
		}
		for _, chunk := range c.Chunks {
			done := false
			i, _ := fromChunk(peer, chunk)
			if peer.bitmap.Get(int(i)) {
				done = peer.requests.Enqueue(chunk)
			}
			if !done {
				drop(peer, chunk)
			}
		}
		maybeRequest(peer)
	case PeerHave:
		if c.Have {
			peer.myBitmap.Set(int(c.Index))
			err := write(peer, protocol.Have{c.Index})
			if err != nil {
				return err
			}
		} else {
			peer.myBitmap.Reset(int(c.Index))
			if peer.dontHaveExt > 0 {
				err := write(peer, protocol.ExtendedDontHave{
					uint8(peer.dontHaveExt), c.Index})
				if err != nil {
					return err
				}
			}
		}
		maybeInterested(peer)
	case PeerCancel:
		if peer.Info == nil {
			return ErrMetadataIncomplete
		}
		cancel(peer, c.Chunk)
	case PeerCancelPiece:
		if peer.Info == nil {
			return ErrMetadataIncomplete
		}
		cpp := uint32(peer.Pieces.PieceSize()) / config.ChunkSize
		for i := 0; i < int(cpp); i++ {
			cancel(peer, c.Index*cpp+uint32(i))
		}
	case PeerInterested:
		peer.shouldInterested = c.Interested
		maybeInterested(peer)
	case PeerGetMetadata:
		if peer.metadataExt == 0 || isCongested(peer) {
			return nil
		}
		write(peer, protocol.ExtendedMetadata{
			uint8(peer.metadataExt), 0, c.Index, 0, nil})
	case PeerPex:
		if peer.pexExt == 0 {
			return nil
		}

		if c.Add {
			for _, p := range c.Peers {
				peer.pexState.add(p)
			}
		} else {
			for _, d := range c.Peers {
				peer.pexState.del(d)
			}
		}
	case PeerGetStatus:
		var down float64
		if time.Since(peer.download.Time()) < 3*time.Minute {
			down = peer.download.Estimate()
		}
		c.Ch <- PeerStatus{
			peer.unchoked != 0,
			peer.interested != 0,
			peer.amUnchoking != 0,
			peer.amInterested,
			isSeed(peer),
			peer.uploadOnly,
			peer.requests.Requested() + peer.requests.Queue(),
			down, peer.avgDownload.Estimate(), peer.unchokeTime,
		}
		close(c.Ch)
	case PeerGetStats:
		var up, down float64
		if time.Since(peer.download.Time()) < 3*time.Minute {
			down = peer.download.Estimate()
		}
		if time.Since(peer.upload.Time()) < 3*time.Minute {
			up = peer.upload.Estimate()
		}
		c.Ch <- PeerStats{
			peer.unchoked != 0,
			peer.interested != 0,
			peer.amUnchoking != 0,
			peer.amInterested,
			isSeed(peer), peer.uploadOnly,
			hasProxy(peer),
			down, peer.avgDownload.Estimate(), up,
			peer.rtt, peer.rttvar, peer.unchokeTime,
			peer.requests.Requested(),
			peer.requests.Requested() + peer.requests.Queue(),
			len(peer.requested),
			peer.bitmap.Count(),
			len(peer.pex),
		}
		close(c.Ch)
	case PeerGetPex:
		if peer.Port > 0 {
			var flags byte
			if peer.prefersEncryption {
				flags |= pex.Encrypt
			}
			if peer.uploadOnly || isSeed(peer) {
				flags |= pex.UploadOnly
			}
			if !peer.Incoming {
				flags |= pex.Outgoing
			}
			c.Ch <- pex.Peer{
				IP:    peer.IP,
				Port:  int(peer.Port),
				Flags: flags,
			}
		}
		close(c.Ch)
	case PeerGetFast:
		if peer.fast == nil {
			c.Ch <- nil
		} else {
			f := make([]uint32, len(peer.fast))
			copy(f, peer.fast)
			c.Ch <- f
		}
		close(c.Ch)
	case PeerGetBitmap:
		c.Ch <- peer.bitmap.Copy()
		close(c.Ch)
	case PeerGetHave:
		c.Ch <- peer.bitmap.Get(int(c.Index))
		close(c.Ch)
	case PeerUnchoke:
		err := unchoke(peer, c.Unchoke)
		if err != nil {
			return err
		}
		if peer.amUnchoking != 0 {
			err := scheduleUpload(peer, false)
			if err != nil {
				return err
			}
		} else {
			peer.stopUpload()
		}
	case PeerDone:
		return io.EOF
	default:
		peer.Log.Printf("Unknown command %#v", c)
		panic("Uknown command")
	}
	return nil
}

func write(peer *Peer, m protocol.Message) error {
	select {
	case peer.writer <- m:
		peer.writeTime = time.Now()
		return nil
	case <-peer.writerDone:
		return io.EOF
	default:
		timer := time.NewTimer(200 * time.Millisecond)
		select {
		case peer.writer <- m:
			peer.writeTime = time.Now()
			timer.Stop()
			return nil
		case <-peer.writerDone:
			return io.EOF
		case <-timer.C:
			return ErrCongested
		}
	}
}

func isCongested(peer *Peer) bool {
	return len(peer.writer) > cap(peer.writer)/2
}

func writeEvent(peer *Peer, m TorEvent) {
	if len(peer.events) == 0 {
		select {
		case peer.torEvent <- m:
			return
		default:
		}
	}
	peer.events = append(peer.events, m)
}

func drop(peer *Peer, chunk uint32) {
	i, b := fromChunk(peer, chunk)
	writeEvent(peer, TorDrop{i, b, config.ChunkSize})
}

func uploadInterval(peer *Peer) time.Duration {
	var interval time.Duration
	rate := peer.upload.Estimate()
	uploadRate := config.UploadRate()
	if rate > uploadRate {
		rate = uploadRate
	}
	if rate < 16*1024 {
		interval = 250 * time.Millisecond
	} else {
		interval = time.Duration(float64(time.Second) / rate * 4096)
	}
	return interval
}

func (peer *Peer) stopUpload() {
	if peer.uploadTicker == nil {
		return
	}
	peer.uploadTicker.Stop()
	peer.uploadTicker = nil
	peer.uploadInterval = 0
	peer.upload.Stop()
}

func (peer *Peer) startStopUpload() {
	if len(peer.requested) == 0 {
		peer.stopUpload()
		return
	}
	interval := uploadInterval(peer)
	if peer.uploadTicker != nil &&
		interval > peer.uploadInterval*3/4 &&
		interval < peer.uploadInterval*3/2 {
		return
	}
	if peer.uploadTicker == nil {
		peer.uploadTicker = time.NewTicker(interval)
	} else {
		peer.uploadTicker.Reset(interval)
	}
	peer.uploadInterval = interval
	peer.upload.Start()
}

func reject(peer *Peer, index uint32, begin uint32, length uint32) error {
	if peer.canFast {
		return write(peer, protocol.RejectRequest{
			index, begin, length})
	}
	return nil
}

func docancel(peer *Peer, chunk uint32) error {
	i, b := fromChunk(peer, chunk)
	cs := chunkSize(peer, chunk)
	return write(peer, protocol.Cancel{i, b, cs})
}

func cancel(peer *Peer, chunk uint32) error {
	var err error
	c, cancel := peer.requests.Cancel(chunk)
	if c {
		if cancel {
			err = docancel(peer, chunk)
		}
		// don't remove the request, the data might already be in
		// flight.  If the peer supports the Fast extension
		// correctly, it will send us RejectRequest.  If it
		// doesn't support Fast or is buggy, expireRequests will
		// take care of the request.
		return err
	}

	q, r, _ := peer.requests.Del(chunk)
	if q || r {
		if r {
			err = docancel(peer, chunk)
		}
		drop(peer, chunk)
	}
	// but don't request yet -- more cancels might follow
	if peer.requests.Requested() == 0 {
		peer.download.Stop()
	}
	return err

}

func (peer *Peer) active() {
	if time.Since(peer.lastActive) > 5*time.Second {
		if peer.Port != 0 {
			writeEvent(peer, TorAddKnown{peer,
				peer.IP, int(peer.Port), nil, "", known.Active,
			})
		}
		peer.lastActive = time.Now()
	}
}

func handleMessage(peer *Peer, m protocol.Message) error {
	switch m := m.(type) {
	case protocol.Error:
		return m.Error
	case protocol.KeepAlive:
	case protocol.Choke:
		atomic.StoreUint32(&peer.unchoked, 0)
		peer.requests.Clear(!peer.canFast, func(index uint32) {
			drop(peer, index)
		})
		peer.download.Stop()
		writeEvent(peer, TorPeerUnchoke{peer, false})
	case protocol.Unchoke:
		atomic.StoreUint32(&peer.unchoked, 1)
		writeEvent(peer, TorPeerUnchoke{peer, true})
	case protocol.Interested:
		atomic.StoreUint32(&peer.interested, 1)
		writeEvent(peer, TorPeerInterested{peer, true})
	case protocol.NotInterested:
		atomic.StoreUint32(&peer.interested, 0)
		unchoke(peer, false)
		writeEvent(peer, TorPeerInterested{peer, false})
	case protocol.Have:
		if peer.Info != nil && m.Index >= uint32(numPieces(peer)) {
			return ErrRange
		}
		if !peer.bitmap.Get(int(m.Index)) {
			peer.bitmap.Set(int(m.Index))
			writeEvent(peer, TorPeerHave{peer, m.Index, true})
			maybeInterested(peer)
		} else {
			peer.Log.Printf("Redundant Have %v", m.Index)
		}
	case protocol.Bitfield:
		b := bitmap.Bitmap(m.Bitfield).Copy()
		if peer.Info != nil && b.Len() > numPieces(peer) {
			return errors.New("overlong bitfield")
		}
		if peer.bitmap != nil {
			peer.Log.Printf("Changing bitmap")
			writeEvent(peer,
				TorPeerBitmap{peer, peer.bitmap.Copy(), false})
			peer.bitmap = nil
		}
		peer.bitmap = b
		writeEvent(peer, TorPeerBitmap{peer, peer.bitmap.Copy(), true})
		maybeInterested(peer)
	case protocol.Request:
		if peer.Info == nil || peer.amUnchoking == 0 {
			return reject(peer, m.Index, m.Begin, m.Length)
		}
		if len(peer.requested) >= reqQ {
			// head drop
			r := peer.requested[0]
			err := reject(peer, r.Index, r.Begin, r.Length)
			if err == nil {
				peer.requested = peer.requested[1:]
			}
		}
		peer.requested = append(peer.requested,
			Requested{m.Index, m.Begin, m.Length})
		err := scheduleUpload(peer, false)
		if err != nil {
			return err
		}
	case protocol.Piece:
		if peer.Info == nil {
			return ErrMetadataIncomplete
		}
		if m.Index >= uint32(numPieces(peer)) {
			return ErrRange
		}
		c := toChunk(peer, m.Index, m.Begin)
		q, r, tm := peer.requests.Del(c)
		if r || q {
			length := len(m.Data)
			DownloadEstimator.Accumulate(length)
			n, complete, err := peer.Pieces.AddData(
				m.Index, m.Begin, m.Data, peer.Counter)
			protocol.PutBuffer(m.Data)
			m.Data = nil
			if n == uint32(length) {
				peer.download.Accumulate(length)
				peer.avgDownload.Accumulate(length)
				writeEvent(peer, TorData{peer,
					m.Index, m.Begin,
					uint32(length), complete})
				// TorData implies active
			} else {
				if err != nil {
					peer.Log.Printf("addData %v %v %v: %v",
						m.Index, m.Begin, len(m.Data),
						err)
				}
				drop(peer, c)
				peer.active()
			}
			if r {
				delay := time.Since(tm)
				diff := delay - peer.rtt
				if diff < 0 {
					diff = -diff
				}
				peer.rtt = (7*peer.rtt + delay) / 8
				peer.rttvar = (3*peer.rttvar + diff) / 4
			}
		}
		maybeRequest(peer)
	case protocol.Cancel:
		if peer.Info == nil {
			return ErrMetadataIncomplete
		}
		found := false
		for i, r := range peer.requested {
			if r.Index == m.Index &&
				r.Begin == m.Begin &&
				r.Length == m.Length {
				peer.requested =
					append(peer.requested[0:i],
						peer.requested[i+1:]...)
				if len(peer.requested) == 0 {
					peer.requested = nil
				}
				found = true
				break
			}
		}
		if found {
			err := reject(peer, m.Index, m.Begin, m.Length)
			if err != nil {
				return err
			}
		}
		peer.startStopUpload()
	case protocol.Port:
		if peer.IP != nil {
			dht.Ping(peer.IP, m.Port)
		}
	case protocol.SuggestPiece:
		if !peer.canFast {
			return ErrCannotFast
		}
	case protocol.RejectRequest:
		if !peer.canFast {
			return ErrCannotFast
		}
		if peer.Info == nil {
			return ErrMetadataIncomplete
		}
		c := toChunk(peer, m.Index, m.Begin)
		r := peer.requests.DelRequested(c)
		if r {
			drop(peer, c)
		}
		maybeRequest(peer)
	case protocol.AllowedFast:
		if !peer.canFast {
			return ErrCannotFast
		}
		if !isFast(peer, m.Index) {
			peer.fast = append(peer.fast, m.Index)
			atomic.StoreUint32(&peer.hasFast, 1)
		}
	case protocol.HaveAll:
		if !peer.canFast {
			return ErrCannotFast
		}
		if peer.bitmap != nil {
			peer.Log.Printf("Changing bitmap")
			writeEvent(peer,
				TorPeerBitmap{peer, peer.bitmap.Copy(), false})
			peer.bitmap = nil
		}
		peer.isSeed = true
		if peer.Info != nil {
			p := numPieces(peer)
			peer.bitmap.SetMultiple(p)
			writeEvent(peer, TorPeerBitmap{peer,
				peer.bitmap.Copy(), true})
		}
		maybeInterested(peer)
	case protocol.HaveNone:
		if !peer.canFast {
			return ErrCannotFast
		}
		if peer.bitmap != nil {
			peer.Log.Printf("Changing bitmap")
			writeEvent(peer,
				TorPeerBitmap{peer, peer.bitmap.Copy(), false})
		}
		peer.bitmap = nil
		peer.isSeed = false
	case protocol.Extended0:
		if peer.gotExtended {
			return errors.New("duplicate Extended0")
		}
		peer.gotExtended = true
		if m.Port != 0 {
			if peer.Port != 0 && uint32(m.Port) != peer.Port {
				peer.Log.Printf("Inconsistent port (%v, %v)",
					m.Port, peer.Port)
			} else {
				atomic.StoreUint32(&peer.Port, uint32(m.Port))
			}
			k := func(ip net.IP, kind known.Kind) {
				writeEvent(peer, TorAddKnown{peer,
					ip, int(peer.Port), peer.Id,
					m.Version, kind,
				})
			}
			k(peer.IP, known.ActiveNoReset)
			k(peer.IP, known.Seen)
			if m.IPv4 != nil {
				k(m.IPv4, known.Heard)
			}
			if m.IPv6 != nil {
				k(m.IPv6, known.Heard)
			}
		}
		if m.ReqQ > 0 {
			peer.reqQ = int(m.ReqQ)
		}
		if m.Messages != nil {
			atomic.StoreUint32(&peer.pexExt,
				uint32(m.Messages["ut_pex"]))
			atomic.StoreUint32(&peer.metadataExt,
				uint32(m.Messages["ut_metadata"]))
			atomic.StoreUint32(&peer.dontHaveExt,
				uint32(m.Messages["lt_donthave"]))
			atomic.StoreUint32(&peer.uploadOnlyExt,
				uint32(m.Messages["upload_only"]))
		}
		peer.prefersEncryption = m.Encrypt
		peer.uploadOnly = m.UploadOnly
		writeEvent(peer, TorPeerExtended{peer, m.MetadataSize})
	case protocol.ExtendedPex:
		for _, p := range m.Added {
			i := pex.Find(p, peer.pex)
			if i >= 0 {
				peer.pex[i] = p
				peer.Log.Printf("Peer added duplicate PEX peer")
			} else {
				peer.pex = append(peer.pex, p)
				writeEvent(peer, TorAddKnown{peer,
					p.IP, p.Port, nil, "", known.PEX,
				})
			}
		}
		for _, p := range m.Dropped {
			i := pex.Find(p, peer.pex)
			if i >= 0 {
				peer.pex =
					append(peer.pex[:i], peer.pex[i+1:]...)
				if len(peer.pex) == 0 {
					peer.pex = nil
				}
			} else {
				peer.Log.Printf("Peer dropped unknown PEX peer")
			}
		}
	case protocol.ExtendedMetadata:
		if m.Type == 0 {
			if peer.metadataExt == 0 || isCongested(peer) {
				return nil
			}
			offset := int(m.Piece) * 16 * 1024
			l := 16 * 1024
			if offset+l > len(peer.Info) {
				l = len(peer.Info) - offset
			}
			var err error
			if l > 0 {
				err = write(peer, protocol.ExtendedMetadata{
					uint8(peer.metadataExt), 1,
					m.Piece, uint32(len(peer.Info)),
					peer.Info[offset : offset+l]})
				if err == nil {
					peer.upload.Accumulate(l)
					UploadEstimator.Accumulate(l)
				} else if err != ErrCongested {
					return err
				}
			}
			if l <= 0 || err != nil {
				write(peer, protocol.ExtendedMetadata{
					uint8(peer.metadataExt), 2,
					m.Piece, 0, nil})
			}
		} else if m.Type == 1 {
			peer.download.Accumulate(len(m.Data))
			peer.avgDownload.Accumulate(len(m.Data))
			DownloadEstimator.Accumulate(len(m.Data))
			writeEvent(peer,
				TorMetaData{peer, m.TotalSize, m.Piece, m.Data})
			// TorMetaData implies active
		} else if m.Type == 2 {
			peer.Log.Println("Metadata rejected")
		} else {
			return errors.New("unexpected Metadata type")
		}
	case protocol.ExtendedDontHave:
		if peer.isSeed && peer.Info == nil {
			return errors.New("DontHave from seed " +
				"with incomplete metadata")
		}
		peer.isSeed = false
		if peer.Info != nil && m.Index >= uint32(numPieces(peer)) {
			return ErrRange
		}
		if peer.bitmap.Get(int(m.Index)) {
			peer.bitmap.Reset(int(m.Index))
			peer.isSeed = false
			writeEvent(peer, TorPeerHave{peer, m.Index, false})
		} else {
			peer.Log.Printf("Redundant DontHave %v", m.Index)
		}
	case protocol.ExtendedUploadOnly:
		peer.uploadOnly = m.Value
	case protocol.ExtendedUnknown:
		return errors.New("unknown extended message")
	case protocol.Unknown:
		return errors.New("unknown message")
	default:
		peer.Log.Printf("%#v", m)
		panic("Unknown message")
	}
	return nil
}

func isFast(peer *Peer, index uint32) bool {
	for _, f := range peer.fast {
		if f == index {
			return true
		}
	}
	return false
}

func rto(peer *Peer) time.Duration {
	return peer.rtt + 4*peer.rttvar
}

func maybeRequest(peer *Peer) {
	if peer.unchoked == 0 && len(peer.fast) == 0 {
		return
	}

	rate := peer.download.Estimate()
	maxdelay := float64(rto(peer)) / float64(time.Second)
	if maxdelay > 10 {
		maxdelay = 10
	}

	for !isCongested(peer) && peer.requests.Queue() > 0 {
		nr := peer.requests.Requested()
		bytes := float64((nr + 1) * int(config.ChunkSize))
		if nr >= 2 && (nr >= peer.reqQ || bytes/rate > maxdelay) {
			break
		}
		q, index := peer.requests.Dequeue()
		i, b := fromChunk(peer, index)

		if (peer.unchoked == 0 && !isFast(peer, i)) ||
			!peer.bitmap.Get(int(i)) {
			drop(peer, index)
			continue
		}

		err := write(peer, protocol.Request{
			i, b, chunkSize(peer, index)})
		if err != nil {
			drop(peer, index)
			break
		}
		peer.requests.EnqueueRequest(q)
	}

	if peer.requests.Requested() > 0 {
		peer.download.Start()
	} else {
		peer.download.Stop()
	}
}

func scheduleUpload(peer *Peer, immediate bool) error {
	if immediate && peer.amUnchoking != 0 && len(peer.requested) > 0 {
		if isCongested(peer) {
			goto done
		}
		r := peer.requested[0]
		upload := config.UploadRate()
		fair := upload / float64(NumUnchoking())

		if !peer.upload.Allow(int(r.Length), fair) &&
			!UploadEstimator.Allow(int(r.Length), upload) {
			goto done
		}

		peer.requested = peer.requested[1:]
		if len(peer.requested) == 0 {
			peer.requested = nil
		}

		offset := int64(r.Index)*int64(peer.Pieces.PieceSize()) +
			int64(r.Begin)
		data := protocol.GetBuffer(int(r.Length))
		n, _ := peer.Pieces.ReadAt(data, offset)
		if n != int(r.Length) {
			protocol.PutBuffer(data)
			data = nil
			err := reject(peer, r.Index, r.Begin, r.Length)
			if err != nil {
				return err
			}
			goto done
		}
		m := protocol.Piece{r.Index, r.Begin, data}
		err := write(peer, m)
		if err == nil {
			peer.upload.Accumulate(int(r.Length))
			UploadEstimator.Accumulate(int(r.Length))
			peer.active()
		} else if err == ErrCongested {
			m.Data = nil
			protocol.PutBuffer(data)
			peer.requested =
				append([]Requested{r}, peer.requested...)
		} else {
			return err
		}
	}

done:
	peer.startStopUpload()
	return nil
}

func unchoke(peer *Peer, unchoke bool) error {
	if unchoke && peer.interested == 0 {
		peer.Log.Printf("Attempted to unchoke uninterested peer")
		unchoke = false
	}
	if unchoke == (peer.amUnchoking != 0) {
		return nil
	}
	if unchoke {
		err := write(peer, protocol.Unchoke{})
		if err == nil {
			atomic.StoreUint32(&peer.amUnchoking, 1)
			atomic.AddInt32(&numUnchoking, 1)
			peer.unchokeTime = time.Now()
		}
		return nil // not an error
	} else {
		err := write(peer, protocol.Choke{})
		if err == nil {
			atomic.StoreUint32(&peer.amUnchoking, 0)
			atomic.AddInt32(&numUnchoking, -1)
			for _, r := range peer.requested {
				err := reject(peer, r.Index, r.Begin, r.Length)
				if err != nil {
					return err
				}
			}
			peer.requested = nil
			peer.unchokeTime = time.Now()
		}
		return err
	}
}

func maybeInterested(peer *Peer) error {
	interested := false
	if peer.shouldInterested && peer.Info != nil && peer.bitmap != nil {
		peer.bitmap.Range(func(i int) bool {
			if !peer.myBitmap.Get(i) {
				interested = true
				return false
			}
			return true
		})
	}
	if interested == peer.amInterested {
		return nil
	}

	var err error
	if interested {
		err = write(peer, protocol.Interested{})
	} else {
		err = write(peer, protocol.NotInterested{})
	}

	if err == nil {
		peer.amInterested = interested
	}
	return err
}

func isSeed(peer *Peer) bool {
	if peer.Info == nil {
		return false
	}
	if peer.isSeed {
		return true
	}
	seed := peer.bitmap.All(peer.Pieces.Num())
	if seed {
		peer.isSeed = true
	}
	return seed
}

func amSeed(peer *Peer) bool {
	if peer.Info == nil {
		return false
	}
	return peer.myBitmap.All(peer.Pieces.Num())
}

func (peer *Peer) GetStatus() *PeerStatus {
	ch := make(chan PeerStatus)
	select {
	case peer.Event <- PeerGetStatus{ch}:
		select {
		case v, ok := <-ch:
			if ok {
				return &v
			}
			return nil
		case <-peer.Done:
			return nil
		}
	case <-peer.Done:
		return nil
	}
}

func (peer *Peer) GetPex() *pex.Peer {
	ch := make(chan pex.Peer)
	select {
	case peer.Event <- PeerGetPex{ch}:
		select {
		case v, ok := <-ch:
			if ok {
				return &v
			}
		case <-peer.Done:
		}
	case <-peer.Done:
	}
	return nil
}

func (peer *Peer) GetStats() *PeerStats {
	ch := make(chan PeerStats)
	select {
	case peer.Event <- PeerGetStats{ch}:
		select {
		case v, ok := <-ch:
			if ok {
				return &v
			}
		case <-peer.Done:
		}
	case <-peer.Done:
	}
	return nil
}

func (peer *Peer) GetFast() []uint32 {
	if atomic.LoadUint32(&peer.hasFast) == 0 {
		return nil
	}
	ch := make(chan []uint32)
	select {
	case peer.Event <- PeerGetFast{ch}:
		select {
		case v, ok := <-ch:
			if ok {
				return v
			}
		case <-peer.Done:
		}
	default:
	}
	return nil
}

func (peer *Peer) GetBitmap() bitmap.Bitmap {
	ch := make(chan bitmap.Bitmap)
	select {
	case peer.Event <- PeerGetBitmap{ch}:
		select {
		case v, ok := <-ch:
			if ok {
				return v
			}
		case <-peer.Done:
		}
	case <-peer.Done:
	}
	return nil
}

func (peer *Peer) GetHave(index uint32) bool {
	ch := make(chan bool)
	select {
	case peer.Event <- PeerGetHave{index, ch}:
		select {
		case v, ok := <-ch:
			if ok {
				return v
			}
		case <-peer.Done:
		}
	case <-peer.Done:
	}
	return false
}

func (peer *Peer) Unchoked() bool {
	return atomic.LoadUint32(&peer.unchoked) != 0
}

func (peer *Peer) Interested() bool {
	return atomic.LoadUint32(&peer.interested) != 0
}

func (peer *Peer) AmUnchoking() bool {
	return atomic.LoadUint32(&peer.amUnchoking) != 0
}

func (peer *Peer) CanPex() bool {
	return atomic.LoadUint32(&peer.pexExt) != 0
}

func (peer *Peer) CanMetadata() bool {
	return atomic.LoadUint32(&peer.metadataExt) != 0
}

func (state *pexState) add(p pex.Peer) {
	i := pex.Find(p, state.pendingDel)
	if i >= 0 {
		state.pendingDel =
			append(state.pendingDel[:i], state.pendingDel[i+1:]...)
		if len(state.pendingDel) == 0 {
			state.pendingDel = nil
		}
		return
	}

	i = pex.Find(p, state.sent)
	if i >= 0 {
		return
	}

	i = pex.Find(p, state.pending)
	if i >= 0 {
		return
	}

	state.pending = append(state.pending, p)
}

func (state *pexState) del(p pex.Peer) {
	i := pex.Find(p, state.pending)
	if i >= 0 {
		state.pending =
			append(state.pending[:i], state.pending[i+1:]...)
		if len(state.pending) == 0 {
			state.pending = nil
		}
		return
	}

	i = pex.Find(p, state.sent)
	if i < 0 {
		return
	}
	state.sent = append(state.sent[:i], state.sent[i+1:]...)
	if len(state.sent) == 0 {
		state.sent = nil
	}

	i = pex.Find(p, state.pendingDel)
	if i >= 0 {
		return
	}

	state.pendingDel = append(state.pendingDel, p)
}

func computePex(state *pexState) ([]pex.Peer, []pex.Peer) {
	if len(state.pending) == 0 && len(state.pendingDel) == 0 {
		return nil, nil
	}

	var tosend, todel []pex.Peer
	if len(state.pending) > 50 {
		tosend = state.pending[:50]
		state.pending = state.pending[50:]
	} else {
		tosend = state.pending
		state.pending = nil
	}
	if len(state.pendingDel) > 50 {
		todel = state.pendingDel[:50]
		state.pendingDel = state.pendingDel[50:]
	} else {
		todel = state.pendingDel
		state.pendingDel = nil
	}

	state.sent = append(state.sent, tosend...)
	return tosend, todel
}

func sendPex(peer *Peer) {
	if peer.pexExt == 0 || isCongested(peer) {
		return
	}

	tosend, todel := computePex(&peer.pexState)
	if len(tosend) > 0 || len(todel) > 0 {
		err := write(peer,
			protocol.ExtendedPex{uint8(peer.pexExt), tosend, todel})
		if err != nil {
			peer.pexState.pending =
				append(tosend, peer.pexState.pending...)
			peer.pexState.pendingDel =
				append(todel, peer.pexState.pendingDel...)
		}
	}
}

func hasProxy(peer *Peer) bool {
	return peer.proxy != ""
}

func getIPv6() net.IP {
	conn, err := net.Dial("udp6", "[2400:cb00:2048:1::6814:155]:443")
	if err != nil {
		return nil
	}
	defer conn.Close()
	addr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok {
		return nil
	}
	if !addr.IP.IsGlobalUnicast() {
		return nil
	}
	return addr.IP
}

func (p *Peer) Encrypted() bool {
	_, ok := p.conn.(*crypto.Conn)
	return ok
}

func (p *Peer) GetPort() int {
	return int(atomic.LoadUint32(&p.Port))
}
