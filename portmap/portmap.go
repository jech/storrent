// +build cgo,!nonatpmp

package portmap

import (
	"context"
	"flag"
	"log"
	"sync"
	"time"

	"storrent/config"
	"storrent/natpmp"
)

var Do bool

func init() {
	flag.BoolVar(&Do, "portmap", true, "perform port mapping")
}

func Map(ctx context.Context) error {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		err := domap(ctx, natpmp.TCP)
		if err != nil {
			log.Printf("Couldn't map TCP: %v", err)
		}
		wg.Done()
	}()
	go func() {
		err := domap(ctx, natpmp.UDP)
		if err != nil {
			log.Printf("Couldn't map UDP: %v", err)
		}
		wg.Done()
	}()
	wg.Wait()
	return nil
}

func domap(ctx context.Context, proto natpmp.Protocol) error {
	n, err := natpmp.New()
	if err != nil {
		return err
	}
	defer n.Close()

	protoname := "UDP"
	if proto == natpmp.TCP {
		protoname = "TCP"
	}

	mapped := false

	unmap := func() error {
		_, _, err := n.Map(context.Background(),
			proto, config.ProtocolPort,
			config.ExternalPort(proto == natpmp.TCP, false),
			0)
		return err
	}

	defer func() {
		if mapped {
			err := unmap()
			if err != nil {
				log.Printf("Couldn't unmap %v: %v",
					protoname, err)
			} else {
				log.Printf("Unmapped %v", protoname)
			}
		}
	}()

	for {
		begin := time.Now()
		lt, port1, err := n.Map(ctx, proto, config.ProtocolPort,
			config.ExternalPort(proto == natpmp.TCP, false),
			30*time.Minute)
		if err != nil {
			log.Printf("Couldn't map %v: %v", protoname, err)
			if mapped {
				unmap()
				mapped = false
			}
		} else {
			mapped = true
			log.Printf("Mapped %v: %v->%v, %v",
				protoname, config.ProtocolPort, port1, lt)
			config.SetExternalIPv4Port(port1, proto == natpmp.TCP)
		}
		lt /= 2
		lt -= time.Now().Sub(begin)
		if lt < time.Minute {
			lt = time.Minute
		}
		timer := time.NewTimer(lt)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}
