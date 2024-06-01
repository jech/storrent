// Package tor implements behaviour of torrents in storrent.
package tor

import (
	"cmp"
	"context"
	crand "crypto/rand"
	"errors"
	"io"
	"log"
	"math"
	"math/rand"
	"net"
	"os"
	"slices"
	"sync/atomic"
	"time"

	"github.com/jech/storrent/alloc"
	"github.com/jech/storrent/bitmap"
	"github.com/jech/storrent/config"
	"github.com/jech/storrent/crypto"
	"github.com/jech/storrent/dht"
	"github.com/jech/storrent/hash"
	"github.com/jech/storrent/known"
	"github.com/jech/storrent/path"
	"github.com/jech/storrent/peer"
	"github.com/jech/storrent/pex"
	"github.com/jech/storrent/protocol"
	"github.com/jech/storrent/tor/piece"
	"github.com/jech/storrent/tracker"
	"github.com/jech/storrent/webseed"
)

var ErrTorrentDead = errors.New("torrent is dead")
var ErrMetadataIncomplete = errors.New("metadata incomplete")

// Torrent represents an active torrent.
type Torrent struct {
	proxy           string
	Hash            hash.Hash
	MyId            hash.Hash
	Info            []byte // raw info dictionary
	CreationDate    int64
	trackers        [][]tracker.Tracker
	webseeds        []webseed.Webseed
	infoComplete    uint32         // 1 when metadata is complete
	infoBitmap      bitmap.Bitmap  // bitmap of available metadata
	infoRequested   []uint8        // chunks of metadata in flight
	infoSizeVotes   map[uint32]int // used for determining metadata size
	PieceHashes     []hash.Hash
	Name            string
	Files           []Torfile // nil if single-file torrent
	Pieces          piece.Pieces
	available       []uint16
	inFlight        []uint8
	amInterested    bool
	requested       Requested
	Event           chan peer.TorEvent
	Done            chan struct{}
	Deleted         chan struct{}
	peers           []*peer.Peer
	known           known.Peers
	Log             *log.Logger
	rand            *rand.Rand
	requestInterval time.Duration // interval of requestTicker
	requestSeconds  float64       // same in seconds
	requestTicker   *time.Ticker  // governs sending chunk requests
	announceTime    time.Time     // DHT
	useDht          bool
	dhtPassive      bool
	useTrackers     bool
	useWebseeds     bool
}

// Torfile represents a file within a torrent.
type Torfile struct {
	Path    path.Path
	Offset  int64 // offset within the torrent
	Length  int64 // length of the file
	Padding bool  // true if a padding file
}

func New(proxy string, hsh hash.Hash, dn string,
	info []byte, cdate int64, announce [][]tracker.Tracker,
	webseeds []webseed.Webseed) (*Torrent, error) {
	myid := hash.Hash(make([]byte, 20))
	_, err := crand.Read(myid)
	if err != nil {
		return nil, err
	}
	return &Torrent{
		proxy:        proxy,
		Hash:         hsh,
		MyId:         myid,
		Info:         info,
		CreationDate: cdate,
		trackers:     announce,
		webseeds:     webseeds,
		Name:         dn,
		requested:    Requested{pieces: make(map[uint32]*RequestedPiece)},
		useDht:       config.DefaultUseDht,
		dhtPassive:   config.DefaultDhtPassive,
		useTrackers:  config.DefaultUseTrackers,
		useWebseeds:  config.DefaultUseWebseeds,
		Log:          log.New(os.Stderr, "     ", log.LstdFlags),
		known:        make(known.Peers),
	}, nil
}

// Announce performs a DHT announce.
func Announce(h hash.Hash, ipv6 bool) error {
	t := Get(h)
	if t == nil {
		return os.ErrNotExist
	}
	select {
	case t.Event <- peer.TorAnnounce{ipv6}:
		return nil
	case <-t.Done:
		return ErrTorrentDead
	}
}

func (t *Torrent) announce(ipv6 bool) {
	if !t.useDht {
		return
	}
	var port uint16
	if !t.hasProxy() && !t.dhtPassive {
		port = uint16(config.ExternalPort(false, ipv6))
	}
	prot := "IPv4"
	if ipv6 {
		prot = "IPv6"
	}
	t.Log.Printf("Starting %v announce for %v\n", prot, t.Hash)
	dht.Announce(t.Hash, ipv6, port)
	t.announceTime = time.Now()
}

// AddTorrent starts a new torrent.
func AddTorrent(ctx context.Context, t *Torrent) (*Torrent, error) {
	t.Event = make(chan peer.TorEvent, 512)
	t.Done = make(chan struct{})
	t.Deleted = make(chan struct{})

	added := add(t)
	if !added {
		close(t.Done)
		close(t.Deleted)
		return nil, os.ErrExist
	}
	go func(ctx context.Context, t *Torrent) {
		defer func(t *Torrent) {
			del(t.Hash)
			close(t.Deleted)
		}(t)
		t.run(ctx)
	}(ctx, t)
	t.announce(true)
	t.announce(false)
	return t, nil
}

// run is the main loop of a torrent.
func (t *Torrent) run(ctx context.Context) {
	defer func() {
		close(t.Done)
		t.Pieces.Del()
	}()

	t.rand = rand.New(rand.NewSource(rand.Int63()))
	jiffy := func() time.Duration {
		return time.Duration(t.rand.Int63n(int64(time.Second)))
	}
	ticker := time.NewTicker(5*time.Second + jiffy())
	slowTicker := time.NewTicker(20*time.Second + jiffy())
	ctx, cancelCtx := context.WithCancel(ctx)
	defer func() {
		cancelCtx()
		slowTicker.Stop()
		t.setRequestInterval(0)
		ticker.Stop()
	}()

	for {
		var requestChan <-chan time.Time
		if t.requestTicker != nil {
			requestChan = t.requestTicker.C
		}

		select {
		case e := <-t.Event:
			err := handleEvent(ctx, t, e)
			if err != nil {
				if err != io.EOF {
					t.Log.Printf("handleEvent: %v", err)
				}
				return
			}
		case <-requestChan:
			periodicRequest(ctx, t)
		case <-ticker.C:
			maybeConnect(ctx, t)
			if t.infoComplete == 0 {
				requestMetadata(t, nil)
			}
		case <-slowTicker.C:
			maybeUnchoke(t, true)
			t.known.Expire()
			if len(t.requested.pieces) == 0 &&
				time.Since(t.requested.time) > time.Minute {
				notInterested(t)
			}
			if time.Since(t.announceTime) > 28*time.Minute {
				t.announce(true)
				t.announce(false)
			}
			if t.useTrackers {
				trackerAnnounce(ctx, t)
			}
		case <-ctx.Done():
			return
		}
	}
}

