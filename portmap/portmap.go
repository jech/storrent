package portmap

import (
	"context"
	"log"
	"net"
	"sync"
	"time"

	"github.com/jackpal/gateway"
	natpmp "github.com/jackpal/go-nat-pmp"

	"storrent/config"
)

var clientMu sync.Mutex
var gw net.IP
var client *natpmp.Client
var clientTime time.Time

func getClient() (*natpmp.Client, error) {
	clientMu.Lock()
	defer clientMu.Unlock()
	if time.Since(clientTime) < time.Minute {
		return client, nil
	}
	g, err := gateway.DiscoverGateway()
	if err != nil {
		clientTime = time.Time{}
		gw = nil
		client = nil
		return nil, err
	}

	if !g.Equal(gw) {
		gw = g
		client = natpmp.NewClient(gw)
	}
	clientTime = time.Now()

	return client, nil
}

func Map(ctx context.Context) error {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		domap(ctx, "tcp")
		wg.Done()
	}()
	go func() {
		domap(ctx, "udp")
		wg.Done()
	}()
	wg.Wait()
	return nil
}

func domap(ctx context.Context, proto string) {
	var client *natpmp.Client
	unmap := func() {
		if client != nil {
			res, err := client.AddPortMapping(
				proto, config.ProtocolPort,
				config.ExternalPort(proto == "tcp", false),
				0)
			if err != nil {
				log.Printf("NAT-PMP: %v", err)
			} else {
				log.Printf("Unmapped %v: %v->%v", proto,
				config.ProtocolPort, res.MappedExternalPort)
			}
		}
		client = nil
	}
	defer unmap()

	for ctx.Err() == nil {
		c, err := getClient()
		if err != nil {
			log.Printf("NAT-PMP: %v", err)
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
			30 * 60,
		)
		if err != nil {
			log.Printf("NAT-PMP: %v", err)
			unmap()
			time.Sleep(30 * time.Second)
			continue
		}

		log.Printf("Mapped %v: %v->%v, %v",
			proto, config.ProtocolPort,
			res.MappedExternalPort,
			time.Duration(res.PortMappingLifetimeInSeconds)*
				time.Second)

		seconds := res.PortMappingLifetimeInSeconds
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
