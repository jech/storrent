// STorrent is a BitTorrent implementation that is optimised for streaming
// media.
package main

import (
	"context"
	crand "crypto/rand"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"syscall"
	"time"

	"github.com/jech/portmap"

	"github.com/jech/storrent/config"
	"github.com/jech/storrent/crypto"
	"github.com/jech/storrent/fuse"
	thttp "github.com/jech/storrent/http"
	"github.com/jech/storrent/peer"
	"github.com/jech/storrent/physmem"
	"github.com/jech/storrent/rundht"
	"github.com/jech/storrent/tor"
)

func main() {
	var proxyURL, mountpoint string
	var cpuprofile, memprofile, mutexprofile string
	var doPortmap string

	mem, err := physmem.Total()
	if err != nil {
		log.Printf("Couldn't determine physical memory: %v", err)
		mem = 2 * 1024 * 1024 * 1024
	}

	fmt.Fprintf(os.Stderr, "STorrent 0.0 by Juliusz Chroboczek\n")

	rand.Seed(time.Now().UTC().UnixNano())

	flag.IntVar(&config.ProtocolPort, "port", 23222,
		"TCP and UDP `port` used for BitTorrent and DHT traffic")
	flag.StringVar(&config.HTTPAddr, "http", "[::1]:8088",
		"Web server `address`")
	flag.Int64Var(&config.MemoryMark, "mem", mem/2,
		"Target memory usage in `bytes`")
	flag.StringVar(&cpuprofile, "cpuprofile", "",
		"CPU profile `filename`")
	flag.StringVar(&memprofile, "memprofile", "",
		"Memory profile `filename`")
	flag.StringVar(&mutexprofile, "mutexprofile", "",
		"Mutex profile `filename`")
	flag.StringVar(&proxyURL, "proxy", "",
		"`URL` of a proxy to use for BitTorrent and tracker traffic.\n"+
			"For tor, use \"socks5://127.0.0.1:9050\" and disable the DHT")
	flag.StringVar(&mountpoint, "mountpoint", "",
		"FUSE `mountpoint`")
	flag.BoolVar(&config.DefaultDhtPassive, "dht-passive", false,
		"Don't perform DHT announces")
	flag.BoolVar(&config.DefaultUseTrackers, "use-trackers", false,
		"Use trackers (if available)")
	flag.BoolVar(&config.DefaultUseWebseeds, "use-webseeds", false,
		"Use webseeds (if available)")
	flag.Float64Var(&config.PrefetchRate, "prefetch-rate", 768*1024,
		"Prefetch `rate` in bytes per second")
	flag.BoolVar(&config.PreferEncryption, "prefer-encryption", true,
		"Prefer encrytion")
	flag.BoolVar(&config.ForceEncryption, "force-encryption", false,
		"Force encryption")
	flag.StringVar(&doPortmap, "portmap", "auto", "NAT port mappping `protocol` (natpmp, upnp, auto, or off)")
	flag.BoolVar(&config.MultipathTCP, "mptcp", false,
		"Use MP-TCP for peer-to-peer connections")
	flag.BoolVar(&config.Debug, "debug", false,
		"Log all BitTorrent messages")
	flag.Parse()

	err = config.SetDefaultProxy(proxyURL)
	if err != nil {
		log.Printf("SetDefaultProxy: %v", err)
		return
	}

	if cpuprofile != "" {
		f, err := os.Create(cpuprofile)
		if err != nil {
			log.Printf("Create(cpuprofile): %v", err)
			return
		}
		pprof.StartCPUProfile(f)
		defer func() {
			pprof.StopCPUProfile()
			f.Close()
		}()
	}

	if memprofile != "" {
		defer func() {
			f, err := os.Create(memprofile)
			if err != nil {
				log.Printf("Create(memprofile): %v", err)
				return
			}
			pprof.WriteHeapProfile(f)
			f.Close()
		}()
	}

	if mutexprofile != "" {
		runtime.SetMutexProfileFraction(1)
		defer func() {
			f, err := os.Create(mutexprofile)
			if err != nil {
				log.Printf("Create(mutexprofile): %v", err)
				return
			}
			pprof.Lookup("mutex").WriteTo(f, 0)
			f.Close()
		}()
	}

	config.SetExternalIPv4Port(config.ProtocolPort, true)
	config.SetExternalIPv4Port(config.ProtocolPort, false)

	peer.UploadEstimator.Init(3 * time.Second)
	peer.UploadEstimator.Start()

	peer.DownloadEstimator.Init(3 * time.Second)
	peer.DownloadEstimator.Start()

	terminate := make(chan os.Signal, 1)
	signal.Notify(terminate, syscall.SIGINT)

	if mountpoint != "" {
		err := fuse.Serve(mountpoint)
		if err != nil {
			log.Fatalf("Couldn't mount directory: %v", err)
		}
		defer func(mountpoint string) {
			err := fuse.Close(mountpoint)
			if err != nil {
				log.Printf("Couldn't unmount directory: %v", err)
			}
		}(mountpoint)
	}

	ctx, cancelCtx := context.WithCancel(context.Background())
	portmapdone := make(chan struct{})
	defer func(portmapdone <-chan struct{}) {
		cancelCtx()
		log.Printf("Shutting down...")
		timer := time.NewTimer(4 * time.Second)
		select {
		case <-portmapdone:
			timer.Stop()
		case <-timer.C:
		}
	}(portmapdone)

	pmkind := 0
	switch doPortmap {
	case "off":
		pmkind = 0
	case "natpmp":
		pmkind = portmap.NATPMP
	case "upnp":
		pmkind = portmap.UPNP
	case "auto":
		pmkind = portmap.All
	default:
		log.Printf("Unknown portmapping kind %v", doPortmap)
	}

	if pmkind != 0 {
		go func() {
			err := portmap.Map(ctx, "STorrent",
				uint16(config.ProtocolPort), pmkind,
				func(proto string, status portmap.Status, err error) {
					if err != nil {
						log.Printf(
							"Port mapping: %v", err,
						)
					} else if status.Lifetime > 0 {
						log.Printf(
							"Mapped %v %v->%v (%v)",
							proto,
							status.Internal,
							status.External,
							status.Lifetime,
						)
					} else {
						log.Printf(
							"Unmapped %v %v",
							proto,
							status.Internal,
						)
					}
					e := status.External
					if e == 0 {
						e = status.Internal
					}
					config.SetExternalIPv4Port(
						int(e), proto == "tcp",
					)
				},
			)
			if err != nil {
				log.Println(err)
			}
			close(portmapdone)
		}()
	} else {
		close(portmapdone)
	}

	var dhtaddrs []net.TCPAddr
	var id []byte
	if config.DHTBootstrap != "" {
		id, dhtaddrs, err = rundht.Read(config.DHTBootstrap)
		if err != nil {
			log.Printf("Couldn't read %v: %v",
				config.DHTBootstrap, err)
		}
	}
	defer func() {
		if config.DHTBootstrap != "" {
			dir := filepath.Dir(config.DHTBootstrap)
			os.MkdirAll(dir, 0700) // ignore errors
			err := rundht.Write(config.DHTBootstrap, id)
			if err != nil {
				log.Printf("Couldn't write %v: %v",
					config.DHTBootstrap, err)
			}
		}
	}()

	config.DhtID = make([]byte, 20)
	if id != nil {
		copy(config.DhtID, id)
	} else {
		_, err := crand.Read(config.DhtID)
		if err != nil {
			log.Printf("Random: %v", err)
			return
		}
	}

	dhtevent, err := rundht.Run(ctx, config.DhtID, config.ProtocolPort)
	if err != nil {
		log.Printf("DHT: %v", err)
		return
	}

	go rundht.Handle(dhtevent)
	go rundht.Bootstrap(ctx, dhtaddrs)

	go func(args []string) {
		for _, arg := range args {
			proxy := config.DefaultProxy()
			t, err := tor.ReadMagnet(proxy, arg)
			if err != nil {
				log.Printf("Read magnet: %v", err)
				terminate <- nil
				return
			}
			if t == nil {
				t, err = tor.GetTorrent(ctx, proxy, arg)
				if err != nil {
					var perr tor.ParseURLError
					if !errors.As(err, &perr) {
						log.Printf("GetTorrent(%v): %v",
							arg, err)
						terminate <- nil
						return
					}
				}
			}
			if t == nil {
				torfile, err := os.Open(arg)
				if err != nil {
					log.Printf("Open(%v): %v", arg, err)
					terminate <- nil
					return
				}
				t, err = tor.ReadTorrent(proxy, torfile)
				if err != nil {
					log.Printf("%v: %v\n", arg, err)
					terminate <- nil
					torfile.Close()
					return
				}
				torfile.Close()
			}
			tor.AddTorrent(ctx, t)
			if t.InfoComplete() {
				t.Log.Printf("Added torrent %v (%v)\n",
					t.Name, t.Hash)
			} else {
				t.Log.Printf("Added torrent %v", t.Hash)
			}
		}
	}(flag.Args())

	var lc net.ListenConfig
	if config.MultipathTCP {
		lc.SetMultipathTCP(true)
	}
	listener, err :=
		lc.Listen(context.Background(),
			"tcp", fmt.Sprintf(":%v", config.ProtocolPort),
		)
	if err != nil {
		log.Printf("Listen: %v", err)
		return
	}

	go listen(listener)

	http.Handle("/", thttp.NewHandler(ctx))
	log.Printf("Listening on http://%v", config.HTTPAddr)
	httpListener, err := net.Listen("tcp", config.HTTPAddr)
	if err != nil {
		log.Printf("Listen (HTTP): %v", err)
		return
	}
	defer httpListener.Close()

	go func() {
		server := &http.Server{Addr: config.HTTPAddr}
		err := server.Serve(httpListener)
		log.Printf("Serve HTTP: %v", err)
	}()

	go func() {
		min := 250 * time.Millisecond
		max := 16 * time.Second
		interval := max
		for {
			rc := tor.Expire()
			if rc < 0 {
				interval = interval / 2
				if interval < min {
					interval = min
				}
			} else if rc > 0 {
				interval = interval * 2
				if interval > max {
					interval = max
				}
			}
			time.Sleep(roughly(interval))
		}
	}()

	<-terminate
}

func listen(listener net.Listener) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Printf("Accept: %v", err)
			time.Sleep(roughly(2 * time.Second))
			continue
		}
		go func(conn net.Conn) {
			err = tor.Server(conn, crypto.DefaultOptions(
				config.PreferEncryption,
				config.ForceEncryption,
			))
			if err != nil && err != io.EOF {
				log.Printf("Server: %v", err)
			}
		}(conn)
	}
}

func roughly(d time.Duration) time.Duration {
	r := d / 4
	if r > 2*time.Second {
		r = 2 * time.Second
	}
	m := time.Duration(rand.Int63n(int64(r)))
	return d + m - r/2
}