func handleEvent(ctx context.Context, t *Torrent, c peer.TorEvent) error {
	active := func(p *peer.Peer) {
		pp := p.GetPort()
		if pp > 0 {
			known.Find(t.known, p.IP, pp, nil, "", known.Active)
		}
	}
	switch c := c.(type) {
	case peer.TorAddPeer:
		c.Peer.Pieces = &t.Pieces
		var info []byte
		if t.infoComplete != 0 {
			info = t.Info
		}
		t.peers = append(t.peers, c.Peer)
		go peer.Run(c.Peer, t.Event, t.Done, info, t.Pieces.Bitmap(),
			c.Init)
		p := c.Peer.GetPex()
		if p != nil {
			writePeers(t, peer.PeerPex{[]pex.Peer{*p}, true},
				c.Peer)
		}
		if t.amInterested {
			writePeer(c.Peer, peer.PeerInterested{true})
		}
	case peer.TorPeerExtended:
		if t.infoComplete == 0 {
			if c.MetadataSize != 0 {
				err := metadataVote(t, c.MetadataSize)
				if err == nil {
					err = requestMetadata(t, c.Peer)
				}
				if err != nil {
					t.Log.Printf("metadata: %v", err)
					return nil
				}
			}
		}
		p := c.Peer.GetPex()
		if p != nil {
			writePeers(t, peer.PeerPex{[]pex.Peer{*p}, true},
				c.Peer)
		}
		if c.Peer.CanPex() {
			var pp []pex.Peer
			for _, q := range t.peers {
				if q == c.Peer {
					continue
				}
				p := q.GetPex()
				if p != nil {
					pp = append(pp, *p)
				}
			}
			if len(pp) > 0 {
				writePeer(c.Peer, peer.PeerPex{pp, true})
			}
		}
	case peer.TorAddKnown:
		kp := known.Find(t.known,
			c.IP, int(c.Port), c.Id, c.Version, c.Kind)
		if kp != nil && c.Peer != nil && c.Kind == known.Seen {
			if kp.ReallyBad() {
				writePeer(c.Peer, peer.PeerDone{})
			}
		}
	case peer.TorBadPeer:
		for _, p := range t.peers {
			if c.Peer == p.Counter {
				port := p.GetPort()
				if port > 0 {
					bad := known.Good
					if c.Bad {
						bad = known.Bad
					}
					kp := known.Find(t.known,
						p.IP, port, nil, "", bad)
					if kp != nil && kp.Bad() &&
						t.rand.Intn(5) == 0 {
						writePeer(p, peer.PeerDone{})
					}
				} else if c.Bad {
					// no listening port
					writePeer(p, peer.PeerDone{})
				}
				break
			}
		}
	case peer.TorGetStats:
		c.Ch <- &peer.TorStats{
			t.Pieces.Length(),
			t.Pieces.PieceSize(),
			len(t.peers),
			t.known.Count(),
			len(t.trackers),
			len(t.webseeds),
		}
		close(c.Ch)
	case peer.TorGetAvailable:
		available := make([]uint16, len(t.available))
		copy(available, t.available)
		c.Ch <- available
		close(c.Ch)
	case peer.TorDropPeer:
		p := findIdlePeer(t)
		if p != nil {
			writePeer(p, peer.PeerDone{})
			c.Ch <- true
		} else {
			c.Ch <- false
		}
		close(c.Ch)
	case peer.TorGetPeer:
		i := slices.IndexFunc(t.peers, func(p *peer.Peer) bool {
			return p.Id.Equal(c.Id)
		})
		if i >= 0 {
			c.Ch <- t.peers[i]
		}
		close(c.Ch)
	case peer.TorGetPeers:
		c.Ch <- slices.Clone(t.peers)
		close(c.Ch)
	case peer.TorGetKnown:
		var p *known.Peer
		if c.Id == nil {
			p = known.Find(t.known, c.IP, c.Port, nil, "",
				known.None)
		} else {
			p = known.FindId(t.known, c.Id, c.IP, c.Port)
		}
		if p != nil {
			c.Ch <- *p
		}
		close(c.Ch)
	case peer.TorGetKnowns:
		knowns := make([]known.Peer, 0, t.known.Count())
		for _, k := range t.known {
			knowns = append(knowns, *k)
		}
		c.Ch <- knowns
		close(c.Ch)
	case peer.TorPeerInterested:
		maybeUnchoke(t, false)
	case peer.TorPeerUnchoke:
		if c.Unchoke {
			maybeRequest(ctx, t)
		}
	case peer.TorPeerGoaway:
		found := delPeer(t, c.Peer)
		if !found {
			t.Log.Printf("Tried to remove unknown peer %v",
				c.Peer.IP)
		}
		maybeUnchoke(t, false)
	case peer.TorData:
		if t.infoComplete == 0 {
			t.Log.Printf("Data: metadata incomplete")
			return nil
		}
		if c.Peer != nil {
			active(c.Peer)
		}
		if c.Begin%config.ChunkSize != 0 {
			t.Log.Printf("TorData: odd offset")
			return nil
		}
		if c.Begin+c.Length > t.Pieces.PieceSize() {
			t.Log.Printf("TorData spans pieces")
			return nil
		}
		if c.Complete {
			finalisePiece(t, c.Index)
		}
		cpp := t.Pieces.PieceSize() / config.ChunkSize
		chunks := c.Length / config.ChunkSize
		for i := uint32(0); i < chunks; i++ {
			chunk := c.Index*cpp + c.Begin/config.ChunkSize + i
			noteInFlight(t, chunk, false)
			if inFlight(t, chunk) > 0 {
				writePeers(t, peer.PeerCancel{chunk}, c.Peer)
			}
		}
	case peer.TorDrop:
		if t.infoComplete == 0 {
			t.Log.Printf("TorDrop: metadata incomplete")
			return nil
		}
		if c.Begin%config.ChunkSize != 0 {
			t.Log.Printf("TorDrop: odd offset")
			return nil
		}
		if c.Begin+c.Length > t.Pieces.PieceSize() {
			t.Log.Printf("TorDrop spans pieces")
			return nil
		}
		cpp := t.Pieces.PieceSize() / config.ChunkSize
		chunks := c.Length / config.ChunkSize
		for i := uint32(0); i < chunks; i++ {
			chunk := c.Index*cpp + c.Begin/config.ChunkSize + i
			noteInFlight(t, chunk, false)
		}
	case peer.TorRequest:
		if t.infoComplete == 0 {
			if c.Ch != nil {
				close(c.Ch)
			}
			return nil
		}
		ch, added := requestPiece(t, c.Index, c.Priority, c.Request,
			c.Ch != nil)
		if c.Ch != nil {
			c.Ch <- ch
			close(c.Ch)
		}
		if added {
			maybeRequest(ctx, t)
		}
	case peer.TorMetaData:
		if t.infoComplete != 0 {
			return nil
		}
		active(c.Peer)
		done, err := gotMetadata(t, c.Index, c.Size, c.Data)
		if err != nil {
			t.Log.Printf("Metadata: %v", err)
			return nil
		}
		if !done {
			requestMetadata(t, c.Peer)
			return nil
		}
		writePeers(t, peer.PeerMetadataComplete{t.Info}, nil)
		periodicRequest(ctx, t)
	case peer.TorPeerBitmap:
		c.Bitmap.Range(func(i int) bool {
			noteAvailable(t, uint32(i), c.Have)
			return true
		})
	case peer.TorPeerHave:
		noteAvailable(t, c.Index, c.Have)
	case peer.TorHave:
		writePeers(t, peer.PeerHave{c.Index, c.Have}, nil)
		if c.Have {
			t.requested.Done(c.Index)
		}
	case peer.TorAnnounce:
		t.announce(c.IPv6)
	case peer.TorGetConf:
		conf := peer.TorConf{
			UseDht:      peer.ConfGet(t.useDht),
			DhtPassive:  peer.ConfGet(t.dhtPassive),
			UseTrackers: peer.ConfGet(t.useTrackers),
			UseWebseeds: peer.ConfGet(t.useWebseeds),
		}
		c.Ch <- conf
		close(c.Ch)
	case peer.TorSetConf:
		announce :=
			(!t.useDht && c.Conf.UseDht == peer.ConfTrue) ||
				(t.dhtPassive && c.Conf.DhtPassive == peer.ConfFalse)
		peer.ConfSet(&t.useDht, c.Conf.UseDht)
		peer.ConfSet(&t.dhtPassive, c.Conf.DhtPassive)
		peer.ConfSet(&t.useTrackers, c.Conf.UseTrackers)
		peer.ConfSet(&t.useWebseeds, c.Conf.UseWebseeds)
		if c.Ch != nil {
			close(c.Ch)
		}
		if announce {
			t.announce(true)
			t.announce(false)
		}
		t.requested.DelIdle()
		maybeRequest(ctx, t)
	case peer.TorGoAway:
		return io.EOF
	default:
		t.Log.Printf("Unknown event %#v", c)
		panic("Uknown event")
	}
	return nil
}

