// +build cgo

package dht

import (
	"context"
	"errors"
	"log"
	"math/rand"
	"net"
	"os"
	"sync"
	"time"
	"unsafe"
)

/*
#include <stdlib.h>
#include <errno.h>
#include <stdio.h>
#include <string.h>
#include <unistd.h>
#include <sys/types.h>
#include <sys/stat.h>
#include <fcntl.h>
#include <arpa/inet.h>
#include <sys/types.h>
#include <sys/socket.h>
#include "dht.h"

void
dht_set_errno(int e)
{
    errno = e;
}

static void
dht_set_debug(int debug)
{
    if(debug)
        dht_debug = stderr;
    else
        dht_debug = NULL;
}

int
dht_blacklisted(const struct sockaddr *sa, int salen)
{
    return 0;
}

extern void dht_callback();

static int
periodic(const void *buf, int buflen,
         const char *from, int fromlen, int fromport,
         time_t *tosleep)
{
    struct sockaddr *sa;
    struct sockaddr_in sin = {0};
    struct sockaddr_in6 sin6 = {0};
    int salen;

    if(fromlen == 0) {
        sa = NULL;
        salen = 0;
    } else if(fromlen == 4) {
        sin.sin_family = AF_INET;
        memcpy(&sin.sin_addr, from, 4);
        sin.sin_port = htons(fromport);
        sa = (struct sockaddr*)&sin;
        salen = sizeof(struct sockaddr_in);
    } else if(fromlen == 16) {
        sin6.sin6_family = AF_INET6;
        memcpy(&sin6.sin6_addr, from, 16);
        sin6.sin6_port = htons(fromport);
        sa = (struct sockaddr*)&sin6;
        salen = sizeof(struct sockaddr_in6);
    } else {
        errno = EINVAL;
        return -1;
    }

    return dht_periodic(buf, buflen, sa, salen, tosleep, dht_callback, NULL);
}

static int
ping_node(const unsigned char *to, int tolen, int toport)
{
    struct sockaddr *sa;
    struct sockaddr_in sin = {0};
    struct sockaddr_in6 sin6 = {0};
    int salen;

    if(tolen == 4) {
        sin.sin_family = AF_INET;
        memcpy(&sin.sin_addr, to, 4);
        sin.sin_port = htons(toport);
        sa = (struct sockaddr*)&sin;
        salen = sizeof(struct sockaddr_in);
    } else if(tolen == 16) {
        sin6.sin6_family = AF_INET6;
        memcpy(&sin6.sin6_addr, to, 16);
        sin6.sin6_port = htons(toport);
        sa = (struct sockaddr*)&sin6;
        salen = sizeof(struct sockaddr_in6);
    } else {
        errno = EINVAL;
        return -1;
    }

    return dht_ping_node(sa, salen);
}

static int
announce(const unsigned char *id, int ipv6, int port)
{
    return dht_search(id, port, ipv6 ? AF_INET6 : AF_INET, dht_callback, NULL);
}

extern int dht_send_callback(const void*, size_t, const void*, size_t,
                             unsigned int);

int
dht_sendto(int sockfd, const void *buf, int len, int flags,
           const struct sockaddr *to, int tolen)
{
    if(sockfd != 42) {
        errno = EINVAL;
        return -1;
    }
    if(to->sa_family == AF_INET) {
        const struct sockaddr_in *sin = (struct sockaddr_in*)to;
        return dht_send_callback(buf, len,
                                 (unsigned char*)&sin->sin_addr, 4,
                                 ntohs(sin->sin_port));
    } else if(to->sa_family == AF_INET6) {
        const struct sockaddr_in6 *sin6 = (struct sockaddr_in6*)to;
        return dht_send_callback(buf, len,
                                 (unsigned char*)&sin6->sin6_addr, 16,
                                 ntohs(sin6->sin6_port));
    } else {
        errno = EINVAL;
        return -1;
    }
}

static int
get_nodes(unsigned char **a_ret, int *a_count,
          unsigned char **a6_ret, int *a6_count)
{
    struct sockaddr_in *sins = NULL;
    struct sockaddr_in6 *sin6s = NULL;
    unsigned char *a = NULL, *a6 = NULL;
    int count, n, n6, rc;

    n = 1024;
    sins = malloc(n * sizeof(struct sockaddr_in));
    if(sins == NULL) {
        goto fail;
    }

    n6 = 1024;
    sin6s = malloc(n6 * sizeof(struct sockaddr_in6));
    if(sin6s == NULL) {
        goto fail;
    }

    rc = dht_get_nodes(sins, &n, sin6s, &n6);
    if(rc < 0) {
        return -1;
    }

    a = malloc(6 * n);
    if(a == NULL) {
        goto fail;
    }

    a6 = malloc(18 * n6);
    if(a == NULL) {
        goto fail;
    }

    for(int i = 0; i < n; i++) {
        memcpy(a + 6 * i, &sins[i].sin_addr, 4);
        memcpy(a + 6 * i + 4, &sins[i].sin_port, 2);
    }
    for(int i = 0; i < n6; i++) {
        memcpy(a6 + 18 * i, &sin6s[i].sin6_addr, 16);
        memcpy(a6 + 18 * i + 16, &sin6s[i].sin6_port, 2);
    }

    *a_ret = a;
    *a_count = n;
    *a6_ret = a6;
    *a6_count = n6;
    free(sins);
    free(sin6s);
    return n + n6;

fail:
    free(a);
    free(a6);
    free(sins);
    free(sin6s);
    return -1;
}


*/
import "C"

