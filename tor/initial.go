package tor

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"

	"golang.org/x/net/proxy"

	"storrent/config"
	"storrent/crypto"
	"storrent/known"
	"storrent/protocol"
)

var ErrMartianAddress = errors.New("martian address")
var ErrConnectionSelf = errors.New("connection to self")
var ErrDuplicateConnection = errors.New("duplicate connection")

func Server(conn net.Conn, cryptoOptions *crypto.Options) error {
	addr, ok := conn.RemoteAddr().(*net.TCPAddr)
	if !ok {
		addr = nil
	} else if !addr.IP.IsGlobalUnicast() {
		conn.Close()
		return ErrMartianAddress
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

	if result.Id.Equals(t.MyId) {
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
		conn.Close()
		return nil
	}

	q, _ := t.GetPeer(result.Id)
	if q != nil {
		conn.Close()
		return ErrDuplicateConnection
	}

	var ip net.IP
	if addr != nil {
		ip = addr.IP
	}
	err = t.NewPeer(t.proxy, conn, ip, 0, true, result, init)
	if err != nil {
		conn.Close()
		return err
	}
	return nil
}

func DialClient(ctx context.Context, t *Torrent, ip net.IP, port int,
	cryptoOptions *crypto.Options) error {
	var conn net.Conn
	var err error
	cryptoHandshake :=
		cryptoOptions.PreferCryptoHandshake &&
			cryptoOptions.AllowCryptoHandshake

again:
	if err := ctx.Err(); err != nil {
		return err
	}

	s := net.JoinHostPort(ip.String(), fmt.Sprintf("%d", port))

	if t.proxy == "" {
		var dialer net.Dialer
		conn, err = dialer.DialContext(ctx, "tcp", s)
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
			return errors.New("Dialer is not ContextDialer")
		}
		conn, err = d.DialContext(ctx, "tcp", s)
	}
	if err != nil {
		return err
	}

	if err := ctx.Err(); err != nil {
		conn.Close()
		return err
	}

	if !ip.IsGlobalUnicast() {
		return ErrMartianAddress
	}
	if port == 0 || port == 1 || port == 22 || port == 25 {
		return errors.New("bad port")
	}

	err = Client(conn, t, ip, port, t.proxy, cryptoHandshake, cryptoOptions)
	if err == protocol.ErrBadHandshake {
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

func Client(conn net.Conn, t *Torrent, ip net.IP, port int, proxy string,
	cryptoHandshake bool, cryptoOptions *crypto.Options) error {

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

	err = t.AddKnown(ip, port, result.Id, "", known.Seen)
	if err != nil {
		conn.Close()
		return err
	}

	if result.Id.Equals(t.MyId) {
		conn.Close()
		return ErrConnectionSelf
	}

	q, _ := t.GetPeer(result.Id)
	if q != nil {
		conn.Close()
		return ErrDuplicateConnection
	}

	err = t.NewPeer(proxy, conn, ip, port, false, result, init)
	if err != nil {
		conn.Close()
		return err
	}
	return nil
}
