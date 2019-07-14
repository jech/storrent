package crypto

import (
	"bytes"
	crand "crypto/rand"
	"crypto/rc4"
	"crypto/sha1"
	"errors"
	"io"
	"math/big"
	"net"
)

type Options struct {
	AllowCryptoHandshake  bool
	PreferCryptoHandshake bool
	ForceCryptoHandshake  bool
	AllowEncryption       bool
	PreferEncryption      bool
	ForceEncryption       bool
}

var OptionsMap = map[int]*Options{
	0: &Options{},
	1: &Options{
		AllowCryptoHandshake: true,
		AllowEncryption:      true,
	},
	2: &Options{
		AllowCryptoHandshake:  true,
		PreferCryptoHandshake: true,
		AllowEncryption:       true,
	},
	3: &Options{
		AllowCryptoHandshake:  true,
		PreferCryptoHandshake: true,
		ForceCryptoHandshake:  true,
		AllowEncryption:       true,
	},
	4: &Options{
		AllowCryptoHandshake:  true,
		PreferCryptoHandshake: true,
		ForceCryptoHandshake:  true,
		AllowEncryption:       true,
		PreferEncryption:      true,
	},
	5: &Options{
		AllowCryptoHandshake:  true,
		PreferCryptoHandshake: true,
		ForceCryptoHandshake:  true,
		AllowEncryption:       true,
		PreferEncryption:      true,
		ForceEncryption:       true,
	},
}

var p, g, zero, one, p1, two big.Int
var vc []byte

func init() {
	p.SetString("FFFFFFFFFFFFFFFFC90FDAA22168C234C4C6628B80DC1CD129024E088A67CC74020BBEA63B139B22514A08798E3404DDEF9519B3CD3A431B302B0A6DF25F14374FE1356D6D51C245E485B576625E7EC6F44C42E9A63A36210000000000090563", 16)
	g.SetInt64(2)
	zero.SetInt64(0)
	one.SetInt64(1)
	p1.Sub(&p, &one)
	vc = make([]byte, 8)
}

func randomInt() (*big.Int, error) {
	buf := make([]byte, 20)
	_, err := crand.Read(buf)
	if err != nil {
		return nil, err
	}
	var x big.Int
	x.SetBytes(buf)
	return &x, nil
}

func tobytes(buf []byte, v *big.Int) {
	b := v.Bytes()
	if len(b) > len(buf) {
		panic("Integer too big")
	}
	for i := 0; i < len(buf)-len(b); i++ {
		buf[i] = 0
	}
	copy(buf[len(buf)-len(b):], b)
}

func hash(v ...[]byte) []byte {
	h := sha1.New()
	for _, w := range v {
		h.Write(w)
	}
	return h.Sum(nil)
}

func len2(v []byte) []byte {
	w := make([]byte, 2)
	w[0] = byte(len(v) >> 8)
	w[1] = byte(len(v) & 0xFF)
	return w
}

func eq(v1 []byte, v2 []byte) bool {
	if len(v1) != len(v2) {
		panic("eq: wrong length")
	}
	for i := range v1 {
		if v1[i] != v2[i] {
			return false
		}
	}
	return true
}

func xor(v1 []byte, v2 []byte) []byte {
	if len(v1) != len(v2) {
		panic("xor: wrong length")
	}
	v := make([]byte, len(v1))
	for i := range v1 {
		v[i] = v1[i] ^ v2[i]
	}
	return v
}

func synchronise(c io.Reader, w []byte, v []byte, n, m int) ([]byte, error) {
	if m < n {
		m = n
	}
	for {
		i := bytes.Index(w, v)
		if i >= 0 {
			return w[i+len(v):], nil
		}
		l := len(w)
		if l >= n {
			return w, errors.New("couldn't synchronise")
		}
		w = append(w, make([]byte, m-l)...)
		k, err := c.Read(w[l:])
		w = w[:l+k]
		if k == 0 && err != nil {
			return w, err
		}
	}
}

func writeAsync(conn net.Conn, v []byte) <-chan error {
	ch := make(chan error, 1)
	go func() {
		_, err := conn.Write(v)
		ch <- err
	}()
	return ch
}

func writeWithPad(conn net.Conn, v *big.Int) <-chan error {
	lbuf := make([]byte, 2)
	crand.Read(lbuf)
	lbuf[0] &= 1
	length := uint16(lbuf[0])<<8 | uint16(lbuf[1])
	buf := make([]byte, 768/8+int(length))
	tobytes(buf[:768/8], v)
	crand.Read(buf[768/8:])
	return writeAsync(conn, buf)
}

func readMore(conn net.Conn, buf []byte, n int, m int) ([]byte, error) {
	if m < n {
		m = n
	}
	l := len(buf)
	if l >= n {
		return buf, nil
	}

	buf = append(buf, make([]byte, m-l)...)
	_, err := io.ReadAtLeast(conn, buf[l:], n-l)
	return buf, err
}

