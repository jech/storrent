package protocol

import (
	"bufio"
	"bytes"
	crand "crypto/rand"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"os"
	"reflect"
	"sync"
	"testing"

	"storrent/crypto"
	"storrent/hash"
	"storrent/pex"
)

type mtest struct {
	m Message
	v string
}

func randomHash() hash.Hash {
	h := make([]byte, 20)
	crand.Read(h)
	return h
}

func testHandshake(t *testing.T, chand bool, opts *crypto.Options) {
	h := randomHash()
	sid := randomHash()
	cid := randomHash()
	hashes := []hash.HashPair{hash.HashPair{h, sid}}
	c, s := net.Pipe()
	var ss net.Conn
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		var err error
		ss, _, _, err = ServerHandshake(s, hashes, opts)
		if err != nil {
			t.Errorf("ServerHandshake: %v", err)
			return
		}
		ss.Close()
	}()
	cc, _, _, err := ClientHandshake(c, chand, h, cid, opts)
	if err != nil {
		c.Close()
		t.Fatalf("ClientHandshake: %v", err)
	}
	defer cc.Close()
	wg.Wait()
}

func TestHandshake(t *testing.T) {
	testHandshake(t, false, &crypto.Options{})
}

func TestCryptoHandshake(t *testing.T) {
	testHandshake(t, true, &crypto.Options{AllowCryptoHandshake: true})
}

var messages = []mtest{
	mtest{KeepAlive{}, "\x00\x00\x00\x00"},
	mtest{Choke{}, "\x00\x00\x00\x01\x00"},
	mtest{Unchoke{}, ""},
	mtest{Interested{}, ""},
	mtest{NotInterested{}, ""},
	mtest{Have{42}, ""},
	mtest{Bitfield{[]byte{0xFF, 0xFE, 0x10}},
		"\x00\x00\x00\x04\x05\xff\xfe\x10"},
	mtest{Request{42, 32768, 16384}, ""},
	mtest{Piece{42, 32768, make([]byte, 16384)}, ""},
	mtest{Cancel{42, 32768, 16384}, ""},
	mtest{Port{1234}, "\x00\x00\x00\x03\t\x04\xd2"},
	mtest{SuggestPiece{42}, ""},
	mtest{RejectRequest{42, 32768, 16384}, ""},
	mtest{AllowedFast{42}, ""},
	mtest{HaveAll{}, ""},
	mtest{HaveNone{}, ""},
	mtest{Extended0{"toto", 1234, 256,
		net.ParseIP("1.2.3.4").To4(), net.ParseIP("2001::1"), 1024,
		map[string]uint8{"ut_pex": ExtPex}, false, true}, ""},
	mtest{ExtendedMetadata{ExtMetadata, 1, 2, 64 * 1024,
		make([]byte, 16384)}, ""},
	mtest{ExtendedPex{ExtPex, []pex.Peer{
		pex.Peer{IP: net.ParseIP("1.2.3.4").To4(), Port: 1234,
			Flags: pex.Encrypt | pex.Outgoing},
		pex.Peer{IP: net.ParseIP("2001::1"), Port: 5678,
			Flags: pex.UploadOnly}},
		nil}, "\x00\x00\x00I\x14\x01" +
		"d5:added6:\x01\x02\x03\x04\x04\xd2" +
		"7:added.f1:\x11" +
		"6:added618:\x20\x01\x00\x00\x00\x00\x00\x00" +
		"\x00\x00\x00\x00\x00\x00\x00\x01\x16." +
		"8:added6.f1:\x02e",
	},
	mtest{ExtendedPex{ExtPex, nil, []pex.Peer{
		pex.Peer{IP: net.ParseIP("1.2.3.4").To4(), Port: 1234},
		pex.Peer{IP: net.ParseIP("5.6.7.8").To4(), Port: 4321},
		pex.Peer{IP: net.ParseIP("2001::1"), Port: 5678},
		pex.Peer{IP: net.ParseIP("2001::2"), Port: 2345}}}, ""},
	mtest{ExtendedDontHave{ExtDontHave, 1}, ""},
}

