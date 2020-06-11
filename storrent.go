package main

import (
	"context"
	crand "crypto/rand"
	"errors"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"runtime/pprof"
	"runtime/trace"
	"syscall"
	"time"

	"github.com/jech/storrent/config"
	"github.com/jech/storrent/crypto"
	"github.com/jech/storrent/fuse"
	thttp "github.com/jech/storrent/http"
	"github.com/jech/storrent/peer"
	"github.com/jech/storrent/physmem"
	"github.com/jech/storrent/portmap"
	"github.com/jech/storrent/rundht"
	"github.com/jech/storrent/tor"
)

func main() {
	var proxyURL, mountpoint string
	var cpuprofile, memprofile, mutexprofile, tracefile string
	var doPortmap bool

	mem, err := physmem.Total()
	if err != nil {
		log.Printf("Couldn't determine physical memory: %v", err)
		mem = 2 * 1024 * 1024 * 1024
	}

	fmt.Fprintf(os.Stderr, "STorrent 0.0 by Juliusz Chroboczek\n")

	rand.Seed(time.Now().UTC().UnixNano())

	flag.IntVar(&config.ProtocolPort, "port", 23222,
		"`port` used for BitTorrent and DHT traffic")
	flag.StringVar(&config.HTTPAddr, "http", "[::1]:8088",
		"web server address")
	flag.Int64Var(&config.MemoryMark, "mem", mem/2,
		"target memory usage in `bytes`")
	flag.StringVar(&cpuprofile, "cpuprofile", "",
		"store CPU profile in `file`")
	flag.StringVar(&memprofile, "memprofile", "",
		"store memory profile in `file`")
	flag.StringVar(&mutexprofile, "mutexprofile", "",
		"store mutex profile in `file`")
	flag.StringVar(&tracefile, "trace", "",
		"store execution trace in `file`")
	flag.StringVar(&proxyURL, "proxy", "",
		"`URL` of proxy to use for BitTorrent traffic")
	flag.StringVar(&mountpoint, "mountpoint", "",
		"FUSE `mountpoint`")
	flag.BoolVar(&config.DefaultDhtPassive, "dht-passive", false,
		"don't perform DHT announces by default")
	flag.BoolVar(&config.DefaultUseTrackers, "use-trackers", false,
		"use trackers (if available) by default")
	flag.BoolVar(&config.DefaultUseWebseeds, "use-webseeds", false,
		"use webseeds (if available) by default")
	flag.Float64Var(&config.PrefetchRate, "prefetch-rate", 768*1024,
		"prefetch `rate` in bytes per second")
	flag.IntVar(&config.DefaultEncryption, "encryption", 2,
		"encryption `level` (0 = never, 2 = default, 5 = always)")
	flag.BoolVar(&doPortmap, "portmap", true, "perform port mapping")
	flag.BoolVar(&config.Debug, "debug", false,
		"log all BitTorrent messages")

	flag.Parse()

	err = config.SetDefaultProxy(proxyURL)
	if err != nil {
		log.Printf("SetDefaultProxy: %v", err)
		return
	}

	if config.DefaultEncryption < 0 || config.DefaultEncryption > 5 {
		log.Printf("Wrong value %v for -encryption",
			config.DefaultEncryption)
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

	if tracefile != "" {
		f, err := os.Create(tracefile)
		if err != nil {
			log.Printf("Create(tracefile): %v", err)
			return
		}
		defer f.Close()
		err = trace.Start(f)
		if err != nil {
			log.Printf("trace.Start: %v", err)
			return
		}
		defer trace.Stop()
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

	if doPortmap {
		go func() {
			err := portmap.Map(ctx)
			if err != nil {
				log.Println(err)
			}
			close(portmapdone)
		}()
		time.Sleep(200 * time.Millisecond)
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
				log.Fatal(err)
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

	listener, err :=
		net.Listen("tcp", fmt.Sprintf(":%v", config.ProtocolPort))
	if err != nil {
		log.Printf("Listen: %v", err)
		return
	}

	go listen(listener)

	http.Handle("/", thttp.NewHandler(ctx))
	go func() {
		log.Printf("Listening on http://%v", config.HTTPAddr)
		err := http.ListenAndServe(config.HTTPAddr, nil)
		log.Printf("ListenAndServe: %v", err)
		return
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
			err = tor.Server(conn,
				crypto.OptionsMap[config.DefaultEncryption])
			if err != nil {
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