func Available() bool {
	return true
}

type Event interface {
}

type ValueEvent struct {
	Hash []byte
	IP   net.IP
	Port uint16
}

var globalEvents chan Event
var mu sync.Mutex

var connection struct {
	conn4 *net.UDPConn
	conn6 *net.UDPConn
}

func getConn(ipv6 bool) *net.UDPConn {
	var conn *net.UDPConn
	if ipv6 {
		conn = connection.conn6
	} else {
		conn = connection.conn4
	}
	return conn
}

func setConn(ipv6 bool, conn *net.UDPConn) {
	if ipv6 {
		if connection.conn6 != nil {
			connection.conn6.Close()
		}
		connection.conn6 = conn
	} else {
		if connection.conn4 != nil {
			connection.conn4.Close()
		}
		connection.conn4 = conn
	}
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

func DHT(ctx context.Context, myid []byte, port uint16) (<-chan Event, error) {
	if len(myid) != 20 {
		return nil, errors.New("invalid id")
	}
	mu.Lock()
	defer mu.Unlock()
	rc, err := C.dht_init(42, 42, (*C.uchar)(&myid[0]), nil)
	if rc < 0 {
		return nil, err
	}
	if globalEvents != nil {
		panic("DHT initialised twice")
	}
	globalEvents = make(chan Event, 32)
	go loop(ctx, false, myid, port)
	go loop(ctx, true, myid, port)
	return globalEvents, nil
}

var eTooBig = errors.New("packet too big")

func loop(ctx context.Context, ipv6 bool, myid []byte, port uint16) error {
	buf := make([]byte, 4096)
	var addrTime time.Time
	var addr net.UDPAddr
	var conn *net.UDPConn
	var tosleep C.time_t

	defer func() {
		mu.Lock()
		setConn(ipv6, nil)
		if globalEvents != nil {
			C.dht_uninit()
			close(globalEvents)
			globalEvents = nil
		}
		mu.Unlock()
	}()

	timeout := func(seconds int) error {
		var s time.Duration
		if seconds > 0 {
			s = time.Duration(seconds) * time.Second
		}
		return conn.SetDeadline(time.Now().Add(s +
			time.Duration(rand.Int63n(int64(time.Second)))))
	}

	for {
		if time.Since(addrTime) > 2*time.Minute {
			addrTime = time.Now()
			newaddr := net.UDPAddr{Port: int(port)}
			if ipv6 {
				ip := getIPv6()
				if ip != nil {
					newaddr = net.UDPAddr{
						IP:   ip,
						Port: int(port),
					}
				}
			}
			if newaddr.Port != addr.Port ||
				!newaddr.IP.Equal(addr.IP) {
				f := "udp4"
				if ipv6 {
					f = "udp6"
				}
				mu.Lock()
				setConn(ipv6, nil)
				mu.Unlock()
				addr = net.UDPAddr{}
				conn = nil

				c, err := net.ListenUDP(f, &newaddr)
				if err != nil {
					log.Printf("ListenUDP: %v", err)
					time.Sleep(time.Minute)
					continue
				}

				mu.Lock()
				setConn(ipv6, c)
				mu.Unlock()
				addr = newaddr
				conn = c
			}
			timeout(0)
		}

		timedout := false
		n, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			if os.IsTimeout(err) {
				timedout = true
			} else {
				log.Printf("DHT: %v", err)
				time.Sleep(100 * time.Millisecond)
				continue
			}
		}

		if err = ctx.Err(); err != nil {
			return err
		}

		timeout(0)

		var rc C.int
		if timedout {
			mu.Lock()
			rc, err = C.periodic(nil, 0, nil, 0, -1, &tosleep)
			mu.Unlock()
		} else if n > 4095 {
			rc, err = -1, eTooBig
		} else {
			buf[n] = 0
			ip := from.IP
			if ipv6 {
				ip = ip.To16()
			} else {
				ip = ip.To4()
			}
			if ip == nil {
				rc, err = -1, errors.New("no sender address")
			} else {
				mu.Lock()
				rc, err = C.periodic(unsafe.Pointer(&buf[0]),
					C.int(n),
					(*C.char)(unsafe.Pointer(&ip[0])),
					C.int(len(ip)),
					C.int(from.Port), &tosleep)
				mu.Unlock()
			}
		}
		if rc < 0 {
			log.Printf("DHT: %v", err)
			tosleep = 1
		}

		if tosleep > 4 {
			tosleep = 4
		}
		timeout(int(tosleep))
	}
}