func trivial(x *big.Int) bool {
	return x.Cmp(&zero) == 0 || x.Cmp(&one) == 0 || x.Cmp(&p1) == 0
}

func ClientHandshake(c net.Conn, skey []byte, ia []byte, options *Options) (conn net.Conn, buf []byte, err error) {

	conn = c

	if !options.AllowCryptoHandshake {
		err = errors.New("crypto handshake forbidden")
		return
	}

	xa, err := randomInt()
	if err != nil {
		return
	}

	var ya, yb, s big.Int
	ya.Exp(&g, xa, &p)
	ch := writeWithPad(conn, &ya)

	buf = make([]byte, 608)
	n, err := io.ReadAtLeast(conn, buf, 768/8)
	if err != nil {
		return
	}

	yb.SetBytes(buf[:768/8])
	if trivial(&yb) {
		err = errors.New("peer returned trivial public key")
		return
	}
	s.Exp(&yb, xa, &p)
	buf = buf[768/8 : n]

	err = <-ch
	if err != nil {
		return
	}

	sb := make([]byte, 768/8)
	tobytes(sb, &s)
	enc, err := rc4.NewCipher(hash([]byte("keyA"), sb, skey))
	if err != nil {
		return
	}
	discard := make([]byte, 1024)
	enc.XORKeyStream(discard, discard)

	encrypt := func(v ...[]byte) []byte {
		n := 0
		for _, vv := range v {
			n += len(vv)
		}
		w := make([]byte, n)
		i := 0
		for _, vv := range v {
			enc.XORKeyStream(w[i:], vv)
			i += len(vv)
		}
		return w
	}

	dec, err := rc4.NewCipher(hash([]byte("keyB"), sb, skey))
	if err != nil {
		return
	}
	dec.XORKeyStream(discard, discard)
	decrypt := func(v []byte) []byte {
		w := make([]byte, len(v))
		dec.XORKeyStream(w, v)
		return w
	}

	wbuf := append(
		hash([]byte("req1"), sb),
		xor(hash([]byte("req2"), skey), hash([]byte("req3"), sb))...)

	cryptoProvide := make([]byte, 4)
	if !options.ForceEncryption {
		cryptoProvide[3] |= 1
	}
	if options.AllowEncryption {
		cryptoProvide[3] |= 2
	}
	if cryptoProvide[3] == 0 {
		err = errors.New("couldn't negotiate encryption")
		return
	}
	wbuf = append(wbuf,
		encrypt(vc, cryptoProvide, []byte{0, 0}, len2(ia), ia)...)

	ch = writeAsync(conn, wbuf)

	buf, err = synchronise(conn, buf, decrypt(vc),
		len(vc)+512, len(vc)+512+4+2)
	if err != nil {
		return
	}

	err = <-ch
	if err != nil {
		return
	}

	buf, err = readMore(conn, buf, 4+2, 0)
	if err != nil {
		return
	}

	stuff := decrypt(buf[:4+2])
	buf = buf[4+2:]

	cryptoSelect :=
		uint32(stuff[0])<<24 |
			uint32(stuff[1])<<16 |
			uint32(stuff[2])<<8 |
			uint32(stuff[3])
	lenPadD := uint16(stuff[4])<<8 | uint16(stuff[5])

	if lenPadD > 0 {
		buf, err = readMore(conn, buf, int(lenPadD), 0)
		if err != nil {
			return
		}
		decrypt(buf[:lenPadD])
		buf = buf[lenPadD:]
	}

	switch cryptoSelect {
	case 1:
		if options.ForceEncryption {
			err = errors.New("peer didn't negotiate encryption")
		}
		buf2 := make([]byte, len(buf))
		copy(buf2, buf)
		buf = buf2
		return
	case 2:
		if !options.AllowEncryption {
			err = errors.New("peer did negotiate encryption")
			return
		}
		buf = decrypt(buf)
		conn = &Conn{conn: conn, enc: enc, dec: dec}
		return
	default:
		err = errors.New("bad value for cryptoSelect")
		return
	}
}

