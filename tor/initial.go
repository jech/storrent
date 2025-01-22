package tor

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"net/url"
	"time"

	"golang.org/x/net/proxy"

	"github.com/jech/storrent/config"
	"github.com/jech/storrent/crypto"
	"github.com/jech/storrent/known"
	"github.com/jech/storrent/protocol"
)

var ErrMartianAddress = errors.New("martian address")
var ErrConnectionSelf = errors.New("connection to self")
var ErrDuplicateConnection = errors.New("duplicate connection")

// Server accepts a peer connection.
func Server(conn net.Conn, cryptoOptions *crypto.Options) error {
	addr, ok := conn.RemoteAddr().(*net.TCPAddr)
	if !ok || !addr.IP.IsGlobalUnicast() {
		conn.Close()
		return ErrMartianAddress
	}

	ip := addr.IP.To4()
	if ip == nil {
		ip = addr.IP.To16()
	}
	if ip == nil {
		conn.Close()
		return errors.New("couldn't parse IP address")
	}

	conn, result, init, err :=
		protocol.ServerHandshake(conn, infoHashes(false), cryptoOptions)
	if err != nil {
		conn.Close()
		return err
	}

	t := Get(result.Hash)
	if t == nil {
		conn.Close()
		return protocol.ErrUnknownTorrent
	}

	if result.Id.Equal(t.MyId) {
		conn.Close()
		return ErrConnectionSelf
	}

	if t.hasProxy() {
		conn.Close()
		return errors.New("torrent is proxied")
	}

	stats, err := t.GetStats()
	if err != nil {
		conn.Close()
		return err
	}

	if stats.NumPeers >= config.MaxPeersPerTorrent {
		dropped, _ := t.DropPeer()
		if !dropped {
			conn.Close()
			return nil
		}
	}

	q, _ := t.GetPeer(result.Id)
	if q != nil {
		conn.Close()
		return ErrDuplicateConnection
	}

	ipp, ok := netip.AddrFromSlice(ip)
	if !ok {
		conn.Close()
		return errors.New("couldn't parse address")
	}
	err = t.NewPeer(
		t.proxy, conn, netip.AddrPortFrom(ipp, 0), true, result, init,
	)
	if err != nil {
		conn.Close()
		return err
	}
	return nil
}

// DialClient connects to a peer and runs the client loop.
func DialClient(ctx context.Context, t *Torrent, addr netip.AddrPort, cryptoOptions *crypto.Options) error {
	var conn net.Conn
	var err error
	cryptoHandshake :=
		cryptoOptions.PreferCryptoHandshake &&
			cryptoOptions.AllowCryptoHandshake

again:
	if err := ctx.Err(); err != nil {
		return err
	}

	if !addr.Addr().IsGlobalUnicast() {
		return ErrMartianAddress
	}
	if port := addr.Port(); port == 0 || port == 1 || port == 22 || port == 25 {
		return errors.New("bad port")
	}

	if t.proxy == "" {
		var dialer net.Dialer
		dialer.Timeout = 20 * time.Second
		if config.MultipathTCP {
			dialer.SetMultipathTCP(true)
		}
		conn, err = dialer.DialContext(ctx, "tcp", addr.String())
	} else {
		var u *url.URL
		u, err = url.Parse(t.proxy)
		if err != nil {
			return err
		}
		var dialer proxy.Dialer
		dialer, err = proxy.FromURL(u, proxy.Direct)
		if err != nil {
			return err
		}
		d, ok := dialer.(proxy.ContextDialer)
		if !ok {
			return errors.New("dialer is not ContextDialer")
		}
		ctx2, cancel := context.WithTimeout(ctx, 20*time.Second)
		conn, err = d.DialContext(ctx2, "tcp", addr.String())
		cancel()
	}
	if err != nil {
		return err
	}

	if err := ctx.Err(); err != nil {
		conn.Close()
		return err
	}

	err = Client(conn, t, addr, t.proxy, cryptoHandshake, cryptoOptions)
	if errors.Is(err, protocol.ErrBadHandshake) {
		if cryptoOptions.PreferCryptoHandshake &&
			!cryptoOptions.ForceCryptoHandshake {
			if cryptoHandshake {
				cryptoHandshake = false
				goto again
			}
		}
		if !cryptoOptions.PreferCryptoHandshake &&
			cryptoOptions.AllowCryptoHandshake {
			if !cryptoHandshake {
				cryptoHandshake = true
				goto again
			}
		}
	}

	return err
}

// Client establishes a connection with a client.
func Client(conn net.Conn, t *Torrent, addr netip.AddrPort, proxy string, cryptoHandshake bool, cryptoOptions *crypto.Options) error {

	stats, err := t.GetStats()
	if err != nil {
		conn.Close()
		return err
	}

	if stats.NumPeers >= config.MaxPeersPerTorrent {
		conn.Close()
		return nil
	}

	conn, result, init, err :=
		protocol.ClientHandshake(conn, cryptoHandshake,
			t.Hash, t.MyId, cryptoOptions)
	if err != nil {
		conn.Close()
		return err
	}

	err = t.AddKnown(addr, result.Id, "", known.Seen)
	if err != nil {
		conn.Close()
		return err
	}

	if result.Id.Equal(t.MyId) {
		conn.Close()
		return ErrConnectionSelf
	}

	q, _ := t.GetPeer(result.Id)
	if q != nil {
		conn.Close()
		return ErrDuplicateConnection
	}

	err = t.NewPeer(proxy, conn, addr, false, result, init)
	if err != nil {
		conn.Close()
		return err
	}
	return nil
}