func Announce(id []byte, ipv6 bool, port uint16) error {
	if len(id) != 20 {
		return errors.New("invalid id")
	}
	v6 := C.int(0)
	if ipv6 {
		v6 = C.int(1)
	}
	mu.Lock()
	rc, err := C.announce((*C.uchar)(&id[0]), v6, C.int(port))
	mu.Unlock()
	if rc < 0 {
		return err
	}
	return nil
}

func Ping(ip net.IP, port uint16) error {
	ip4 := ip.To4()
	if ip4 != nil {
		ip = ip4
	}
	mu.Lock()
	rc, err := C.ping_node((*C.uchar)(&ip[0]), C.int(len(ip)), C.int(port))
	mu.Unlock()
	if rc < 0 {
		return err
	}
	return nil
}

func Count() (good4 int, good6 int,
	dubious4 int, dubious6 int,
	incoming4 int, incoming6 int) {
	var g4, g6, d4, d6, i4, i6 C.int
	mu.Lock()
	defer mu.Unlock()
	C.dht_nodes(C.AF_INET, &g4, &d4, nil, &i4)
	C.dht_nodes(C.AF_INET6, &g6, &d6, nil, &i6)
	good4 = int(g4)
	good6 = int(g6)
	dubious4 = int(d4)
	dubious6 = int(d6)
	incoming4 = int(i4)
	incoming6 = int(i6)
	return
}

func GetNodes() ([]net.TCPAddr, error) {
	var aptr, a6ptr *C.uchar
	var aCount, a6Count C.int
	mu.Lock()
	rc, err := C.get_nodes(&aptr, &aCount, &a6ptr, &a6Count)
	mu.Unlock()
	if rc < 0 {
		return nil, err
	}

	defer func() {
		C.free(unsafe.Pointer(aptr))
		C.free(unsafe.Pointer(a6ptr))
	}()

	if aCount*6 > 4096 {
		aCount = 4096 / 6
	}
	a := (*[4096]byte)(unsafe.Pointer(aptr))[:aCount*6]

	if a6Count*18 > 4096 {
		a6Count = 4096 / 18
	}
	a6 := (*[4096]byte)(unsafe.Pointer(a6ptr))[:a6Count*18]

	var addrs []net.TCPAddr
	for i := 0; i < int(aCount); i++ {
		ip := make([]byte, 4)
		copy(ip, a[i*6:])
		port := int(a[i*6+4])<<8 + int(a[i*6+5])
		addrs = append(addrs, net.TCPAddr{IP: ip, Port: port})
	}
	for i := 0; i < int(a6Count); i++ {
		ip := make([]byte, 16)
		copy(ip, a6[i*18:])
		port := int(a6[i*18+16])<<8 + int(a6[i*18+17])
		addrs = append(addrs, net.TCPAddr{IP: ip, Port: port})
	}
	return addrs, nil
}
