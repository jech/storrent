package protocol

import (
	"bytes"
	"errors"
	"io"
	"net"
	"time"

	"storrent/crypto"
	"storrent/hash"
)

type HandshakeResult struct {
	Hash, Id            hash.Hash
	Dht, Fast, Extended bool
}

var header = []byte{19,
	0x42, 0x69, 0x74, 0x54, 0x6f, 0x72, 0x72, 0x65, 0x6e, 0x74,
	0x20, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x63, 0x6f, 0x6c}

func handshake(infoHash hash.Hash, myid hash.Hash) []byte {
	header := []byte{19,
		0x42, 0x69, 0x74, 0x54, 0x6f, 0x72, 0x72, 0x65, 0x6e, 0x74,
		0x20, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x63, 0x6f, 0x6c}
	reserved := []byte{0, 0, 0, 0, 0, 0x10, 0, 0x05}
	hs := make([]byte, 0, 20+8+20+20)
	hs = append(hs, header...)
	hs = append(hs, reserved...)
	hs = append(hs, infoHash...)
	hs = append(hs, myid...)
	return hs
}

var ErrBadHandshake = errors.New("bad handshake")
var ErrUnknownTorrent = errors.New("unknown torrent")

func readMore(conn net.Conn, buf []byte, n int, m int) ([]byte, error) {
	if m < n {
		m = n
	}
	l := len(buf)
	if l >= n {
		return buf, nil
	}

	buf = append(buf, make([]byte, m - l)...)
	k, err := io.ReadAtLeast(conn, buf[l:], n - l)
	buf = buf[:l + k]
	return buf, err
}

func ClientHandshake(c net.Conn, cryptoHandshake bool,
	infoHash hash.Hash, myid hash.Hash,
	cryptoOptions *crypto.Options) (conn net.Conn,
	result HandshakeResult, init []byte, err error) {

	conn = c

	err = conn.SetDeadline(time.Now().Add(30 * time.Second))
	if err != nil {
		return
	}

	hshk := handshake(infoHash, myid)

	var buf []byte
	if cryptoHandshake {
		conn, buf, err = crypto.ClientHandshake(
			conn, infoHash, hshk, cryptoOptions)
		if err != nil {
			return
		}
	} else {
		_, err = conn.Write(hshk)
		if err != nil {
			return
		}
	}

	buf, err = readMore(conn, buf, 20+8+20+20, 0)
	if err != nil {
		return
	}

	if !bytes.Equal(buf[:20], header) {
		err = ErrBadHandshake
		return
	}

	buf = buf[20:]

	if buf[7]&0x01 != 0 {
		result.Dht = true
	}
	if buf[7]&0x04 != 0 {
		result.Fast = true
	}
	if buf[5]&0x10 != 0 {
		result.Extended = true
	}

	buf = buf[8:]

	if !bytes.Equal(buf[:20], infoHash) {
		err = errors.New("unexpected infoHash")
		return
	}
	result.Hash = make([]byte, 20)
	copy(result.Hash, buf)

	buf = buf[20:]

	result.Id = make([]byte, 20)
	copy(result.Id, buf)

	buf = buf[20:]

	if len(buf) > 0 {
		init = make([]byte, len(buf))
		copy(init, buf)
	}

	return
}

func checkHeader(buf []byte) bool {
	if len(buf) < len(header) {
		return false
	}

	for i, v := range header {
		if buf[i] != v {
			return false
		}
	}
	return true
}

func ServerHandshake(c net.Conn, hashes []hash.HashPair,
	cryptoOptions *crypto.Options) (conn net.Conn,
	result HandshakeResult, init []byte, err error) {
	conn = c
	err = conn.SetDeadline(time.Now().Add(90 * time.Second))
	if err != nil {
		return
	}
	buf := make([]byte, 20+8+20+20)
	var n int
	n, err = io.ReadAtLeast(conn, buf, len(header))
	if err != nil {
		return
	}

	buf = buf[:n]

	ok := checkHeader(buf)
	if ok && cryptoOptions.ForceCryptoHandshake {
		err = errors.New("plaintext handshake forbidden")
		return
	}

	var skey hash.Hash
	if !ok && cryptoOptions.AllowCryptoHandshake {
		skeys := make([][]byte, len(hashes))
		for i := range hashes {
			skeys[i] = hashes[i].First
		}
		var ia []byte
		conn, skey, ia, err =
			crypto.ServerHandshake(conn, buf, skeys, cryptoOptions)
		if err != nil {
			return
		}
		buf = ia
		buf, err = readMore(conn, buf, 20, 20+8+20+20)
		if err != nil {
			return
		}
		ok = checkHeader(buf)
	}

	if !ok {
		err = ErrBadHandshake
		return
	}

	buf = buf[20:]
	buf, err = readMore(conn, buf, 8 + 20, 8 + 20 + 20)
	if err != nil {
		return
	}

	if buf[7]&0x01 != 0 {
		result.Dht = true
	}
	if buf[7]&0x04 != 0 {
		result.Fast = true
	}
	if buf[5]&0x10 != 0 {
		result.Extended = true
	}
	buf = buf[8:]

	hsh := hash.Hash(make([]byte, 20))
	copy(hsh, buf)
	buf = buf[20:]

	if skey != nil && !skey.Equals(hsh) {
		err = errors.New("crypto hash mismatch")
		return
	}

	found := false
	var h hash.HashPair
	for _, h = range hashes {
		if hsh.Equals(h.First) {
			found = true
			break
		}
	}
	if !found {
		err = ErrUnknownTorrent
		return
	}

	result.Hash = hsh

	hndshk := handshake(result.Hash, h.Second)
	_, err = conn.Write(hndshk)
	if err != nil {
		return
	}

	buf, err = readMore(conn, buf, 20, 0)
	if err != nil {
		return
	}
	result.Id = make([]byte, 20)
	copy(result.Id, buf)
	buf = buf[20:]

	if len(buf) > 0 {
		init = make([]byte, len(buf))
		copy(init, buf)
	}

	return
}
