package tor

import (
	"context"
	crand "crypto/rand"
	"errors"
	"io"
	"log"
	"math"
	"math/rand"
	"net"
	"os"
	"sort"
	"sync/atomic"
	"time"

	"storrent/alloc"
	"storrent/bitmap"
	"storrent/config"
	"storrent/crypto"
	"storrent/dht"
	"storrent/hash"
	"storrent/known"
	"storrent/peer"
	"storrent/pex"
	"storrent/protocol"
	"storrent/tor/piece"
	"storrent/tracker"
	"storrent/webseed"
)

var ErrTorrentDead = errors.New("torrent is dead")
var ErrMetadataIncomplete = errors.New("metadata incomplete")

type Torrent struct {
	proxy           string
	Hash            hash.Hash
	MyId            hash.Hash
	Info            []byte
	CreationDate    int64
	trackers        [][]tracker.Tracker
	webseeds        []webseed.Webseed
	infoComplete    uint32
	infoBitmap      bitmap.Bitmap
	infoRequested   []uint8
	infoSizeVotes   map[uint32]int
	PieceHashes     []hash.Hash
	Name            string
	Files           []Torfile
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
	requestInterval time.Duration
	requestSeconds  float64
	requestTicker   *time.Ticker
	announceTime    time.Time
	useDht          bool
	dhtPassive      bool
	useTrackers     bool
	useWebseeds     bool
}

