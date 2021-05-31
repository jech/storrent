// Package portmap implements port mapping using NAT-PMP or uPNP.

package portmap

import (
	"context"
	"errors"
	"log"
	"net"
	"sync"
	"time"

	ig1 "github.com/huin/goupnp/dcps/internetgateway1"
	"github.com/jackpal/gateway"
	natpmp "github.com/jackpal/go-nat-pmp"

	"github.com/jech/storrent/config"
)

const (
	NATPMP = 1
	UPNP   = 2
	All    = NATPMP | UPNP
)

type portmapClient interface {
	AddPortMapping(protocol string, port, externalPort int, lifetime int) (int, int, error)
}

// natpmpClient implements portmapClient for NAT-PMP.
type natpmpClient natpmp.Client

// newNatpmpClient attempts to contact the NAT-PMP client on the default
// gateway and returns a natpmpClient structure if successful.
func newNatpmpClient() (*natpmpClient, error) {
	g, err := gateway.DiscoverGateway()
	if err != nil {
		return nil, err
	}

	c := natpmp.NewClient(g)

	// NewClient always succeeds, verify that the gateway actually
	// supports NAT-PMP.
	_, err = c.GetExternalAddress()
	if err != nil {
		return nil, err
	}

	return (*natpmpClient)(c), nil
}

// AddPortMapping maps a port for the given lifetime.  It returns the
// allocated external port, which might be different from the port
// requested, and a lifetime in seconds.
func (c *natpmpClient) AddPortMapping(protocol string, port, externalPort int, lifetime int) (int, int, error) {
	r, err := (*natpmp.Client)(c).AddPortMapping(protocol, port, externalPort, lifetime)
	if err != nil {
		return 0, 0, err
	}
	return int(r.MappedExternalPort), int(r.PortMappingLifetimeInSeconds), nil
}

// upnpClient implements portmapClient for uPNP.
type upnpClient ig1.WANIPConnection1

// newUpnpClient attempts to discover a WAN IP Connection client on the
// local network.  If more than one are found, it returns an arbitrary one.
func newUpnpClient() (*upnpClient, error) {
	clients, errs, err := ig1.NewWANIPConnection1Clients()
	if err != nil {
		return nil, err
	}
	if len(clients) == 0 {
		if len(errs) > 0 {
			return nil, errs[0]
		}
		return nil, errors.New("no UPNP gateways found")
	}

	return (*upnpClient)(clients[0]), nil
}

// getMyIPv4 returns the local IPv4 address used by the default route.
func getMyIPv4() (net.IP, error) {
	conn, err := net.Dial("udp", "8.8.8.8:53")
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	localAddr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok {
		return nil, errors.New("unexpected type for local address")
	}

	return localAddr.IP, nil
}

// AddPortMapping attempts to create a mapping for the given port.  If
// successful, it returns the allocated external port, which might be
// different from the requested port if the latter was alredy allocated by
// a different host, and a lifetime in seconds.
func (c *upnpClient) AddPortMapping(protocol string, port, externalPort int, lifetime int) (int, int, error) {
	var prot string
	switch protocol {
	case "tcp":
		prot = "TCP"
	case "udp":
		prot = "UDP"
	default:
		return 0, 0, errors.New("unknown protocol")
	}

	myip, err := getMyIPv4()
	if err != nil {
		return 0, 0, err
	}

	ipc := (*ig1.WANIPConnection1)(c)

	// Find a free port
	ep := externalPort
	ok := false
	for ep < 65535 {
		p, c, e, d, l, err :=
			ipc.GetSpecificPortMappingEntry("", uint16(ep), prot)
		if err != nil || e == false || l <= 0 {
			ok = true
			break
		}
		a := net.ParseIP(c)
		if a.Equal(myip) && int(p) == port && d == "STorrent" {
			ok = true
			break
		}
		if lifetime == 0 {
			return 0, 0, errors.New("mapping not found")
		}
		ep++
	}

	if !ok {
		return 0, 0, errors.New("couldn't find free port")
	}

	if lifetime > 0 {
		err = ipc.AddPortMapping(
			"", uint16(ep), prot,
			uint16(port), myip.String(), true,
			"STorrent", uint32(lifetime),
		)
		if err != nil {
			return 0, 0, err
		}
		return ep, lifetime, nil
	}

	err = ipc.DeletePortMapping("", uint16(ep), prot)
	if err != nil {
		return 0, 0, err
	}
	return ep, 0, nil
}

// newClient attempts to contact a NAT-PMP gateway; if that fails, it
// attempts to discover a uPNP gateway.
func newClient(kind int) (portmapClient, error) {
	var err error
	if (kind & NATPMP) != 0 {
		c, err1 := newNatpmpClient()
		if err1 == nil {
			return c, nil
		}
		err = err1
	}

	if (kind & UPNP) != 0 {
		c, err1 := newUpnpClient()
		if err1 == nil {
			return c, nil
		}
		if err == nil {
			err = err1
		} else {
			err = errors.New(err.Error() + " and " + err1.Error())
		}
	}

	if err == nil {
		err = errors.New("no portmapping protocol found")
	}

	return nil, err
}

var clientMu sync.Mutex
var client portmapClient

// getClient is a memoised version of newClient.
func getClient(kind int) (portmapClient, error) {
	clientMu.Lock()
	defer clientMu.Unlock()

	if client != nil {
		return client, nil
	}

	c, err := newClient(kind)
	if err != nil {
		return nil, err
	}

	client = c
	return client, nil
}

// failClient resets the cache set by getClient.
func failClient() {
	clientMu.Lock()
	defer clientMu.Unlock()
	client = nil
}

// Map runs a portmapping loop for both TCP and UDP.  The kind parameter
// indicates the portmapping protocols to attempt.
func Map(ctx context.Context, kind int) error {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		domap(ctx, "tcp", kind)
		wg.Done()
	}()
	go func() {
		domap(ctx, "udp", kind)
		wg.Done()
	}()
	wg.Wait()
	return nil
}

func domap(ctx context.Context, proto string, kind int) {
	var client portmapClient
	unmap := func() {
		if client != nil {
			ep, _, err := client.AddPortMapping(
				proto, config.ProtocolPort,
				config.ExternalPort(proto == "tcp", false),
				0)
			if err != nil {
				log.Printf("Portmap: %v", err)
			} else {
				log.Printf("Unmapped %v: %v->%v",
					proto, config.ProtocolPort, ep,
				)
			}
		}
		config.SetExternalIPv4Port(config.ProtocolPort, proto == "tcp")
		client = nil
	}

	defer unmap()

	sleep := func(d time.Duration) error {
		timer := time.NewTimer(d)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
			return nil
		}
	}

	for {
		c, err := getClient(kind)
		if err != nil {
			log.Printf("Portmap: %v", err)
			unmap()
			err = sleep(30 * time.Second)
			if err != nil {
				return
			}
			continue
		}
		if c != client {
			unmap()
			client = c
		}

		ep, lifetime, err := client.AddPortMapping(
			proto, config.ProtocolPort,
			config.ExternalPort(proto == "tcp", false),
			30*60,
		)
		if err != nil {
			log.Printf("Portmap: %v", err)
			unmap()
			failClient()
			client = nil
			continue
		}

		config.SetExternalIPv4Port(int(ep), proto == "tcp")

		log.Printf("Mapped %v: %v->%v, %v",
			proto, config.ProtocolPort,
			ep, time.Duration(lifetime)*time.Second)

		if lifetime < 30 {
			lifetime = 30
		}
		err = sleep(time.Duration(lifetime) * time.Second * 2 / 3)
		if err != nil {
			return
		}
	}
}
