package tor

import (
	"sync"
	"storrent/hash"
)

var torrents sync.Map

func Get(hash hash.Hash) *Torrent {
	var h [20]byte
	copy(h[:], hash)
	v, ok := torrents.Load(h)
	if !ok {
		return nil
	}
	return v.(*Torrent)
}

func Add(hash hash.Hash, torrent *Torrent) bool {
	var h [20]byte
	copy(h[:], hash)
	_, exists := torrents.LoadOrStore(h, torrent)
	return !exists
}
func del(hash hash.Hash) {
	var h [20]byte
	copy(h[:], hash)
	torrents.Delete(h)
}

func Range(f func(hash.Hash, *Torrent) bool) {
	torrents.Range(func(k, v interface{}) bool {
		a := k.([20]byte)
		return f(a[:], v.(*Torrent))
	})
}

func count() int {
	count := 0
	Range(func(h hash.Hash, t *Torrent) bool {
		count++
		return true
	})
	return count
}

func infoHashes(all bool) []hash.HashPair {
	var pairs []hash.HashPair
	Range(func(h hash.Hash, t *Torrent) bool {
		if all || !t.hasProxy() {
			pairs = append(pairs, hash.HashPair{h, t.MyId})
		}
		return true
	})
	return pairs
}