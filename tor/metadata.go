package tor

import (
	"crypto/sha1"
	"errors"
	"sort"

	"github.com/jech/storrent/hash"
	"github.com/jech/storrent/peer"
)

func metadataPeers(t *Torrent, count int) []*peer.Peer {
	if len(t.peers) == 0 {
		return nil
	}
	pn := t.rand.Perm(len(t.peers))
	var peers []*peer.Peer
	for n := range pn {
		if t.peers[n].CanMetadata() {
			peers = append(peers, t.peers[n])
			if len(peers) >= count {
				break
			}
		}
	}
	return peers
}

func metadataVote(t *Torrent, size uint32) error {
	if t.infoComplete != 0 {
		return errors.New("metadata complete")
	}

	if size <= 0 || size > 128*1024*1024 {
		return errors.New("bad size")
	}
	if t.infoSizeVotes == nil {
		t.infoSizeVotes = make(map[uint32]int)
	}
	t.infoSizeVotes[size]++
	return nil
}

func metadataGuess(t *Torrent) uint32 {
	if t.infoComplete != 0 {
		panic("Eek!")
	}
	if t.infoSizeVotes == nil {
		return 0
	}
	count := -1
	var size uint32
	for s, c := range t.infoSizeVotes {
		if c > count {
			count = c
			size = s
		}
	}
	return size
}

func resizeMetadata(t *Torrent, size uint32) error {
	if t.infoComplete != 0 {
		return errors.New("metadata complete")
	}

	if size <= 0 || size > 128*1024*1024 {
		return errors.New("bad size")
	}
	if len(t.Info) != int(size) {
		t.Info = make([]byte, size)
		t.infoBitmap = nil
		t.infoRequested = make([]uint8, (size+16*1024-1)/(16*1024))

	}
	return nil
}

func requestMetadata(t *Torrent, p *peer.Peer) error {
	if t.infoComplete != 0 {
		return errors.New("metadata complete")
	}
	guess := metadataGuess(t)
	if guess == 0 {
		return errors.New("unknown size")
	}
	if uint32(len(t.Info)) != guess {
		err := resizeMetadata(t, guess)
		if err != nil {
			return err
		}
	}

	var peers []*peer.Peer
	if p == nil {
		peers = metadataPeers(t, 8)
	} else {
		peers = []*peer.Peer{p}
	}

	cn := t.rand.Perm(len(t.infoRequested))
	sort.Slice(cn, func(i, j int) bool {
		if !t.infoBitmap.Get(cn[i]) && t.infoBitmap.Get(cn[j]) {
			return true
		}
		if t.infoBitmap.Get(cn[i]) && !t.infoBitmap.Get(cn[j]) {
			return false
		}
		return t.infoRequested[cn[i]] < t.infoRequested[cn[j]]
	})

	j := 0
	for _, p := range peers {
		if j >= len(cn) || t.infoBitmap.Get(cn[j]) {
			return nil
		}

		err := maybeWritePeer(p, peer.PeerGetMetadata{uint32(cn[j])})
		if err == nil {
			t.infoRequested[cn[j]]++
			j++
		}
	}
	return nil
}

func gotMetadata(t *Torrent, index, size uint32, data []byte) (bool, error) {
	if t.infoComplete != 0 {
		return false, errors.New("metadata complete")
	}
	if size != uint32(len(t.Info)) {
		return false, errors.New("inconsistent metadata size")
	}
	chunks := len(t.infoRequested)
	if int(index) > chunks {
		return false, errors.New("chunk beyond end of metadata")
	}
	if len(data) != 16*1024 &&
		int(index)*16*1024+len(data) != len(t.Info) {
		return false, errors.New("inconsistent metadata length")
	}
	if t.infoBitmap.Get(int(index)) {
		return false, nil
	}
	copy(t.Info[index*16*1024:], data)
	t.infoBitmap.Set(int(index))

	for i := 0; i < chunks; i++ {
		if !t.infoBitmap.Get(i) {
			return false, nil
		}
	}

	hsh := sha1.Sum(t.Info)
	h := hash.Hash(hsh[:])
	if !h.Equals(t.Hash) {
		t.Info = nil
		t.infoBitmap = nil
		t.infoRequested = nil
		return false, errors.New("hash mismatch")
	}
	err := t.MetadataComplete()
	if err != nil {
		t.Info = nil
		t.infoBitmap = nil
		t.infoRequested = nil
		return false, err
	}
	t.infoSizeVotes = nil
	t.infoBitmap = nil
	t.infoRequested = nil
	return true, nil
}