// writePeer sends an event to a peer.
func writePeer(p *peer.Peer, e peer.PeerEvent) error {
	select {
	case p.Event <- e:
		return nil
	case <-p.Done:
		return io.EOF
	}
}

// writePeers writes an event to all peers.  If non-nil, except indicates
// a peer to be excluded.
func writePeers(t *Torrent, e peer.PeerEvent, except *peer.Peer) {
	for _, p := range t.peers {
		if p != except {
			writePeer(p, e)
		}
	}
}

// maybeWritePeer is like write peer, but excludes congested peers.
func maybeWritePeer(p *peer.Peer, e peer.PeerEvent) error {
	select {
	case p.Event <- e:
		return nil
	case <-p.Done:
		return io.EOF
	default:
		return peer.ErrCongested
	}
}

// delPeer removes a peer after it sent TorPeerGoaway.
func delPeer(t *Torrent, p *peer.Peer) bool {
	i := slices.Index(t.peers, p)
	if i >= 0 {
		t.peers = slices.Delete(t.peers, i, i+1)
		if len(t.peers) == 0 {
			t.peers = nil
		}
	}
	// at this point, the dying peer won't reply to a GetPex request
	port := p.GetPort()
	if port > 0 {
		pp := []pex.Peer{{
			IP:   p.IP,
			Port: port,
		}}
		writePeers(t, peer.PeerPex{pp, false}, nil)
	}
	return i >= 0
}

// setRequestInterval sets the interval of the request ticker.
func (t *Torrent) setRequestInterval(interval time.Duration) {
	if interval == t.requestInterval ||
		(interval > 0 &&
			t.requestInterval > interval-interval/8 &&
			t.requestInterval < interval+interval/8) {
		return
	}

	if interval == 0 {
		if t.requestTicker != nil {
			t.requestTicker.Stop()
			t.requestTicker = nil
			t.requestInterval = 0
			t.requestSeconds = 0
		}
		return
	}

	if t.requestTicker == nil {
		t.requestTicker = time.NewTicker(interval)
	} else {
		t.requestTicker.Reset(interval)
	}
	t.requestInterval = interval
	t.requestSeconds = float64(interval) / float64(time.Second)
}

const (
	fastInterval = 250 * time.Millisecond
	slowInterval = 2 * time.Second
)

// finalisePIece is called when a piece is complete in order to verify its
// hash and trigger sending data to clients.
func finalisePiece(t *Torrent, index uint32) {
	if index >= uint32(len(t.PieceHashes)) {
		t.Log.Printf("FinalisePiece: %v >= %v",
			index, len(t.PieceHashes))
		return
	}
	go func(index uint32, h hash.Hash) {
		done, peers, err := t.Pieces.Finalise(index, h)
		if done {
			t.Have(index, true)
			t.BadPeers(peers, false)
		}
		if errors.Is(err, piece.ErrHashMismatch) {
			t.Log.Printf("Hash mismatch")
			t.BadPeers(peers, true)
		} else if err != nil {
			t.Log.Printf("Finalise failed: %v", err)
		}
	}(index, t.PieceHashes[index])
}

func maybeInterested(t *Torrent) {
	if !t.amInterested && len(t.requested.pieces) > 0 {
		t.amInterested = true
		writePeers(t, peer.PeerInterested{true}, nil)
	}
}

func notInterested(t *Torrent) {
	if t.amInterested {
		t.amInterested = false
		writePeers(t, peer.PeerInterested{false}, nil)
	}
}

