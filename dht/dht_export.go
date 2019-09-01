// +build cgo

package dht

import (
	crand "crypto/rand"
	"crypto/sha1"
	"errors"
	"net"
	"os"
	"syscall"
	"time"
	"unsafe"
)

/*
extern void dht_set_errno(int);
*/
import "C"

//export dht_callback
func dht_callback(
	closure unsafe.Pointer,
	event C.int,
	infoHash *C.uchar,
	data unsafe.Pointer,
	dataLen C.size_t) {
	hash := C.GoBytes(unsafe.Pointer(infoHash), 20)
	switch event {
	case 1: // DHT_EVENT_VALUES
		data := (*[4096]byte)(data)
		dataLen := int(dataLen)
		if globalEvents == nil || dataLen > 4096 || dataLen%6 != 0 {
			return
		}
		for i := 0; i < dataLen/6; i++ {
			ip := make([]byte, 4)
			copy(ip, data[6*i:6*i+4])
			port := uint16((*data)[6*i+4])*256 +
				uint16((*data)[6*i+5])
			globalEvents <- ValueEvent{hash, ip, port}
		}
	case 2: // DHT_EVENT_VALUES6
		data := (*[4096]byte)(data)
		dataLen := int(dataLen)
		if globalEvents == nil || dataLen > 4096 || dataLen%18 != 0 {
			return
		}
		for i := 0; i < dataLen/18; i++ {
			ip := make([]byte, 16)
			copy(ip, data[18*i:18*i+16])
			port := uint16((*data)[18*i+16])*256 +
				uint16((*data)[18*i+17])
			globalEvents <- ValueEvent{hash, ip, port}
		}
	}
}

//export dht_hash
func dht_hash(hash_return unsafe.Pointer, hash_size C.int,
	v1 unsafe.Pointer, len1 C.int,
	v2 unsafe.Pointer, len2 C.int,
	v3 unsafe.Pointer, len3 C.int) {
	h := sha1.New()
	if len1 > 0 {
		h.Write((*[4096]byte)(v1)[:len1])
	}
	if len2 > 0 {
		h.Write((*[4096]byte)(v2)[:len2])
	}
	if len3 > 0 {
		h.Write((*[4096]byte)(v3)[:len3])
	}
	sum := h.Sum(nil)
	r := (*[4096]byte)(hash_return)
	copy(r[0:hash_size], sum[0:hash_size])
}

//export dht_random_bytes
func dht_random_bytes(buf unsafe.Pointer, size C.size_t) C.int {
	n, _ := crand.Read((*[4096]byte)(buf)[:size])
	return C.int(n)
}

//export dht_send_callback
func dht_send_callback(buf unsafe.Pointer, size C.size_t,
	ip unsafe.Pointer, iplen C.size_t, port C.uint) C.int {
	data := C.GoBytes(buf, C.int(size))
	var addr net.UDPAddr
	var conn *net.UDPConn
	addr.IP = C.GoBytes(ip, C.int(iplen))
	addr.Port = int(port)
	conn = getConn(addr.IP.To4() == nil)
	if conn == nil {
		C.dht_set_errno(C.int(syscall.EAFNOSUPPORT))
		return -1
	}
	err := conn.SetWriteDeadline(time.Now().Add(time.Second))
	var n int
	if err == nil {
		n, err = conn.WriteToUDP(data, &addr)
	}
	if err != nil {
		var e syscall.Errno
		if !errors.As(err, &e) {
			if os.IsTimeout(err) {
				e = syscall.EAGAIN
			} else {
				e = syscall.EIO
			}
		}
		C.dht_set_errno(C.int(e))
		return -1
	}
	return C.int(n)
}