type Torfile struct {
	Path    []string
	Offset  int64
	Length  int64
	Padding bool
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
		seed := t.Pieces.Bitmap().All(t.Pieces.Num())
		var q *peer.Peer
		ps := t.rand.Perm(len(t.peers))
		for _, pn := range ps {
			p := t.peers[pn]
			if seed {
				s := p.GetStatus()
				if s == nil || s.Seed || s.UploadOnly {
					q = p
					break
				}
			}
			port := p.GetPort()
			if port > 0 {
				kp := known.Find(t.known, p.IP, port, nil, "",
					known.None)
				if kp != nil {
					tm := time.Since(kp.ActiveTime)
					if tm > 5*time.Minute {
						q = p
						break
					}
				}
			}
		}
		if q != nil {
			writePeer(q, peer.PeerDone{})
			c.Ch <- true
		} else {
			c.Ch <- false
		}
		close(c.Ch)
	case peer.TorGetPeer:
		for _, p := range t.peers {
			if p.Id.Equals(c.Id) {
				c.Ch <- p
				break
			}
		}
		close(c.Ch)
	case peer.TorGetPeers:
		peers := make([]*peer.Peer, 0, len(t.peers))
		for _, p := range t.peers {
			peers = append(peers, p)
		}
		c.Ch <- peers
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
		kill := t.useTrackers && c.Conf.UseTrackers == peer.ConfFalse
		peer.ConfSet(&t.useDht, c.Conf.UseDht)
		peer.ConfSet(&t.dhtPassive, c.Conf.DhtPassive)
		peer.ConfSet(&t.useTrackers, c.Conf.UseTrackers)
		peer.ConfSet(&t.useWebseeds, c.Conf.UseWebseeds)
		if c.Ch != nil {
			close(c.Ch)
		}
		if kill {
			for _, tl := range t.trackers {
				if len(tl) > 0 {
					tl[0].Kill()
				}
			}
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

func writePeer(p *peer.Peer, e peer.PeerEvent) error {
	select {
	case p.Event <- e:
		return nil
	case <-p.Done:
		return io.EOF
	}
}

func writePeers(t *Torrent, e peer.PeerEvent, except *peer.Peer) {
	for _, p := range t.peers {
		if p != except {
			writePeer(p, e)
		}
	}
}

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

func delPeer(t *Torrent, p *peer.Peer) bool {
	var found bool
	for i, q := range t.peers {
		if q == p {
			t.peers = append(t.peers[:i], t.peers[i+1:]...)
			if len(t.peers) == 0 {
				t.peers = nil
			}
			found = true
			break
		}
	}
	// at this point, the dying peer won't reply to a GetPex request
	port := p.GetPort()
	if port > 0 {
		pp := []pex.Peer{pex.Peer{
			IP:   p.IP,
			Port: port,
		}}
		writePeers(t, peer.PeerPex{pp, false}, nil)
	}
	return found
}

func (t *Torrent) setRequestInterval(interval time.Duration) {
	if interval == t.requestInterval ||
		(interval > 0 &&
			t.requestInterval > interval-interval/8 &&
			t.requestInterval < interval+interval/8) {
		return
	}

	if t.requestTicker != nil {
		t.requestTicker.Stop()
		t.requestTicker = nil
		t.requestInterval = 0
		t.requestSeconds = 0
	}

	if interval == 0 {
		return
	}

	t.requestTicker = time.NewTicker(interval)
	t.requestInterval = interval
	t.requestSeconds = float64(interval) / float64(time.Second)
}

const fastInterval = 250 * time.Millisecond
const slowInterval = 2 * time.Second

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

func requestPiece(t *Torrent, index uint32, prio int8, request bool, want bool) (<-chan struct{}, bool) {
	if index > uint32(len(t.PieceHashes)) {
		return nil, false
	}

	t.requested.time = time.Now()

	var done <-chan struct{}
	added := false
	if request {
		if t.Pieces.PieceComplete(index) {
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

func getChunks(t *Torrent, prio int8, max int) ([]chunk, []uint32) {
	cpp := t.Pieces.PieceSize() / config.ChunkSize
	var chunks []chunk
	var unavailable []uint32
	maxIF := maxInFlight(prio)
outer:
	for index, r := range t.requested.pieces {
		if !hasPriority(r, prio) || t.Pieces.PieceComplete(index) {
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

func numChunks(t *Torrent, rate float64) int {
	return int(rate*t.requestSeconds*(1/float64(config.ChunkSize)) + 0.5)
}

func request(t *Torrent, p *peer.Peer, indices []uint32) error {
	err := maybeWritePeer(p, peer.PeerRequest{indices})
	if err == nil {
		for _, c := range indices {
			noteInFlight(t, c, true)
		}
	}
	return err
}

func maxInFlight(prio int8) uint8 {
	if prio > 0 {
		return 3
	}
	if prio >= 0 {
		return 2
	}
	return 1
}

const fastPieces = 8

func maybeRequest(ctx context.Context, t *Torrent) {
	prio := pickPriority(t)
	if prio > IdlePriority {
		periodicRequest(ctx, t)
	}
}

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
		rate = 2*irate - peer.DownloadEstimator.Estimate()
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
				return !t.Pieces.PieceComplete(index)
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

	sort.SliceStable(chunks, func(i, j int) bool {
		fi := inFlight(t, chunks[i].index)
		fj := inFlight(t, chunks[j].index)
		return fi < fj
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
					chunks = append(chunks[:cn],
						chunks[cn+1:]...)
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
		sort.SliceStable(chunks, func(i, j int) bool {
			return chunks[i].index%cpp < chunks[j].index%cpp
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
	path       []string
	filelength int64
	offset     int64
	length     int64
	pad        bool
}

func fileChunks(t *Torrent, index, offset, length uint32) []filechunk {
	o := int64(index)*int64(t.Pieces.PieceSize()) + int64(offset)
	l := int64(length)
	if t.Files == nil {
		return []filechunk{filechunk{nil,
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

func maybeWebseed(ctx context.Context, t *Torrent, index uint32, idle bool) bool {
	if !hasWebseeds(t) {
		return false
	}

	wn := t.rand.Perm(len(t.webseeds))
	sort.Slice(wn, func(i, j int) bool {
		wsi := t.webseeds[wn[i]]
		wsj := t.webseeds[wn[j]]
		if wsi.Count() < wsj.Count() {
			return true
		}
		if wsi.Count() > wsj.Count() {
			return false
		}
		if t.rand.Intn(10) == 0 {
			return false
		}
		return wsi.Rate() > wsj.Rate()
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

func webseedGR(ctx context.Context, ws *webseed.GetRight,
	t *Torrent, index, offset, length uint32) {
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
		if t.Pieces.PieceComplete(index) ||
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
	for _, f := range fast {
		if i == f {
			return true
		}
	}
	return false
}

func pickIdlePieces(t *Torrent, count int) {
	maxp := t.Pieces.Num()
	pcount := t.Pieces.Count()
	ps := t.rand.Perm(maxp)
	sort.Slice(ps, func(i, j int) bool {
		p1 := uint32(ps[i])
		p2 := uint32(ps[j])
		// complete pieces last
		c1 := t.Pieces.PieceComplete(p1)
		c2 := t.Pieces.PieceComplete(p2)
		if !c1 && c2 {
			return true
		} else if c1 && !c2 {
			return false
		}
		if c1 && c2 {
			return false
		}

		a1 := available(t, p1)
		a2 := available(t, p2)
		if !hasWebseeds(t) {
			// unavailable last
			if a1 > 0 && a2 == 0 {
				return true
			} else if a1 == 0 && a2 > 0 {
				return false
			}
			if a1 == 0 && a2 == 0 {
				return false
			}
		}
		// empty pieces last
		e1 := t.Pieces.PieceEmpty(p1)
		e2 := t.Pieces.PieceEmpty(p2)
		if !e1 && e2 {
			return true
		} else if e1 && !e2 {
			return false
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
				return a1 < a2
			} else {
				return false
			}
		}
		// fewest chunks remaining
		n1, b1 := t.Pieces.PieceBitmap(p1)
		n2, b2 := t.Pieces.PieceBitmap(p2)
		return n1-b1.Count() < n2-b2.Count()
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
		if t.Pieces.PieceComplete(uint32(pn)) ||
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
				if !t.Pieces.PieceComplete(i) && p.GetHave(i) {
					if add(i) {
						return
					}
				}
			}
		}
	}

	for _, pn := range ps {
		if t.Pieces.PieceComplete(uint32(pn)) {
			break
		}
		if available(t, uint32(pn)) > 0 || hasWebseeds(t) {
			if add(uint32(pn)) {
				return
			}
		}
	}

	return
}

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

	sort.Slice(peers, func(i, j int) bool {
		return peers[i].Attempts < peers[j].Attempts
	})

	count := 0
	for _, kp := range peers {
		if !kp.Recent() || kp.Bad() {
			continue
		}
		if kp.Id != nil && kp.Id.Equals(t.MyId) {
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
				crypto.OptionsMap[config.DefaultEncryption])
			if err != nil {
				t.Log.Printf("Client: %v", err)
			}
		}(kp)
		count++
		if len(t.peers)+count >= max {
			break
		}
	}
}

const maxUnchoking = 5

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

	sort.Slice(unchoking, func(i, j int) bool {
		s1 := unchoking[i].GetStatus()
		s2 := unchoking[j].GetStatus()
		if s2 == nil {
			return false
		}
		if s1 == nil {
			return true
		}
		return s1.UnchokeTime.Before(s2.UnchokeTime)
	})

	for len(unchoking) > maxUnchoking {
		writePeer(unchoking[0], peer.PeerUnchoke{false})
		unchoking = unchoking[1:]
	}

	if !periodic && len(unchoking) >= maxUnchoking {
		return
	}

	opportunistic := t.rand.Intn(5) == 0
	sort.Slice(interested, func(i, j int) bool {
		s1 := interested[i].GetStatus()
		s2 := interested[j].GetStatus()

		if s2 == nil {
			return true
		}
		if s1 == nil {
			return false
		}

		if !opportunistic {
			if s1.AvgDownload > s2.AvgDownload {
				return true
			}
			if s1.AvgDownload < s2.AvgDownload {
				return false
			}
		}

		return s1.UnchokeTime.Before(s2.UnchokeTime)
	})

	n := 0
	for len(interested) > 0 && len(unchoking)+n < maxUnchoking {
		err := writePeer(interested[0], peer.PeerUnchoke{true})
		if err == nil {
			n++
			if(opportunistic) {
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
	for _, p := range t.peers {
		if id.Equals(p.Id) {
			return p
		}
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

func (t *Torrent) NewPeer(proxy string, conn net.Conn,
	ip net.IP, port int, incoming bool,
	result protocol.HandshakeResult, init []byte) error {
	if !result.Hash.Equals(t.Hash) {
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

type Available []uint16

func (a Available) Available(index int) int {
	if index < len(a) {
		return int(a[index])
	}
	return 0
}

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

func (t *Torrent) Have(index uint32, have bool) error {
	select {
	case t.Event <- peer.TorHave{index, have}:
		return nil
	case <-t.Done:
		return ErrTorrentDead
	}
}

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

func (t *Torrent) InfoComplete() bool {
	return atomic.LoadUint32(&t.infoComplete) != 0
}

func (t *Torrent) Trackers() [][]tracker.Tracker {
	return t.trackers
}
func (t *Torrent) Webseeds() []webseed.Webseed {
	return t.webseeds
}

func (t *Torrent) Request(index uint32, prio int8, request bool,
	want bool) (bool, <-chan struct{}, error) {
	if atomic.LoadUint32(&t.infoComplete) == 0 {
		return false, nil, ErrMetadataIncomplete
	}

	if index >= uint32(len(t.PieceHashes)) {
		return false, nil, errors.New("offset beyond end of torrent")
	}

	if request {
		complete := t.Pieces.Update(index)
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
		return false, nil, nil
	}
}

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

// This follows the uTorrent interpretation, not what the BEP says.
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