// requestPiece requests a piece with a given priority, or cancels a piece
// request, depending on the value of request.  It returns a boolean
// indicating whether the piece was actually added, and a channel that
// will be closed when the piece is complete (nil if already complete).
func requestPiece(t *Torrent, index uint32, prio int8, request bool, want bool) (<-chan struct{}, bool) {
	if index > uint32(len(t.PieceHashes)) {
		return nil, false
	}

	t.requested.time = time.Now()

	var done <-chan struct{}
	added := false
	if request {
		if t.Pieces.Complete(index) {
			want = false
		}
		done, added = t.requested.Add(index, prio, want)
		maybeInterested(t)
	} else {
		removed := t.requested.Del(index, prio)
		if removed && t.requested.pieces[index] == nil {
			writePeers(t, peer.PeerCancelPiece{index}, nil)
		}
	}
	return done, added
}

// noteAvailable increases or decreases the local availability of a piece.
func noteAvailable(t *Torrent, index uint32, have bool) {
	if len(t.available) <= int(index) {
		t.available = append(t.available,
			make([]uint16, int(index)-len(t.available)+1)...)
	}
	if have {
		if t.available[index] >= ^uint16(0) {
			t.Log.Printf("Eek!  Available overflow.")
			return
		}
		t.available[index]++
	} else {
		if t.available[index] <= 0 {
			t.Log.Printf("Eek!  Available underflow.")
			return
		}
		t.available[index]--
	}
}

// noteInFlight increases or decreases the count of in-flight requests for
// a chunk.
func noteInFlight(t *Torrent, index uint32, have bool) {
	if have {
		if t.inFlight[index] >= ^uint8(0) {
			t.Log.Printf("Eek!  InFlight overflow.")
			return
		}
		t.inFlight[index]++
	} else {
		if t.inFlight[index] <= 0 {
			t.Log.Printf("Eek!  InFlight underflow.")
			return
		}
		t.inFlight[index]--
	}
}

type chunk struct {
	index uint32
	prio  int8
}

// getChunks returns a list of requested available chunks and a list of
// requested unavailable chunks.
func getChunks(t *Torrent, prio int8, max int) ([]chunk, []uint32) {
	cpp := t.Pieces.PieceSize() / config.ChunkSize
	var chunks []chunk
	var unavailable []uint32
	maxIF := maxInFlight(prio)
outer:
	for index, r := range t.requested.pieces {
		if !hasPriority(r, prio) || t.Pieces.Complete(index) {
			continue
		}
		if available(t, index) <= 0 {
			unavailable = append(unavailable, index)
			continue
		}
		n, bitmap := t.Pieces.PieceBitmap(index)
		if bitmap.All(n) {
			// is this needed?
			finalisePiece(t, index)
			continue
		}
		for i := 0; i < n; i++ {
			ch := index*uint32(cpp) + uint32(i)
			if inFlight(t, ch) >= maxIF {
				continue
			}
			if !bitmap.Get(i) {
				chunks = append(chunks, chunk{ch, prio})
				if max >= 0 && len(chunks) >= max {
					break outer
				}
			}
		}
	}
	return chunks, unavailable
}

// numChunks computes the number of chunks that should be in-flight.
func numChunks(t *Torrent, rate float64) int {
	return int(rate*t.requestSeconds*(1/float64(config.ChunkSize)) + 0.5)
}

// request requests that a peer request a list of chunks.
func request(t *Torrent, p *peer.Peer, indices []uint32) error {
	err := maybeWritePeer(p, peer.PeerRequest{indices})
	if err == nil {
		for _, c := range indices {
			noteInFlight(t, c, true)
		}
	}
	return err
}

// maxInFlight returns the maximum number of in-flight requests that we am for.
func maxInFlight(prio int8) uint8 {
	if prio > 0 {
		return 3
	}
	if prio >= 0 {
		return 2
	}
	return 1
}

// fastPieces is the number of pieces at which we stop listening to
// "Allowed Fast" requests (BEP-006)
const fastPieces = 8

// maybeRequest requests some chunks.  Or not.
func maybeRequest(ctx context.Context, t *Torrent) {
	prio := pickPriority(t)
	if prio > IdlePriority {
		periodicRequest(ctx, t)
	}
}

// periodicRequest is called periodically to request more chunks.
func periodicRequest(ctx context.Context, t *Torrent) {
	if t.infoComplete == 0 {
		t.setRequestInterval(0)
		return
	}

	prio := pickPriority(t)

	var rate float64

	if prio > IdlePriority {
		t.requested.DelIdle()
		t.requested.time = time.Now()
		if prio > 0 {
			rate = peer.DownloadEstimator.Estimate() * 1.5
		}
		if rate < config.PrefetchRate {
			rate = config.PrefetchRate
		}
	} else {
		irate := config.IdleRate()
		if irate == 0 {
			t.setRequestInterval(0)
			return
		}
		rate = 2*float64(irate) - peer.DownloadEstimator.Estimate()
		if rate <= 0 {
			t.setRequestInterval(slowInterval)
			return
		}
		count := int(rate*60/float64(t.Pieces.PieceSize()) + 0.5)
		if count < 2 {
			count = 2
		}
		incomplete :=
			t.requested.Count(func(index uint32) bool {
				return !t.Pieces.Complete(index)
			})
		if incomplete < count {
			pickIdlePieces(t, count-incomplete)
		}
	}

	interval := time.Duration(float64(2*config.ChunkSize) / rate *
		float64(time.Second))
	if interval < fastInterval {
		interval = fastInterval
	}
	if interval > slowInterval {
		interval = slowInterval
	}
	t.setRequestInterval(interval)

	cpp := t.Pieces.PieceSize() / config.ChunkSize

	count := numChunks(t, rate)
	if count < 2 {
		count = 2
	}

	chunks, unavailable := getChunks(t, prio, -1) // full list
	if prio > IdlePriority {
		q := prio - 1
		for len(chunks) < count && q >= -1 {
			more, umore := getChunks(t, q, count-len(chunks))
			chunks = append(chunks, more...)
			unavailable = append(unavailable, umore...)
			q--
		}
	}
	if len(chunks) == 0 && len(unavailable) == 0 {
		t.setRequestInterval(slowInterval)
		return
	}

	webseedDone := false
	if hasWebseeds(t) {
		for _, u := range unavailable {
			webseedDone :=
				maybeWebseed(ctx, t, u, prio <= IdlePriority)
			if webseedDone {
				break
			}
		}
	} else if len(t.peers) == 0 {
		t.setRequestInterval(0)
		return
	}

	slices.SortStableFunc(chunks, func(a, b chunk) int {
		return cmp.Compare(inFlight(t, a.index), inFlight(t, a.index))
	})

	if len(chunks) > count {
		chunks = chunks[:count]
	}

	pcount := t.Pieces.Count()
	delay := 1

outer:
	for pass := 0; pass < 4; pass++ {
		ps := t.rand.Perm(len(t.peers))
		for _, pn := range ps {
			p := t.peers[pn]
			var fast []uint32
			if !p.Unchoked() {
				if pcount < fastPieces {
					fast = p.GetFast()
				}
				if len(fast) == 0 {
					continue
				}
			}
			stats := p.GetStatus()
			if stats == nil {
				continue
			}

			cmax := numChunks(t, float64(delay)*stats.Download)
			if cmax <= 2 {
				cmax = 2
			}
			cmax -= stats.Qlen
			if cmax <= 0 {
				continue
			}
			var pbitmap bitmap.Bitmap
			if !stats.Seed {
				pbitmap = p.GetBitmap()
				if pbitmap == nil {
					continue
				}
			}
			req := make([]chunk, 0)
			cn := 0
			for cn < len(chunks) && len(req) < cmax {
				c := chunks[cn]
				p := c.index / cpp
				if inFlight(t, c.index) < maxInFlight(c.prio) &&
					(stats.Unchoked || isFast(p, fast)) &&
					(stats.Seed || pbitmap.Get(int(p))) {
					req = append(req, c)
					chunks = slices.Delete(chunks, cn, cn+1)
				} else {
					cn++
				}
			}
			if len(req) > 0 {
				indices := make([]uint32, len(req))
				for i, c := range req {
					indices[i] = c.index
				}
				err := request(t, p, indices)
				if err != nil {
					chunks = append(chunks, req...)
				}
			}
			if len(chunks) == 0 {
				break outer
			}
		}
		delay *= 2
	}

	if !webseedDone && hasWebseeds(t) {
		slices.SortStableFunc(chunks, func(a, b chunk) int {
			return cmp.Compare(a.index%cpp, b.index%cpp)
		})
		for _, c := range chunks {
			if inFlight(t, c.index) == 0 {
				webseedDone = maybeWebseed(ctx, t, c.index/cpp,
					c.prio <= IdlePriority)
				if webseedDone {
					break
				}
			}
		}
	}
}