func ServerHandshake(c net.Conn, head []byte, skeys [][]byte, options *Options) (conn net.Conn, skey []byte, ia []byte, err error) {
	conn = c

	if !options.AllowCryptoHandshake {
		err = errors.New("crypto handshake forbidden")
		return
	}

	if len(head) > 768/8 {
		err = errors.New("unexpected length for encrypted header")
		return
	}

	var ya, yb, s big.Int

	buf := make([]byte, 608)

	copy(buf, head)
	n := len(head)
	if n < 768/8 {
		var m int
		m, err = io.ReadAtLeast(conn, buf[n:], 768/8-n)
		if err != nil {
			return
		}
		n += m
	}

	ya.SetBytes(buf[:768/8])
	if trivial(&ya) {
		err = errors.New("peer sent trivial public key")
		return
	}
	buf = buf[768/8 : n]

	xb, err := randomInt()
	if err != nil {
		return nil, nil, nil, err
	}

	yb.Exp(&g, xb, &p)
	s.Exp(&ya, xb, &p)

	ch := writeWithPad(conn, &yb)

	sb := make([]byte, 768/8)
	tobytes(sb, &s)

	buf, err = synchronise(conn, buf, hash([]byte("req1"), sb),
		4+768/8+512, 1500)
	if err != nil {
		return
	}
	err = <-ch
	if err != nil {
		return
	}

	buf, err = readMore(conn, buf, 20, 1024)
	if err != nil {
		return
	}

	req23 := buf[:20]
	buf = buf[20:]
	req2 := xor(req23, hash([]byte("req3"), sb))
	for _, sk := range skeys {
		if eq(req2, hash([]byte("req2"), sk)) {
			skey = sk
			break
		}
	}
	if skey == nil {
		err = errors.New("unknown torrent")
		return
	}

	enc, err := rc4.NewCipher(hash([]byte("keyB"), sb, skey))
	if err != nil {
		return
	}
	discard := make([]byte, 1024)
	enc.XORKeyStream(discard, discard)

	encrypt := func(v ...[]byte) []byte {
		n := 0
		for _, vv := range v {
			n += len(vv)
		}
		w := make([]byte, n)
		i := 0
		for _, vv := range v {
			enc.XORKeyStream(w[i:], vv)
			i += len(vv)
		}
		return w
	}

	dec, err := rc4.NewCipher(hash([]byte("keyA"), sb, skey))
	if err != nil {
		return
	}
	dec.XORKeyStream(discard, discard)
	decrypt := func(v []byte) []byte {
		w := make([]byte, len(v))
		dec.XORKeyStream(w, v)
		return w
	}

	buf, err = readMore(conn, buf, len(vc)+4+2, len(vc)+4+2+2+512)
	if err != nil {
		return
	}

	estuff := buf[:len(vc)+4+2]
	buf = buf[len(vc)+4+2:]

	stuff := decrypt(estuff)
	theirvc := stuff[:len(vc)]
	if !eq(theirvc, vc) {
		err = errors.New("bad VC")
		return
	}
	cryptoProvide :=
		uint32(stuff[len(vc)+0])<<24 |
			uint32(stuff[len(vc)+1])<<16 |
			uint32(stuff[len(vc)+2])<<8 |
			uint32(stuff[len(vc)+3])
	lenPadC := uint16(stuff[len(vc)+4])<<8 | uint16(stuff[len(vc)+5])

	if cryptoProvide&3 == 0 {
		err = errors.New("peer didn't provide a known crypto algorithm")
		return
	}

	if lenPadC > 0 {
		buf, err = readMore(conn, buf, int(lenPadC), 2+512)
		if err != nil {
			return
		}
		padC := buf[:lenPadC]
		buf = buf[lenPadC:]
		decrypt(padC)
	}

	buf, err = readMore(conn, buf, 2, 1024)
	if err != nil {
		return
	}
	eLenIa := buf[:2]
	buf = buf[2:]
	lenIaBuf := decrypt(eLenIa)

	lenIa := uint16(lenIaBuf[0])<<8 | uint16(lenIaBuf[1])
	buf, err = readMore(conn, buf, int(lenIa), 0)
	if err != nil {
		return
	}
	eia := buf[:lenIa]
	buf = buf[lenIa:]
	ia = decrypt(eia)

	if len(buf) > 0 {
		err = errors.New("extra data after handshake")
		println()
		return
	}

	cryptoSelect := make([]byte, 4)

	if (cryptoProvide&2) != 0 &&
		options.AllowEncryption && options.PreferEncryption {
		cryptoSelect[3] = 2
	} else if (cryptoProvide&1) != 0 &&
		!options.ForceEncryption && !options.PreferEncryption {
		cryptoSelect[3] = 1
	} else if (cryptoProvide&2) != 0 && options.AllowEncryption {
		cryptoSelect[3] = 2
	} else if (cryptoProvide&1) != 0 && !options.ForceEncryption {
		cryptoSelect[3] = 1
	}
	if cryptoSelect[3] == 0 {
		err = errors.New("couldn't negotiate encryption")
		return
	}

	lenPadD := make([]byte, 2)

	_, err = conn.Write(encrypt(vc, cryptoSelect, lenPadD))
	if err != nil {
		return
	}

	switch cryptoSelect[3] {
	case 1:
		return
	case 2:
		conn = &Conn{conn: conn, enc: enc, dec: dec}
		return
	default:
		err = errors.New("Unexpected value for cryptoSelect")
		return
	}
}
