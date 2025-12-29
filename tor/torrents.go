package tor

import (
	"bytes"
	"github.com/jech/storrent/hash"
	"iter"
	"sync"
)

// torrents is the set of torrents that we are currently handling
var torrents sync.Map

// Get finds a torrent by hash.
func Get(hash hash.Hash) *Torrent {
	var h [20]byte
	copy(h[:], hash)
	v, ok := torrents.Load(h)
	if !ok {
		return nil
	}
	return v.(*Torrent)
}

// GetByName gets a torrent by name.  If behaves deterministically if
// multiple torrents have the same name.
func GetByName(name string) *Torrent {
	var torrent *Torrent
	for _, t := range All() {
		if t.Name == name {
			if torrent == nil || bytes.Compare(t.Hash, torrent.Hash) < 0 {
				torrent = t
			}
		}
	}
	return torrent
}

func add(torrent *Torrent) bool {
	var h [20]byte
	copy(h[:], torrent.Hash)
	_, exists := torrents.LoadOrStore(h, torrent)
	return !exists
}

func del(hash hash.Hash) {
	var h [20]byte
	copy(h[:], hash)
	torrents.Delete(h)
}

func All() iter.Seq2[[]byte, *Torrent] {
	return func(yield func([]byte, *Torrent) bool) {
		torrents.Range(func(k, v interface{}) bool {
			a := k.([20]byte)
			return yield(a[:], v.(*Torrent))
		})
	}
}

func count() int {
	count := 0
	for _, _ = range All() {
		count++
	}
	return count
}

func infoHashes(all bool) []hash.HashPair {
	var pairs []hash.HashPair
	for h, t := range All() {
		if all || !t.hasProxy() {
			pairs = append(pairs, hash.HashPair{h, t.MyId})
		}
	}
	return pairs
}