type filechunk struct {
	path       path.Path
	filelength int64
	offset     int64
	length     int64
	pad        bool
}

func fileChunks(t *Torrent, index, offset, length uint32) []filechunk {
	o := int64(index)*int64(t.Pieces.PieceSize()) + int64(offset)
	l := int64(length)
	if t.Files == nil {
		return []filechunk{{nil,
			t.Pieces.Length(), o, l, false}}
	}
	var fcs []filechunk
	for _, f := range t.Files {
		if f.Offset+f.Length <= o {
			continue
		}
		if f.Offset >= o+l {
			break
		}
		m := f.Length - (o - f.Offset)
		if m > l {
			m = l
		}
		fcs = append(fcs, filechunk{
			f.Path,
			f.Length,
			o - f.Offset,
			m,
			f.Padding,
		})
		o += m
		l -= m
		if l <= 0 {
			break
		}
	}
	return fcs
}

// maybeWebseed requests some data from webseeds.  Or not.
func maybeWebseed(ctx context.Context, t *Torrent, index uint32, idle bool) bool {
	if !hasWebseeds(t) {
		return false
	}

	wn := t.rand.Perm(len(t.webseeds))
	slices.SortFunc(wn, func(i, j int) int {
		wsi := t.webseeds[i]
		wsj := t.webseeds[j]
		if wsi.Count() != wsj.Count() {
			return cmp.Compare(wsi.Count(), wsj.Count())
		}
		if t.rand.Intn(10) == 0 {
			return 1
		}
		return cmp.Compare(wsj.Rate(), wsi.Rate())
	})
	var ws webseed.Webseed
	for n := range wn {
		ws = t.webseeds[wn[n]]
		if ws.Ready(idle) {
			break
		}
		ws = nil
	}
	if ws == nil {
		return false
	}

	cpp := t.Pieces.PieceSize() / config.ChunkSize
	var o, l uint32
	for {
		o, l = t.Pieces.Hole(index, o)
		if o == ^uint32(0) {
			return false
		}

		ch := index*cpp + o/config.ChunkSize
		if inFlight(t, ch) == 0 {
			break
		}
		o = o + config.ChunkSize
	}

	if l > 1024*1024 {
		rate := ws.Rate()
		bytes := uint32(rate * 5)
		m := uint32(1024 * 1024)
		if bytes > m {
			m = bytes + config.ChunkSize - 1
			m = (m / config.ChunkSize) * config.ChunkSize
		}
		if l > m {
			l = m
		}
	}

	for i := uint32(0); i < uint32(l); i += config.ChunkSize {
		chunk := index*cpp + (uint32(o)+i)/config.ChunkSize
		noteInFlight(t, chunk, true)
	}

	switch ws := ws.(type) {
	case *webseed.GetRight:
		go webseedGR(ctx, ws, t, index, o, l)
	case *webseed.Hoffman:
		go webseedH(ctx, ws, t, index, o, l)
	default:
		panic("Eek")
	}
	return true
}

type zeroReader struct {
	n int64
}

func (r *zeroReader) Read(buf []byte) (int, error) {
	n := len(buf)
	if int64(n) > r.n {
		n = int(r.n)
	}
	for i := 0; i < n; i++ {
		buf[i] = 0
	}
	r.n -= int64(n)
	var err error
	if r.n <= 0 {
		err = io.EOF
	}
	return n, err
}

// webseedGR fetches data from a GetRight webseed.
func webseedGR(ctx context.Context, ws *webseed.GetRight, t *Torrent, index, offset, length uint32) {
	fcs := fileChunks(t, index, offset, length)
	writer := NewWriter(t, index, offset, length)
	defer writer.Close()
	for _, fc := range fcs {
		var n int64
		var err error
		if fc.pad {
			n, err = io.Copy(writer, &zeroReader{fc.length})
		} else {
			n, err = ws.Get(ctx, t.proxy, t.Name,
				fc.path, fc.filelength, fc.offset, fc.length,
				writer)
		}
		if err != nil {
			t.Log.Printf("webseed: %v", err)
			break
		}
		if n != fc.length {
			break
		}
	}
}

// webseedH fetches data from a Hoffman-style webseed.
func webseedH(ctx context.Context, ws *webseed.Hoffman,
	t *Torrent, index, offset, length uint32) {
	w := NewWriter(t, index, offset, length)
	defer w.Close()
	_, err := ws.Get(ctx, t.proxy, t.Hash, index, offset, length, w)
	if err != nil {
		t.Log.Printf("webseed: %v", err)
	}
}

func hasWebseeds(t *Torrent) bool {
	return t.useWebseeds && len(t.webseeds) > 0
}

