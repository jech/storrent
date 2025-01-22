package http

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jech/storrent/alloc"
	"github.com/jech/storrent/config"
	"github.com/jech/storrent/dht"
	"github.com/jech/storrent/hash"
	"github.com/jech/storrent/known"
	"github.com/jech/storrent/path"
	"github.com/jech/storrent/peer"
	"github.com/jech/storrent/tor"
	"github.com/jech/storrent/tracker"
)

func Serve(addr string) error {
	http.HandleFunc("/{$}", rootHandler)
	http.HandleFunc("/{file}", torRootHandler)
	http.HandleFunc("/{hash}/{path...}", torHandler)

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}

	server := &http.Server{Addr: addr}
	go func(listener net.Listener) {
		err := server.Serve(listener)
		panic(err)
	}(listener)

	return nil
}

func checkLocal(w http.ResponseWriter, r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.Host)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return false
	}

	// The server is only bound to localhost, but an attacker might be
	// able to cause the user's browser to connect to localhost by
	// manipulating the DNS.  Prevent this by making sure that the
	// browser thinks it's connecting to localhost.
	if host != "localhost" && net.ParseIP(host) == nil {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return false
	}

	return true
}

func rootHandler(w http.ResponseWriter, r *http.Request) {
	if !checkLocal(w, r) {
		return
	}

	if r.Method != "HEAD" && r.Method != "GET" && r.Method != "POST" {
		w.Header().Set("allow", "HEAD, GET, POST")
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	err := r.ParseForm()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	q := r.Form.Get("q")

	if q == "" {
		if r.Method != "HEAD" && r.Method != "GET" {
			http.Error(w, "Method not allowed",
				http.StatusMethodNotAllowed)
			return
		}
		torrents(w, r)
		return
	} else if q == "peers" {
		if r.Method != "HEAD" && r.Method != "GET" {
			http.Error(w, "Method not allowed",
				http.StatusMethodNotAllowed)
			return
		}
		hash := hash.Parse(r.Form.Get("hash"))
		if hash == nil {
			http.NotFound(w, r)
			return
		}
		torrent := tor.Get(hash)
		if torrent == nil {
			http.NotFound(w, r)
			return
		}
		peers(w, r, torrent)
		return
	} else if q == "add" {
		if r.Method != "GET" && r.Method != "POST" {
			http.Error(w, "Method not allowed",
				http.StatusMethodNotAllowed)
			return
		}
		m, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		var u string
		var f io.ReadCloser
		if m == "multipart/form-data" {
			err := r.ParseMultipartForm(1024 * 1024)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			u = strings.TrimSpace(r.FormValue("url"))
			f, _, err = r.FormFile("file")
			if err != nil && err != http.ErrMissingFile {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		} else {
			u = strings.TrimSpace(r.Form.Get("url"))
		}
		var t *tor.Torrent
		if f != nil {
			defer f.Close()
			if u != "" {
				http.Error(w, "both magnet and file provided",
					http.StatusBadRequest)
				return
			}
			t, err = tor.ReadTorrent(config.DefaultProxy(), f)

		} else {
			if u == "" {
				http.Error(w, "neither URL nor file provided",
					http.StatusBadRequest)
				return
			}
			t, err = fetchTorrent(r.Context(), u)
		}
		if t == nil || err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		_, err = tor.AddTorrent(context.Background(), t)
		if err != nil && err != os.ErrExist {
			http.Error(w, err.Error(),
				http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	} else if q == "delete" {
		if r.Method != "GET" && r.Method != "POST" {
			http.Error(w, "Method not allowed",
				http.StatusMethodNotAllowed)
			return
		}
		h := hash.Parse(r.FormValue("hash"))
		if h == nil {
			http.Error(w, "couldn't parse hash",
				http.StatusBadRequest)
			return
		}
		t := tor.Get(h)
		if t == nil {
			http.NotFound(w, r)
			return
		}
		err := t.Kill(r.Context())
		if err != nil {
			if errors.Is(err, tor.ErrTorrentDead) {
				http.NotFound(w, r)
			} else {
				http.Error(w, err.Error(),
					http.StatusInternalServerError)
			}
			return
		}
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	} else if q == "set" {
		if r.Method != "GET" && r.Method != "POST" {
			http.Error(w, "Method not allowed",
				http.StatusMethodNotAllowed)
			return
		}
		upload := r.Form.Get("upload")
		if upload != "" {
			v, err := strconv.ParseFloat(upload, 64)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			config.SetUploadRate(v)
		}
		idle := r.Form.Get("idle")
		if idle != "" {
			v, err := strconv.ParseInt(idle, 10, 32)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			config.SetIdleRate(uint32(v))
		}
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	} else if q == "set-torrent" {
		if r.Method != "GET" && r.Method != "POST" {
			http.Error(w, "Method not allowed",
				http.StatusMethodNotAllowed)
			return
		}
		h := hash.Parse(r.FormValue("hash"))
		if h == nil {
			http.Error(w, "couldn't parse hash", http.StatusBadRequest)
			return
		}
		t := tor.Get(h)
		if t == nil {
			http.NotFound(w, r)
			return
		}
		dhtMode, err := config.ParseDhtMode(r.Form.Get("dht-mode"))
		if err != nil {
			http.Error(w, "couldn't parse dht-mode",
				http.StatusBadRequest)
			return
		}
		conf := peer.TorConf{
			DhtMode:     dhtMode,
			UseTrackers: r.Form.Get("use-trackers") != "",
			UseWebseeds: r.Form.Get("use-webseeds") != "",
		}
		err = t.SetConf(conf)
		if err != nil {
			http.Error(w, err.Error(),
				http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	} else {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
}

func torRootHandler(w http.ResponseWriter, r *http.Request) {
	if !checkLocal(w, r) {
		return
	}

	file := r.PathValue("file")
	base, ext, ok := strings.Cut(file, ".")
	if !ok {
		hash := hash.Parse(base)
		if hash == nil {
			http.NotFound(w, r)
			return
		}

		t := tor.Get(hash)
		if t == nil {
			http.NotFound(w, r)
			return
		}

		http.Redirect(w, r, r.URL.Path+"/",
			http.StatusMovedPermanently)
		return
	}

	hash := hash.Parse(base)
	if hash == nil {
		http.NotFound(w, r)
		return
	}

	t := tor.Get(hash)
	if t == nil {
		http.NotFound(w, r)
		return
	}

	switch ext {
	case "torrent":
		torfile(w, r, t)
		return
	case "m3u":
		playlist(w, r, t, nil)
		return
	default:
		http.NotFound(w, r)
		return
	}
}

func torHandler(w http.ResponseWriter, r *http.Request) {
	if !checkLocal(w, r) {
		return
	}

	h := r.PathValue("hash")
	pth := "/" + r.PathValue("path")

	hash := hash.Parse(h)
	if hash == nil {
		http.NotFound(w, r)
		return
	}

	t := tor.Get(hash)
	if t == nil {
		http.NotFound(w, r)
		return
	}

	if r.Method != "HEAD" && r.Method != "GET" {
		w.Header().Set("allow", "HEAD, GET")
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	err := r.ParseForm()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if r.Form["playlist"] != nil {
		playlist(w, r, t, path.Parse(pth))
		return
	}
	if pth[len(pth)-1] == '/' {
		directory(w, r, t, path.Parse(pth))
		return
	}
	file(w, r, t, path.Parse(pth))
	return
}

func fetchTorrent(ctx context.Context, data string) (*tor.Torrent, error) {
	t, err := tor.ReadMagnet(config.DefaultProxy(), data)
	if t != nil || err != nil {
		return t, err
	}
	return tor.GetTorrent(ctx, config.DefaultProxy(), data)
}

func header(w http.ResponseWriter, r *http.Request, title string) bool {
	w.Header().Set("content-type", "text/html; charset=utf-8")
	w.Header().Set("cache-control", "no-cache")
	if r.Method == "HEAD" {
		return true
	}
	fmt.Fprintf(w, "<!DOCTYPE html>\n<html><head>\n")
	fmt.Fprintf(w, "<title>%v</title>\n", html.EscapeString(title))
	if r.Host != "" {
		fmt.Fprintf(w, "<script type=\"text/javascript\">\n")
		fmt.Fprintf(w, "navigator.registerProtocolHandler('magnet','http://%v/?q=add&url=%%s','Torrent');\n",
			r.Host)
		fmt.Fprintf(w, "</script>\n")
	}
	fmt.Fprintf(w, "</head><body>\n")
	return false
}

func footer(w http.ResponseWriter) {
	fmt.Fprintf(w, "</body></html>\n")
}

func directory(w http.ResponseWriter, r *http.Request, t *tor.Torrent, pth path.Path) {

	ctx := r.Context()

	done := header(w, r, t.Name)
	if done {
		return
	}
	err := torrentEntry(ctx, w, t, pth)
	if err != nil {
		return
	}
	footer(w)
}

func pathUrl(p path.Path) string {
	var b []byte
	for _, s := range p {
		t := url.PathEscape(s)
		b = append(b, t...)
		b = append(b, '/')
	}
	return string(b[0 : len(b)-1])
}

func approxBytes(v int64) string {
	if v == 0 {
		return "0"
	}
	if v < 2048 {
		return fmt.Sprintf("%v B", v)
	}
	if v < 1000*1024 {
		return fmt.Sprintf("%.1f kB", float64(v)/1024)
	}
	if v < 2048*1024 {
		return fmt.Sprintf("%.0f kB", float64(v)/1024)
	}
	if v < 1000*1024*1024 {
		return fmt.Sprintf("%.1f MB", float64(v)/(1024*1024))
	}
	if v < 2048*1024*1024 {
		return fmt.Sprintf("%.0f MB", float64(v)/(1024*1024))
	}
	if v < 1000*1024*1024*1024 {
		return fmt.Sprintf("%.1f GB", float64(v)/(1024*1024*1024))
	}
	return fmt.Sprintf("%.0f GB", float64(v)/(1024*1024*1024))

}

func approxRate(v float64) string {
	if v == 0 {
		return "0"
	}
	if v < 2048 {
		return fmt.Sprintf("%.0f B/s", v)
	}
	if v < 1000*1024 {
		return fmt.Sprintf("%.1f kB/s", v/1024)
	}
	if v < 2048*1024 {
		return fmt.Sprintf("%.0f kB/s", v/1024)
	}
	if v < 1000*1024*1024 {
		return fmt.Sprintf("%.1f MB/s", v/(1024*1024))
	}
	return fmt.Sprintf("%.0f MB/s", v/(1024*1024))
}

func torrentFile(w io.Writer, hash hash.Hash, path path.Path, length int64, available int) {
	p := pathUrl(path)
	fmt.Fprintf(w,
		"<tr><td><a href=\"/%v/%v\">%v</a></td>"+
			"<td>%v</td><td>%v</td></tr>\n",
		hash, p, html.EscapeString(path.String()),
		approxBytes(length), available)
}

func torrentDir(w io.Writer, hash hash.Hash, pth path.Path, lastdir path.Path) {
	var dir path.Path
	for i := 0; i < len(pth) && i < len(lastdir); i++ {
		if pth[i] != lastdir[i] {
			break
		}
		dir = append(dir, pth[i])
	}
	for i := len(dir); i < len(pth); i++ {
		dir = append(dir, pth[i])
		p := pathUrl(dir)
		fmt.Fprintf(w,
			"<tr><td><a href=\"/%v/%v/\">%v/</a></td><td>"+
				"(<a href=\"/%v/%v/?playlist\">playlist</a>)"+
				"</td></tr>\n",
			hash, p, html.EscapeString(dir.String()),
			hash, p)
	}
}

func torrentEntry(ctx context.Context, w http.ResponseWriter, t *tor.Torrent, dir path.Path) error {
	hash := t.Hash
	name := t.Name
	if !t.InfoComplete() {
		if name != "" {
			name = name + " "
		}
		name = name + "<em>(incomplete)</em>"
	}
	fmt.Fprintf(w, "<p><a href=\"/%v/\">%v</a> ", hash, name)
	fmt.Fprintf(w, "<a href=\"/%v.torrent\">%v</a> ", hash, hash)
	fmt.Fprintf(w, "(<a href=\"/%v.m3u\">playlist</a>): ", hash)
	c := t.Pieces.Bitmap().Count()
	if t.InfoComplete() {
		fmt.Fprintf(w, "%v in %v+%v/%v pieces (%v each), ",
			approxBytes(t.Pieces.Bytes()),
			c, t.Pieces.Count()-c,
			(t.Pieces.Length()+int64(t.Pieces.PieceSize())-1)/
				int64(t.Pieces.PieceSize()),
			approxBytes(int64(t.Pieces.PieceSize())))
	}
	stats, _ := t.GetStats()
	if stats != nil {
		fmt.Fprintf(w, "<a href=\"/?q=peers&hash=%v\">%v/%v peers</a>",
			hash, stats.NumPeers, stats.NumKnown)
	}
	fmt.Fprintf(w, "</p>")
	fmt.Fprintf(w, "<p><table>\n")

	if !t.InfoComplete() {
	} else if t.Files == nil {
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(dir) == 0 {
			available, _ := t.GetAvailable()
			torrentFile(w, t.Hash, path.Parse(t.Name),
				t.Pieces.Length(),
				available.AvailableRange(t,
					0, t.Pieces.Length()))
		}
	} else {
		a := make([]int, 0, len(t.Files))
		for i := range t.Files {
			if t.Files[i].Path.Within(dir) {
				a = append(a, i)
			}
		}
		slices.SortFunc(a, func(i, j int) int {
			return t.Files[i].Path.Compare(t.Files[j].Path)
		})
		var lastdir path.Path
		available, _ := t.GetAvailable()
		for _, i := range a {
			if err := ctx.Err(); err != nil {
				return err
			}
			f := t.Files[i]
			path := f.Path
			dir := path[:len(path)-1]
			if !dir.Equal(lastdir) {
				torrentDir(w, t.Hash, dir, lastdir)
				lastdir = dir
			}
			torrentFile(w, t.Hash, f.Path, f.Length,
				available.AvailableRange(t, f.Offset, f.Length))
		}
	}
	fmt.Fprintf(w, "</table></p>\n")
	conf, err := t.GetConf()
	if err == nil {
		var useTrackers, useWebseeds string
		if conf.UseTrackers {
			useTrackers = " checked"
		}
		if conf.UseWebseeds {
			useWebseeds = " checked"
		}
		fmt.Fprintf(w, "<form action=\"/?q=set-torrent\" method=\"post\">\n")
		if dht.Available() {
			fmt.Fprintf(w, "<label for=\"dht-mode-%v\">DHT mode:</label>\n", t.Hash)
			fmt.Fprintf(w, "<select id=\"dht-mode-%v\" name=\"dht-mode\" style=\"margin-right: 1em\">\n", t.Hash)
			for _, m := range []config.DhtMode{config.DhtNone, config.DhtPassive, config.DhtNormal} {
				selected := ""
				if conf.DhtMode == m {
					selected = " selected"
				}
				fmt.Fprintf(w, "<option value=\"%v\"%v>%v</option>\n",
					m, selected, m)
			}
			fmt.Fprintf(w, "</select>\n")
		}
		fmt.Fprintf(w, "<label for=\"use-trackers-%v\">Use trackers (%v):</label>\n", t.Hash, stats.NumTrackers)
		fmt.Fprintf(w, "<input type=\"checkbox\" id=\"use-trackers-%v\" name=\"use-trackers\"%v/ style=\"margin-right: 1em\">\n", t.Hash, useTrackers)
		fmt.Fprintf(w, "<label for=\"use-webseeds-%v\">Use webseeds (%v):</label>\n", t.Hash, stats.NumWebseeds)
		fmt.Fprintf(w, "<input type=\"checkbox\" id=\"use-webseeds-%v\" name=\"use-webseeds\"%v/ style=\"margin-right: 1em\">\n ", t.Hash, useWebseeds)
		fmt.Fprintf(w, "<button type=\"submit\" name=\"hash\" value=\"%v\">Set</button></form>\n", t.Hash)
	}
	fmt.Fprintf(w, "<form action=\"/?q=delete\" class=\"delete-form\" method=\"post\"><button type=\"submit\" name=\"hash\" value=\"%v\">Delete</button></form>\n",
		t.Hash)
	return nil
}

func torrents(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	done := header(w, r, "STorrent")
	if done {
		return
	}

	fmt.Fprintf(w, "<form style=\"display: inline;\" action=\"/?q=add\" method=\"post\" enctype=\"multipart/form-data\">Magnet or URL: <input type=\"text\" name=\"url\"/><input type=\"submit\"/></form>\n")
	fmt.Fprintf(w, "<form style=\"display: inline; margin-left: 2em;\" action=\"/?q=add\" method=\"post\" enctype=\"multipart/form-data\"><input type=\"file\" name=\"file\"><input type=\"submit\"/></form>\n")
	fmt.Fprintf(w, "<form action=\"/?q=set\" method=\"post\">Idle download: <input type=\"text\" name=\"idle\"/> Upload: <input type=\"text\" name=\"upload\"/> <input type=\"submit\"/></form>\n")

	fmt.Fprintf(w, "<p>Download %v / %v, upload %v / %v (unchoking %v), ",
		approxRate(peer.DownloadEstimator.Estimate()),
		approxRate(float64(config.IdleRate())),
		approxRate(peer.UploadEstimator.Estimate()),
		approxRate(config.UploadRate()),
		peer.NumUnchoking())

	if dht.Available() {
		g4, g6, d4, d6, i4, i6 := dht.Count()
		fmt.Fprintf(w, "DHT %v+%v/%v %v+%v/%v, ",
			g4, i4, g4+d4,
			g6, i6, g6+d6)
	}
	fmt.Fprintf(w, "%v / %v allocated.</p>\n",
		approxBytes(alloc.Bytes()), approxBytes(config.MemoryHighMark()))

	var tors []*tor.Torrent
	tor.Range(func(k hash.Hash, t *tor.Torrent) bool {
		tors = append(tors, t)
		return true
	})
	slices.SortFunc(tors, func(a, b *tor.Torrent) int {
		if a.Name != b.Name {
			return strings.Compare(a.Name, b.Name)
		}
		return bytes.Compare(a.Hash, b.Hash)
	})
	for _, t := range tors {
		err := torrentEntry(ctx, w, t, path.Path(nil))
		if err != nil {
			return
		}
	}

	footer(w)
}

func peers(w http.ResponseWriter, r *http.Request, t *tor.Torrent) {
	ps, err := t.GetPeers()
	if err != nil {
		if errors.Is(err, tor.ErrTorrentDead) {
			http.NotFound(w, r)
		} else {
			http.Error(w, err.Error(),
				http.StatusInternalServerError)
		}
		return
	}

	kps, err := t.GetKnowns()
	if err != nil {
		if errors.Is(err, tor.ErrTorrentDead) {
			http.NotFound(w, r)
		} else {
			http.Error(w, err.Error(),
				http.StatusInternalServerError)
		}
		return
	}

	done := header(w, r, "Peers for "+t.Name)
	if done {
		return
	}

	slices.SortFunc(ps, func(a, b *peer.Peer) int {
		return bytes.Compare(a.Id, b.Id)
	})
	fmt.Fprintf(w, "<p><table>\n")
	for _, p := range ps {
		hpeer(w, p, t)
	}
	fmt.Fprintf(w, "</table></p>\n")

	trackers := t.Trackers()
	if len(trackers) > 0 {
		fmt.Fprintf(w, "<p><table>\n")
		for i, tl := range trackers {
			for _, tt := range tl {
				state := ""
				st, err := tt.GetState()
				if st == tracker.Error && err != nil {
					state = fmt.Sprintf("(%v)", err)
				} else if st != tracker.Idle {
					state = fmt.Sprintf("(%v)", st.String())
				}
				fmt.Fprintf(w, "<tr><td>%v</td><td>%v</td></tr>\n",
					tt.URL(), state)
			}
			if i+1 < len(trackers) {
				fmt.Fprintf(w, "<tr></tr>\n")
			}
		}
		fmt.Fprintf(w, "</table></p>\n")
	}

	wss := t.Webseeds()
	if len(wss) > 0 {
		fmt.Fprintf(w, "<p><table>\n")
		for _, ws := range t.Webseeds() {
			cnt := ""
			count := ws.Count()
			if count > 0 {
				cnt = fmt.Sprintf("%v", count)
			}
			fmt.Fprintf(w, "<tr><td>%v</td><td>%v</td><td>%.0f</td>",
				ws.URL(), cnt, ws.Rate())
		}
		fmt.Fprintf(w, "</table></p>\n")
	}

	slices.SortFunc(kps, func(a, b known.Peer) int {
		return a.Addr.Compare(b.Addr)
	})
	fmt.Fprintf(w, "<p><table>\n")
	for _, k := range kps {
		hknown(w, &k, t)
	}
	fmt.Fprintf(w, "</table></p>\n")

	footer(w)
}

func peerVersion(id []byte, version string) string {
	if version != "" {
		return version
	}
	if len(id) > 7 && id[0] == '-' && id[7] == '-' {
		return string(id[1:7])
	}
	return ""
}

func hpeer(w http.ResponseWriter, p *peer.Peer, t *tor.Torrent) {
	kp, _ := t.GetKnown(p.Id, p.GetAddr())
	var addr string
	a := p.GetAddr()
	if a.Port() == 0 {
		if kp != nil {
			addr = kp.Addr.String()
		} else {
			addr = p.IP.String()
		}
	} else {
		addr = a.String()
	}
	fmt.Fprintf(w, "<tr><td>%v</td>", addr)

	stats := p.GetStats()
	if stats == nil {
		fmt.Fprintf(w, "<td><em>(dead)</em></td></tr>\n")
		return
	}

	var prefix, suffix string
	if !stats.Unchoked {
		prefix = "("
		suffix = ")"
	}
	qlen := stats.Qlen - stats.Rlen
	if qlen != 0 {
		fmt.Fprintf(w, "<td>%v%v+%v%v</td>",
			prefix, stats.Rlen, qlen, suffix)
	} else if stats.Rlen > 0 {
		fmt.Fprintf(w, "<td>%v%v%v</td>", prefix, stats.Rlen, suffix)
	} else if stats.Unchoked {
		fmt.Fprintf(w, "<td>0</td>")
	} else if stats.AmInterested {
		fmt.Fprintf(w, "<td>&#183;</td>")
	} else {
		fmt.Fprintf(w, "<td></td>")
	}

	fmt.Fprintf(w, "<td>%.0f/%.0f</td>", stats.AvgDownload, stats.Download)

	var zero time.Time
	if stats.AmUnchoking {
		fmt.Fprintf(w, "<td>%v</td>", stats.Ulen)
	} else if stats.Interested {
		if stats.UnchokeTime.Equal(zero) {
			fmt.Fprintf(w, "<td>(&#8734;s)</td>")
		} else {
			fmt.Fprintf(w, "<td>(%vs)</td>",
				int((time.Since(stats.UnchokeTime)+
					time.Second/2)/time.Second))
		}
	} else {
		fmt.Fprintf(w, "<td></td>")
	}

	fmt.Fprintf(w, "<td>%.0f</td>", stats.Upload)

	if stats.Rtt != 0 || stats.Rttvar != 0 {
		fmt.Fprintf(w, "<td>%.0f&#177;%.0f</td>",
			float64(stats.Rtt)/float64(time.Millisecond),
			float64(stats.Rttvar)/float64(time.Millisecond))
	} else {
		fmt.Fprintf(w, "<td></td>")
	}

	var scount string
	if t.Pieces.PieceSize() > 0 {
		count := (t.Pieces.Length() +
			int64(t.Pieces.PieceSize()) - 1) /
			int64(t.Pieces.PieceSize())
		scount = fmt.Sprintf("/%v", count)
	} else {
		scount = ""
	}

	flags := ""
	if stats.HasProxy {
		flags += "P"
	}
	if p.Incoming {
		flags += "I"
	}
	if p.Encrypted() {
		flags += "E"
	}
	if p.MultipathTCP() {
		flags += "M"
	}
	if stats.AmUnchoking {
		flags += "U"
	} else if stats.Interested {
		flags += "u"
	}
	if stats.Seed {
		flags += "S"
	} else if stats.UploadOnly {
		flags += "s"
	}

	fmt.Fprintf(w, "<td>%v%v</td><td>%v</td>",
		stats.PieceCount, scount, flags)
	if stats.NumPex > 0 {
		fmt.Fprintf(w, "<td>%v</td>", stats.NumPex)
	} else {
		fmt.Fprintf(w, "<td></td>")
	}

	version := ""
	if kp != nil {
		version = kp.Version
	}
	version = peerVersion(p.Id, version)
	fmt.Fprintf(w, "<td>%v</td>", html.EscapeString(version))
	fmt.Fprintf(w, "<td>%v</td></tr>", p.Id)
}

func recent(tm time.Time) bool {
	return time.Since(tm) < 35*time.Minute
}

func hknown(w http.ResponseWriter, kp *known.Peer, t *tor.Torrent) {
	buf := new(bytes.Buffer)
	if kp.Attempts > 0 {
		fmt.Fprintf(buf, "%v, ", kp.Attempts)
	}
	if recent(kp.SeenTime) || recent(kp.ActiveTime) {
		fmt.Fprintf(buf, "Seen, ")
	}
	if recent(kp.HeardTime) {
		fmt.Fprintf(buf, "Heard, ")
	}
	if recent(kp.TrackerTime) {
		fmt.Fprintf(buf, "T, ")
	}
	if recent(kp.DHTTime) {
		fmt.Fprintf(buf, "DHT, ")
	}
	if recent(kp.PEXTime) {
		fmt.Fprintf(buf, "PEX, ")
	}
	if kp.Bad() {
		fmt.Fprintf(buf, "Bad, ")
	}
	var flags string
	if buf.Len() > 2 {
		b := buf.Bytes()
		flags = string(b[0 : len(b)-2])
	} else {
		flags = buf.String()
	}

	fmt.Fprintf(w, "<tr><td>%v</td><td>%v</td><td>%v</td><td>%v</td></tr>\n",
		kp.Addr.String(), flags,
		html.EscapeString(peerVersion(kp.Id, kp.Version)), kp.Id,
	)
}

func torfile(w http.ResponseWriter, r *http.Request, t *tor.Torrent) {
	if !t.InfoComplete() {
		http.Error(w, "torrent metadata incomplete",
			http.StatusGatewayTimeout)
		return
	}

	w.Header().Set("content-type", "application/x-bittorrent")
	if t.CreationDate > 0 {
		cdate := time.Unix(t.CreationDate, 0)
		w.Header().Set("last-modified",
			cdate.UTC().Format(http.TimeFormat))
	}

	if r.Method == "HEAD" {
		return
	}

	err := tor.WriteTorrent(w, t)
	if err != nil {
		panic(http.ErrAbortHandler)
	}
}

func m3uentry(w http.ResponseWriter, host string, hash hash.Hash, path path.Path) {
	fmt.Fprintf(w, "#EXTINF:-1,%v\n",
		strings.Replace(path[len(path)-1], ",", "", -1))
	fmt.Fprintf(w, "http://%v/%v/%v\n",
		host, hash, pathUrl(path))
}

func playlist(w http.ResponseWriter, r *http.Request, t *tor.Torrent, dir path.Path) {
	if !t.InfoComplete() {
		http.Error(w, "torrent metadata incomplete",
			http.StatusGatewayTimeout)
		return
	}

	if t.Files == nil {
		if len(dir) > 0 {
			http.NotFound(w, r)
			return
		}
	} else {
		var found bool
		for _, f := range t.Files {
			if f.Path.Within(dir) {
				found = true
				break
			}
		}
		if !found {
			http.NotFound(w, r)
			return
		}
	}

	w.Header().Set("content-type", "application/vnd.apple.mpegurl")
	if t.CreationDate > 0 {
		cdate := time.Unix(t.CreationDate, 0)
		w.Header().Set("last-modified",
			cdate.UTC().Format(http.TimeFormat))
	}
	if r.Method == "HEAD" {
		return
	}

	fmt.Fprintf(w, "#EXTM3U\n")
	if t.Files == nil {
		m3uentry(w, r.Host, t.Hash, path.Parse(t.Name))
	} else {
		a := make([]int, len(t.Files))
		for i := range a {
			a[i] = i
		}
		slices.SortFunc(a, func(i, j int) int {
			return t.Files[i].Path.Compare(t.Files[j].Path)
		})
		for _, i := range a {
			path := t.Files[i].Path
			if path.Within(dir) {
				m3uentry(w, r.Host, t.Hash, path)
			}
		}
	}
}

func file(w http.ResponseWriter, r *http.Request, t *tor.Torrent, path path.Path) {
	if !t.InfoComplete() {
		http.Error(w, "torrent metadata incomplete",
			http.StatusGatewayTimeout)
		return
	}

	offset, length, etag, err := fileParms(t, path)
	if err != nil {
		if os.IsNotExist(err) {
			http.NotFound(w, r)
			return
		} else {
			http.Error(w, err.Error(),
				http.StatusInternalServerError)
			return
		}
	}
	var ctime time.Time
	if t.CreationDate > 0 {
		ctime = time.Unix(t.CreationDate, 0)
	}
	w.Header().Set("etag", etag)
	reader := t.NewReader(r.Context(), offset, length)
	defer reader.Close()
	http.ServeContent(w, r, path.String(), ctime, reader)
}

func fileParms(t *tor.Torrent, pth path.Path) (offset int64, length int64, etag string, err error) {
	var file *tor.Torfile

	if t.Files == nil {
		if len(pth) != 1 || pth[0] != t.Name {
			err = os.ErrNotExist
			return
		}
		offset = 0
		length = t.Pieces.Length()
	} else {
		for _, f := range t.Files {
			if pth.Equal(f.Path) {
				file = &f
				break
			}
		}

		if file == nil {
			err = os.ErrNotExist
			return
		}
		offset = file.Offset
		length = file.Length
	}
	etag = fmt.Sprintf("\"%v-%v\"", t.Hash.String(), offset)
	return
}
