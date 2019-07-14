package natpmp

import (
	"context"
	"errors"
	"runtime"
	"time"
	"unsafe"
)

/*
#cgo LDFLAGS: -lnatpmp
#include <stdlib.h>
#include <errno.h>
#define ENABLE_STRNATPMPERR
#include "natpmp.h"

#define NATPMP_ERR_SELECTERROR -666

struct mapping_response {
    uint16_t privateport;
    uint16_t mappedpublicport;
    uint32_t lifetime;
};

struct mapping_response response_mapping(natpmpresp_t *response) {
    struct mapping_response resp = {0};
    if(response->type != NATPMP_RESPTYPE_UDPPORTMAPPING &&
       response->type != NATPMP_RESPTYPE_TCPPORTMAPPING)
        return resp;
    resp.privateport = response->pnu.newportmapping.privateport;
    resp.mappedpublicport = response->pnu.newportmapping.mappedpublicport;
    resp.lifetime = response->pnu.newportmapping.lifetime;
    return resp;
}

int natpmp_wait(natpmp_t *natpmp) {
    int rc;
    struct timeval tv;
    fd_set fdset;
    rc = getnatpmprequesttimeout(natpmp, &tv);
    if(rc < 0)
        return rc;
    FD_ZERO(&fdset);
    FD_SET(natpmp->s, &fdset);
    rc = select(natpmp->s + 1, &fdset, NULL, NULL, &tv);
    if(rc < 0)
        return NATPMP_ERR_SELECTERROR;
    return rc;
}
*/
import "C"

type natpmpError C.int

func (e natpmpError) Error() string {
	if e == C.NATPMP_ERR_SELECTERROR {
		return "select() failed"
	}
	return C.GoString(C.strnatpmperr(C.int(e)))
}

type Natpmp struct {
	natpmp *C.natpmp_t
}

func New() (*Natpmp, error) {
	natpmp := &Natpmp{}
	natpmp.natpmp = (*C.natpmp_t)(C.malloc(C.sizeof_natpmp_t))
	var addr C.in_addr_t
	rc := C.initnatpmp(natpmp.natpmp, 0, addr)
	if rc < 0 {
		C.free(unsafe.Pointer(natpmp.natpmp))
		natpmp.natpmp = nil
		return nil, natpmpError(rc)
	}
	runtime.SetFinalizer(natpmp, (*Natpmp).Close)
	return natpmp, nil
}

func (natpmp *Natpmp) Close() error {
	rc := C.closenatpmp(natpmp.natpmp)
	C.free(unsafe.Pointer(natpmp.natpmp))
	natpmp.natpmp = nil
	runtime.SetFinalizer(natpmp, nil)
	if rc < 0 {
		return natpmpError(rc)
	}
	return nil
}

type Protocol C.int

const (
	TCP Protocol = C.NATPMP_PROTOCOL_TCP
	UDP Protocol = C.NATPMP_PROTOCOL_UDP
)

func (natpmp *Natpmp) Map(ctx context.Context, protocol Protocol, privateport int, publicport int, lifetime time.Duration) (lease time.Duration, port int, err error) {

	var rtype C.ushort
	if protocol == UDP {
		rtype = C.NATPMP_RESPTYPE_UDPPORTMAPPING
	} else if protocol == TCP {
		rtype = C.NATPMP_RESPTYPE_TCPPORTMAPPING
	} else {
		err = errors.New("unknown protocol")
		return
	}

	response := (*C.natpmpresp_t)(C.malloc(C.sizeof_natpmpresp_t))
	defer C.free(unsafe.Pointer(response))

	rc := C.sendnewportmappingrequest(natpmp.natpmp,
		C.int(protocol),
		C.uint16_t(privateport),
		C.uint16_t(publicport),
		C.uint32_t((lifetime+time.Second-1)/time.Second))
	if rc < 0 {
		err = natpmpError(rc)
		return
	}

	for {
		err = ctx.Err()
		if err != nil {
			return
		}

		rc = C.natpmp_wait(natpmp.natpmp)
		if rc < 0 {
			err = natpmpError(rc)
			return
		}

		err = ctx.Err()
		if err != nil {
			return
		}
		rc = C.readnatpmpresponseorretry(natpmp.natpmp, response)
		if rc != C.NATPMP_TRYAGAIN {
			break
		}
	}
	if rc < 0 {
		err = natpmpError(rc)
		return
	}

	if response._type != rtype {
		err = errors.New("unexpected response")
		return
	}
	resp := C.response_mapping(response)
	if int(resp.privateport) != privateport {
		err = errors.New("unexpected port")
		return
	}

	lease = time.Duration(resp.lifetime) * time.Second
	port = int(resp.mappedpublicport)
	return
}