func available(t *Torrent, index uint32) uint16 {
	if index >= uint32(len(t.available)) {
		return 0
	}
	return t.available[index]
}

func inFlight(t *Torrent, index uint32) uint8 {
	return t.inFlight[index]
}

func pickPriority(t *Torrent) int8 {
	prio := IdlePriority
	for index, r := range t.requested.pieces {
		if t.Pieces.Complete(index) ||
			(!hasWebseeds(t) && available(t, index) == 0) {
			continue
		}
		for _, p := range r.prio {
			if p > prio {
				prio = p
			}
		}
	}
	return prio
}

func isFast(i uint32, fast []uint32) bool {
	return slices.Index(fast, i) >= 0
}

// pickIdlePieces chooses pieces to request when we are idle.
func pickIdlePieces(t *Torrent, count int) {
	maxp := t.Pieces.Num()
	pcount := t.Pieces.Count()
	ps := t.rand.Perm(maxp)
	slices.SortFunc(ps, func(i, j int) int {
		p1 := uint32(i)
		p2 := uint32(j)
		// complete pieces last
		c1 := t.Pieces.Complete(p1)
		c2 := t.Pieces.Complete(p2)
		if !c1 && c2 {
			return -1
		} else if c1 && !c2 {
			return 1
		} else if c1 && c2 {
			return 0
		}

		a1 := available(t, p1)
		a2 := available(t, p2)
		if !hasWebseeds(t) {
			// unavailable last
			if a1 > 0 && a2 == 0 {
				return -1
			} else if a1 == 0 && a2 > 0 {
				return 1
			}
			if a1 == 0 && a2 == 0 {
				return 0
			}
		}
		// empty pieces last
		e1 := t.Pieces.PieceEmpty(p1)
		e2 := t.Pieces.PieceEmpty(p2)
		if !e1 && e2 {
			return -1
		} else if e1 && !e2 {
			return 1
		}
		if e1 && e2 {
			// both empty
			if pcount >= 4 {
				// already bootstrapped: rarest first
				if a1 == 0 {
					a1 = 1
				}
				if a2 == 0 {
					a2 = 1
				}
				return cmp.Compare(a1, a2)
			} else {
				return 0
			}
		}
		// fewest chunks remaining
		n1, b1 := t.Pieces.PieceBitmap(p1)
		n2, b2 := t.Pieces.PieceBitmap(p2)
		return cmp.Compare(n1-b1.Count(), n2-b2.Count())
	})

	n := 0
	add := func(i uint32) bool {
		_, done := t.requested.Add(uint32(i), IdlePriority, false)
		t.requested.time = time.Now()
		if done {
			maybeInterested(t)
			chunks, bitmap := t.Pieces.PieceBitmap(i)
			if false {
				t.Log.Printf("Selected idle piece %v "+
					"(available %v, chunks %v/%v)",
					i, available(t, i),
					bitmap.Count(), chunks)
			}
			n++
		}
		return n >= count
	}

	// pick incomplete piece
	for _, pn := range ps {
		if t.Pieces.Complete(uint32(pn)) ||
			t.Pieces.PieceEmpty(uint32(pn)) {
			break
		}
		if available(t, uint32(pn)) > 0 || hasWebseeds(t) {
			if add(uint32(pn)) {
				return
			}
		}
	}

	if alloc.Bytes() >= config.MemoryLowMark() {
		return
	}

	if pcount < fastPieces {
		for _, p := range t.peers {
			fast := p.GetFast()
			for _, i := range fast {
				if !t.Pieces.Complete(i) && p.GetHave(i) {
					if add(i) {
						return
					}
				}
			}
		}
	}

	for _, pn := range ps {
		if t.Pieces.Complete(uint32(pn)) {
			break
		}
		if available(t, uint32(pn)) > 0 || hasWebseeds(t) {
			if add(uint32(pn)) {
				return
			}
		}
	}
}

// maybeConnect attempts to connect a new peer.  Or not.
func maybeConnect(ctx context.Context, t *Torrent) {
	max := config.MinPeersPerTorrent
	if t.hasProxy() {
		max = config.MaxPeersPerTorrent
	}
	if len(t.peers) >= max || t.known.Count() == 0 {
		return
	}

	peers := make([]*known.Peer, t.known.Count())
	pn := rand.Perm(len(peers))
	i := 0
	for _, kp := range t.known {
		if time.Since(kp.ConnectAttemptTime) > time.Hour {
			kp.Attempts = 0
		}
		peers[pn[i]] = kp
		i++
	}

	slices.SortFunc(peers, func(a, b *known.Peer) int {
		return cmp.Compare(a.Attempts, b.Attempts)
	})

	count := 0
	for _, kp := range peers {
		if !kp.Recent() || kp.Bad() {
			continue
		}
		if kp.Id != nil && kp.Id.Equal(t.MyId) {
			continue
		}
		if kp.Id != nil && findPeer(t, kp.Id) != nil {
			continue
		}
		when := time.Since(kp.ConnectAttemptTime)
		if kp.Attempts >= 3 ||
			when < time.Duration(1<<kp.Attempts)*time.Minute {
			continue
		}
		kp.Update("", known.ConnectAttempt)
		go func(kp *known.Peer) {
			delay := time.Duration(
				rand.Int63n(int64(3 * time.Second)))
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			err := DialClient(ctx, t, kp.Addr.IP, kp.Addr.Port,
				crypto.DefaultOptions(
					config.PreferEncryption,
					config.ForceEncryption,
				),
			)
			if err != nil && config.Debug {
				t.Log.Printf("DialClient: %v", err)
			}
		}(kp)
		count++
		if len(t.peers)+count >= max {
			break
		}
	}
}

// maxUnchoking is the maximum number of peers that we unchoke in a torrent
const maxUnchoking = 5

