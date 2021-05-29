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

type portmapResult struct {
	externalPort uint16
	lifetime     uint32
}

type portmapClient interface {
	AddPortMapping(protocol string, port, externalPort int, lifetime int) (portmapResult, error)
}

type natpmpClient natpmp.Client

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

func (c *natpmpClient) AddPortMapping(protocol string, port, externalPort int, lifetime int) (portmapResult, error) {
	r, err := (*natpmp.Client)(c).AddPortMapping(protocol, port, externalPort, lifetime)
	if err != nil {
		return portmapResult{}, err
	}
	result := portmapResult{
		externalPort: r.MappedExternalPort,
		lifetime:     r.PortMappingLifetimeInSeconds,
	}
	return result, nil
}

type upnpClient ig1.WANIPConnection1

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

func getMyIP() (net.IP, error) {
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

func (c *upnpClient) AddPortMapping(protocol string, port, externalPort int, lifetime int) (portmapResult, error) {
	var prot string
	switch protocol {
	case "tcp":
		prot = "TCP"
	case "udp":
		prot = "UDP"
	default:
		return portmapResult{}, errors.New("unknown protocol")
	}

	if lifetime > 0 {
		ip, err := getMyIP()
		if err != nil {
			return portmapResult{}, err
		}

		err = (*ig1.WANIPConnection1)(c).AddPortMapping(
			"", uint16(externalPort), prot,
			uint16(port), ip.String(), true,
			"STorrent", uint32(lifetime),
		)
		if err != nil {
			return portmapResult{}, err
		}
		return portmapResult{
			externalPort: uint16(externalPort),
			lifetime:     uint32(lifetime),
		}, nil
	}

	err := (*ig1.WANIPConnection1)(c).DeletePortMapping(
		"", uint16(externalPort), prot,
	)
	if err != nil {
		return portmapResult{}, err
	}
	return portmapResult{
		externalPort: uint16(externalPort),
		lifetime:     0,
	}, nil
}

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

func failClient() {
	clientMu.Lock()
	defer clientMu.Unlock()
	client = nil
}

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
			res, err := client.AddPortMapping(
				proto, config.ProtocolPort,
				config.ExternalPort(proto == "tcp", false),
				0)
			if err != nil {
				log.Printf("Portmap: %v", err)
			} else {
				log.Printf("Unmapped %v: %v->%v", proto,
					config.ProtocolPort, res.externalPort)
			}
		}
		client = nil
	}
	defer unmap()

	for ctx.Err() == nil {
		c, err := getClient(kind)
		if err != nil {
			log.Printf("Portmap: %v", err)
			unmap()
			time.Sleep(30 * time.Second)
			continue
		}
		if c != client {
			unmap()
			client = c
		}

		res, err := client.AddPortMapping(
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

		log.Printf("Mapped %v: %v->%v, %v",
			proto, config.ProtocolPort,
			res.externalPort,
			time.Duration(res.lifetime)*time.Second)

		seconds := res.lifetime
		if seconds < 30 {
			seconds = 30
		}
		timer := time.NewTimer(time.Duration(seconds) * time.Second *
			2 / 3)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