func TestWriter(t *testing.T) {
	for _, m := range messages {
		t.Run(fmt.Sprintf("%T", m.m), func(t *testing.T) {
			p1, p2 := net.Pipe()
			w := bufio.NewWriter(p1)
			b := make([]byte, 32*1024)
			var wg sync.WaitGroup
			wg.Add(1)
			go func() {
				defer wg.Done()
				n := 0
				for n < len(b) {
					m, _ := p2.Read(b[n:])
					if m == 0 {
						break
					}
					n += m
				}
				b = b[:n]
				p2.Close()
			}()
			err := Write(w, m.m, log.New(ioutil.Discard, "", 0))
			if err != nil {
				t.Error(err)
			}
			err = w.Flush()
			if err != nil {
				t.Error(err)
			}
			p1.Close()
			wg.Wait()
			if m.v != "" {
				if string(b) != m.v {
					t.Errorf("Got %#v, expected %#v",
						string(b), m.v)
				}
			}
		})
	}
}

func TestReader(t *testing.T) {
	for _, m := range messages {
		if m.v == "" {
			continue
		}
		t.Run(fmt.Sprintf("%T", m.m), func(t *testing.T) {
			r := bufio.NewReader(bytes.NewReader([]byte(m.v)))
			mm, err := Read(r, nil)
			if err != nil {
				t.Fatal(err)
				return
			}
			n, err := r.Read(make([]byte, 32))
			if n != 0 || err != io.EOF {
				t.Errorf("%v bytes remaining (%v)", n, m.v)
			}
			if !reflect.DeepEqual(mm, m.m) {
				t.Errorf("Got %#v, expected %#v", mm, m.m)
			}
		})
	}
}

func getLogger() *log.Logger {
	if testing.Verbose() {
		return log.New(os.Stdout, "", log.LstdFlags)
	}
	return nil
}

func TestRoundtrip(t *testing.T) {
	r, w := net.Pipe()
	reader := make(chan Message, 1024)
	readerDone := make(chan struct{})
	logger := getLogger()
	go Reader(r, nil, logger, reader, readerDone)
	writer := make(chan Message, 1024)
	writerDone := make(chan struct{})
	go Writer(w, logger, writer, writerDone)
	for _, m := range messages {
		select {
		case writer <- m.m:
		case <-writerDone:
			t.Fatal("Writer quit prematurely")
		}
		mm := <-reader
		if !reflect.DeepEqual(m.m, mm) {
			var e string
			me, ok := mm.(Error)
			if ok {
				e = fmt.Sprintf(" (%v)", me.Error)
			}
			t.Errorf("Got %#v%v, expected %#v", mm, e, m.m)
		}
	}
	r.Close()
	close(readerDone)
	mm, ok := <-reader
	if ok {
		_, ok := mm.(Error)
		if !ok {
			t.Errorf("Got %v, expected EOF", mm)
		}
	}
	close(writer)
}

func benchmarkMessage(m Message, bytes int64, b *testing.B) {
	if bytes > 0 {
		b.SetBytes(bytes)
	}
	r, w := net.Pipe()
	reader := make(chan Message, 1024)
	readerDone := make(chan struct{})
	logger := getLogger()
	go Reader(r, nil, logger, reader, readerDone)
	writer := make(chan Message, 1024)
	writerDone := make(chan struct{})
	go Writer(w, logger, writer, writerDone)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		select {
		case writer <- m:
		case <-writerDone:
			b.Errorf("Writer quit prematurely")
		}
		_, ok := <-reader
		if !ok {
			b.Errorf("Reader quit prematurely")
		}
	}
	r.Close()
	close(readerDone)
	close(writer)
}

func BenchmarkRequest(b *testing.B) {
	benchmarkMessage(Request{42, 32768, 16384}, 0, b)
}

func BenchmarkData(b *testing.B) {
	benchmarkMessage(Piece{42, 32768, make([]byte, 16384)}, 16384, b)
}