// maybeUnchoke unchokes a new peer.  If periodic is false, then no
// currently unchoked peer will be choked.
func maybeUnchoke(t *Torrent, periodic bool) {
	unchoking := make([]*peer.Peer, 0, maxUnchoking)
	interested := make([]*peer.Peer, 0)
	ps := t.rand.Perm(len(t.peers))
	for _, pn := range ps {
		p := t.peers[pn]
		if p.AmUnchoking() {
			unchoking = append(unchoking, p)
		} else if p.Interested() {
			interested = append(interested, p)
		}
	}

	if !periodic && len(unchoking) == maxUnchoking {
		return
	}

	slices.SortFunc(unchoking, func(a, b *peer.Peer) int {
		s1 := a.GetStatus()
		s2 := b.GetStatus()
		if s1 == nil && s2 == nil {
			return 0
		} else if s2 == nil {
			return -1
		} else if s1 == nil {
			return +1
		}
		return s1.UnchokeTime.Compare(s2.UnchokeTime)
	})

	for len(unchoking) > maxUnchoking {
		writePeer(unchoking[0], peer.PeerUnchoke{false})
		unchoking = unchoking[1:]
	}

	if !periodic && len(unchoking) >= maxUnchoking {
		return
	}

	opportunistic := t.rand.Intn(5) == 0
	slices.SortFunc(interested, func(a, b *peer.Peer) int {
		s1 := a.GetStatus()
		s2 := b.GetStatus()

		if s1 == nil && s2 == nil {
			return 0
		} else if s2 == nil {
			return -1
		} else if s1 == nil {
			return +1
		}

		if !opportunistic {
			if s1.AvgDownload != s2.AvgDownload {
				return cmp.Compare(
					s2.AvgDownload, s1.AvgDownload,
				)
			}
		}

		return s1.UnchokeTime.Compare(s2.UnchokeTime)
	})

	n := 0
	for len(interested) > 0 && len(unchoking)+n < maxUnchoking {
		err := writePeer(interested[0], peer.PeerUnchoke{true})
		if err == nil {
			n++
			if opportunistic {
				return
			}
		}
		interested = interested[1:]
	}

	if !periodic || n > 0 {
		return
	}

	for len(interested) > 0 && interested[0].AmUnchoking() {
		interested = interested[1:]
	}

	if len(interested) > 0 && len(unchoking) > 0 {
		writePeer(unchoking[0], peer.PeerUnchoke{false})
		writePeer(interested[0], peer.PeerUnchoke{true})
	}
}

func findPeer(t *Torrent, id hash.Hash) *peer.Peer {
	i := slices.IndexFunc(t.peers, func(p *peer.Peer) bool {
		return id.Equal(p.Id)
	})
	if i >= 0 {
		return t.peers[i]
	}
	return nil
}

func (t *Torrent) hasProxy() bool {
	return t.proxy != ""
}

func (t *Torrent) Kill(ctx context.Context) error {
	select {
	case <-t.Done:
		return ErrTorrentDead
	case <-ctx.Done():
		return ctx.Err()
	case t.Event <- peer.TorGoAway{}:
		select {
		case <-t.Deleted:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (t *Torrent) NewPeer(proxy string, conn net.Conn, ip net.IP, port int, incoming bool, result protocol.HandshakeResult, init []byte) error {
	if !result.Hash.Equal(t.Hash) {
		conn.Close()
		return errors.New("hash mismatch")
	}
	p := peer.New(proxy, conn, ip, port, incoming, result)
	if p == nil {
		conn.Close()
		return errors.New("couldn't create peer")
	}

	select {
	case t.Event <- peer.TorAddPeer{p, init}:
		return nil
	case <-t.Done:
		conn.Close()
		return ErrTorrentDead
	}
}

func (t *Torrent) AddKnown(ip net.IP, port int, id hash.Hash, version string,
	kind known.Kind) error {
	select {
	case t.Event <- peer.TorAddKnown{nil, ip, port, id, version, kind}:
		return nil
	case <-t.Done:
		return ErrTorrentDead
	}
}

func findIdlePeer(t *Torrent) *peer.Peer {
	seed := t.Pieces.All()
	ps := t.rand.Perm(len(t.peers))
	for _, pn := range ps {
		p := t.peers[pn]
		if seed {
			s := p.GetStatus()
			if s == nil || s.Seed || s.UploadOnly {
				return p
			}
		}
		port := p.GetPort()
		if port <= 0 {
			continue
		}
		kp := known.Find(t.known, p.IP, port, nil, "", known.None)
		if kp != nil {
			tm := time.Since(kp.ActiveTime)
			if tm > 5*time.Minute {
				return p
			}
		}
	}
	return nil

}

// BadPeer is called when a peer participated in a failed piece.
func (t *Torrent) BadPeer(p uint32, bad bool) error {
	select {
	case t.Event <- peer.TorBadPeer{p, bad}:
		return nil
	case <-t.Done:
		return ErrTorrentDead
	}
}

func (t *Torrent) BadPeers(peers []uint32, bad bool) {
	for _, p := range peers {
		t.BadPeer(p, bad)
	}
}

func (t *Torrent) GetStats() (*peer.TorStats, error) {
	ch := make(chan *peer.TorStats)
	select {
	case t.Event <- peer.TorGetStats{ch}:
		select {
		case v := <-ch:
			return v, nil
		case <-t.Done:
			return nil, ErrTorrentDead
		}
	case <-t.Done:
		return nil, ErrTorrentDead
	}
}

// Available is an array indicating the number of locally available copies
// of each piece in a torrent.
type Available []uint16

func (a Available) Available(index int) int {
	if index < len(a) {
		return int(a[index])
	}
	return 0
}

// AvailableRange returns the number of copies of a subrange of a torrent.
func (a Available) AvailableRange(t *Torrent, offset int64, length int64) int {
	r := math.MaxInt32
	ps := int64(t.Pieces.PieceSize())
	for i := int(offset / ps); i <= int((offset+length-1)/ps); i++ {
		av := a.Available(i)
		if r > av {
			r = av
		}
	}
	return r
}

// GetAvailable returns information about piece availability.
func (t *Torrent) GetAvailable() (Available, error) {
	ch := make(chan []uint16)
	select {
	case t.Event <- peer.TorGetAvailable{ch}:
		select {
		case v := <-ch:
			return v, nil
		case <-t.Done:
			return nil, ErrTorrentDead
		}
	case <-t.Done:
		return nil, ErrTorrentDead
	}
}

// DropPeer requests that a peer should be dropped.
func (t *Torrent) DropPeer() (bool, error) {
	ch := make(chan bool)
	select {
	case t.Event <- peer.TorDropPeer{ch}:
		select {
		case v := <-ch:
			return v, nil
		case <-t.Done:
			return false, ErrTorrentDead
		}
	case <-t.Done:
		return false, ErrTorrentDead
	}
}

// GetPeer returns a given peer.
func (t *Torrent) GetPeer(id hash.Hash) (*peer.Peer, error) {
	ch := make(chan *peer.Peer)
	select {
	case t.Event <- peer.TorGetPeer{id, ch}:
		select {
		case v := <-ch:
			return v, nil
		case <-t.Done:
			return nil, ErrTorrentDead
		}
	case <-t.Done:
		return nil, ErrTorrentDead
	}
}

// GetPeers returns all peers of a torrent.
func (t *Torrent) GetPeers() ([]*peer.Peer, error) {
	ch := make(chan []*peer.Peer)
	select {
	case t.Event <- peer.TorGetPeers{ch}:
		select {
		case v := <-ch:
			return v, nil
		case <-t.Done:
			return nil, ErrTorrentDead
		}
	case <-t.Done:
		return nil, ErrTorrentDead
	}
}

func (t *Torrent) GetKnown(id hash.Hash, ip net.IP, port int) (*known.Peer, error) {
	ch := make(chan known.Peer)
	select {
	case t.Event <- peer.TorGetKnown{id, ip, port, ch}:
		select {
		case v, ok := <-ch:
			if !ok {
				return nil, nil
			}
			return &v, nil
		case <-t.Done:
			return nil, ErrTorrentDead
		}
	case <-t.Done:
		return nil, ErrTorrentDead
	}
}

func (t *Torrent) GetKnowns() ([]known.Peer, error) {
	ch := make(chan []known.Peer)
	select {
	case t.Event <- peer.TorGetKnowns{ch}:
		select {
		case v := <-ch:
			return v, nil
		case <-t.Done:
			return nil, ErrTorrentDead
		}
	case <-t.Done:
		return nil, ErrTorrentDead
	}
}

// Have indicates that a piece is available.
func (t *Torrent) Have(index uint32, have bool) error {
	select {
	case t.Event <- peer.TorHave{index, have}:
		return nil
	case <-t.Done:
		return ErrTorrentDead
	}
}

// GetConf returns the configuration of a torrent.
func (t *Torrent) GetConf() (peer.TorConf, error) {
	ch := make(chan peer.TorConf)
	select {
	case t.Event <- peer.TorGetConf{ch}:
		select {
		case v := <-ch:
			return v, nil
		case <-t.Done:
			return peer.TorConf{}, ErrTorrentDead
		}
	case <-t.Done:
		return peer.TorConf{}, ErrTorrentDead
	}
}

// SetConf reconfigures a torrent.
func (t *Torrent) SetConf(conf peer.TorConf) error {
	ch := make(chan struct{})
	select {
	case t.Event <- peer.TorSetConf{conf, ch}:
		{
			select {
			case <-ch:
				return nil
			case <-t.Done:
				return ErrTorrentDead
			}
		}
	case <-t.Done:
		return ErrTorrentDead
	}
}

// InfoComplete returns true if the metadata of a torrent is complete.
func (t *Torrent) InfoComplete() bool {
	return atomic.LoadUint32(&t.infoComplete) != 0
}

// Trackers returns the set of trackers of a torrent.
func (t *Torrent) Trackers() [][]tracker.Tracker {
	return t.trackers
}

// Webseeds returns the set of webseeds of a torrent.
func (t *Torrent) Webseeds() []webseed.Webseed {
	return t.webseeds
}

// Request requests a piece from a torrent with given priority and updates
// the piece's access time.  It returns a boolean indicating whether the
// piece was requested and a channel that will be closed when the piece is
// complete (nil if already complete).
func (t *Torrent) Request(index uint32, prio int8, request bool, want bool) (bool, <-chan struct{}, error) {
	if atomic.LoadUint32(&t.infoComplete) == 0 {
		return false, nil, ErrMetadataIncomplete
	}

	if index >= uint32(len(t.PieceHashes)) {
		return false, nil, errors.New("offset beyond end of torrent")
	}

	if request {
		complete := t.Pieces.UpdateTime(index)
		if complete {
			return false, nil, nil
		}
	}

	var ch chan (<-chan struct{})
	if want {
		ch = make(chan (<-chan struct{}))
	}
	select {
	case t.Event <- peer.TorRequest{index, prio, request, ch}:
		if ch != nil {
			return true, <-ch, nil
		}
		return true, nil, nil
	case <-t.Done:
		return false, nil, ErrTorrentDead
	}
}

// Expire discards pieces in order to meet our memory usage target.
func Expire() int {
	low := config.MemoryLowMark()
	high := config.MemoryHighMark()
	mid := (low + high) / 2

	space := alloc.Bytes()
	if space < mid {
		return +1
	} else if space < high {
		return 0
	}

	count := count()
	fair := low / int64(count)

	bigcount := 0
	var smallspace int64
	Range(func(h hash.Hash, t *Torrent) bool {
		bytes := t.Pieces.Bytes()
		if bytes <= fair {
			smallspace += bytes
		} else {
			bigcount++
		}
		return true
	})

	fair2 := (low - smallspace) / int64(bigcount)

	Range(func(h hash.Hash, t *Torrent) bool {
		if t.Pieces.Bytes() > fair2 {
			available, err := t.GetAvailable()
			if err != nil {
				t.Log.Printf("GetAvailable: %v", err)
			}
			go t.Pieces.Expire(fair2, available,
				func(index uint32) {
					t.Have(index, false)
				})
		}
		return true
	})
	return -1
}

// trackerAnnounce is called periodically to announce a torrent to its
// trackers.  It follows the uTorrent interpretation, not what the BEP says.
func trackerAnnounce(ctx context.Context, t *Torrent) {
	tn := t.rand.Perm(len(t.trackers))
	for _, i := range tn {
		tl := t.trackers[i]
		for _, tr := range tl {
			state, _ := tr.GetState()
			if state == tracker.Ready {
				go trackerAnnounceSingle(ctx, t, tr)
				return
			}
			if state != tracker.Error {
				// skip to next tier
				break
			}
		}
	}
}

func trackerAnnounceSingle(ctx context.Context,
	t *Torrent, tr tracker.Tracker) error {
	t.Log.Printf("Tracker announce to %v", tr.URL())
	var port4, port6 int
	if !t.hasProxy() {
		port4 = config.ExternalPort(true, false)
		port6 = config.ExternalPort(true, true)
	}
	var length int64
	if t.InfoComplete() {
		length = t.Pieces.Length()
	}
	want := config.MaxPeersPerTorrent - len(t.peers)
	if want < 0 {
		want = 0
	}
	err := tr.Announce(ctx, t.Hash, t.MyId,
		want, length, port4, port6, t.proxy,
		func(ip net.IP, port int) bool {
			select {
			case t.Event <- peer.TorAddKnown{
				nil, ip, port, nil, "", known.Tracker}:
				return true
			case <-t.Done:
				return false
			case <-ctx.Done():
				return false
			}
		})
	if err != nil {
		t.Log.Printf("Tracker %v: %v", tr.URL(), err)
		return err
	}
	t.Log.Printf("Tracker done %v", tr.URL())
	return nil
}
