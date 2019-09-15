package http

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"storrent/alloc"
	"storrent/config"
	"storrent/dht"
	"storrent/hash"
	"storrent/known"
	"storrent/peer"
	"storrent/tor"
	"storrent/tracker"
)

type handler struct {
	ctx context.Context
}

func NewHandler(ctx context.Context) http.Handler {
	return &handler{ctx}
}

func (handler *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	host, _, err := net.SplitHostPort(r.Host)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if host != "localhost" && net.ParseIP(host) == nil {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	path := r.URL.Path
	if path == "/" {
		root(handler.ctx, w, r)
		return
	}

	if len(path) < 41 {
		http.NotFound(w, r)
		return
	}

	hash := hash.Parse(path[1:41])
	if hash == nil {
		http.NotFound(w, r)
		return
	}

	if r.Method != "HEAD" && r.Method != "GET" {
		w.Header().Set("allow", "HEAD, GET")
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if len(path) == 41 {
		http.Redirect(w, r, path+"/", 301)
		return
	}

	if path[41] == '/' {
		err = r.ParseForm()
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if r.Form["playlist"] != nil {
			playlist(w, r, hash, toPath(path[42:]))
			return
		}
		if path[len(path)-1] == '/' {
			directory(w, r, hash, toPath(path[42:]))
			return
		} else {
			file(w, r, hash, toPath(path[42:]))
			return
		}
	}

	if path[41] == '.' {
		extension := path[42:]
		if extension == "torrent" {
			torfile(w, r)
			return
		} else if extension == "m3u" {
			playlist(w, r, hash, nil)
			return
		}
	}

	http.NotFound(w, r)
}

func getTorrent(ctx context.Context, data string) (*tor.Torrent, error) {
	t, err := tor.ReadMagnet(config.DefaultProxy(), data)
	if t != nil || err != nil {
		return t, err
	}
	return tor.GetTorrent(ctx, config.DefaultProxy(), data)
}

func root(serverctx context.Context, w http.ResponseWriter, r *http.Request) {
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
			http.Error(w, "Method not allowed", 405)
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
		data := strings.TrimSpace(r.FormValue("url"))
		if data == "" {
			http.Error(w, "No torrent supplied", http.StatusBadRequest)
			return
		}
		t, err := getTorrent(r.Context(), data)
		if t == nil || err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		_, err = tor.AddTorrent(serverctx, t)
		if err != nil && err != os.ErrExist {
			http.Error(w, err.Error(),
				http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/", 303)
		return
	} else if q == "delete" {
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
		http.Redirect(w, r, "/", 303)
		return
	} else if q == "set" {
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
			v, err := strconv.ParseFloat(idle, 64)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			config.SetIdleRate(v)
		}
		http.Redirect(w, r, "/", 303)
		return
	} else if q == "set-torrent" {
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
		conf := peer.TorConf{
			UseDht:      peer.ConfGet(r.Form.Get("use-dht") != ""),
			DhtPassive:  peer.ConfGet(r.Form.Get("dht-passive") != ""),
			UseTrackers: peer.ConfGet(r.Form.Get("use-trackers") != ""),
			UseWebseeds: peer.ConfGet(r.Form.Get("use-webseeds") != ""),
		}
		err = t.SetConf(conf)
		if err != nil {
			http.Error(w, err.Error(),
				http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/", 303)
		return
	} else {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
}

func less(a []string, b []string) bool {
	for i := range a {
		if i >= len(b) {
			return false
		}
		if a[i] < b[i] {
			return true
		}
		if a[i] > b[i] {
			return false
		}
	}

	return len(a) < len(b)
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

func toPath(s string) []string {
	path := strings.Split(s, "/")
	if len(path) > 0 && path[0] == "" {
		path = path[1:]
	}
	if len(path) > 0 && path[len(path)-1] == "" {
		path = path[0 : len(path)-1]
	}
	return path
}

func directory(w http.ResponseWriter, r *http.Request,
	hash hash.Hash, path []string) {

	ctx := r.Context()

	t := tor.Get(hash)
	if t == nil {
		http.NotFound(w, r)
		return
	}

	done := header(w, r, t.Name)
	if done {
		return
	}
	err := torrentEntry(ctx, w, t, path)
	if err != nil {
		return
	}
	footer(w)
}

func equal(p1 []string, p2 []string) bool {
	if len(p1) != len(p2) {
		return false
	}
	for i := range p1 {
		if p1[i] != p2[i] {
			return false
		}
	}
	return true
}

func within(path []string, begin []string) bool {
	if len(path) <= len(begin) {
		return false
	}
	for i := range begin {
		if path[i] != begin[i] {
			return false
		}
	}
	return true
}

func pathUrl(p []string) string {
	var b []byte
	for _, s := range p {
		t := url.PathEscape(s)
		b = append(b, t...)
		b = append(b, '/')
	}
	return string(b[0 : len(b)-1])
}

func torrentFile(w io.Writer, hash hash.Hash, path []string, length int64,
	available int) {
	p := pathUrl(path)
	fmt.Fprintf(w,
		"<tr><td><a href=\"/%v/%v\">%v</a></td>"+
			"<td>%v</td><td>%v</td></tr>\n",
		hash, p, html.EscapeString(strings.Join(path, "/")),
		length, available)
}

func torrentDir(w io.Writer, hash hash.Hash, path []string, lastdir []string) {
	var dir []string
	for i := 0; i < len(path) && i < len(lastdir); i++ {
		if path[i] != lastdir[i] {
			break
		}
		dir = append(dir, path[i])
	}
	for i := len(dir); i < len(path); i++ {
		dir = append(dir, path[i])
		p := pathUrl(dir)
		fmt.Fprintf(w,
			"<tr><td><a href=\"/%v/%v/\">%v/</a></td><td>"+
				"(<a href=\"/%v/%v/?playlist\">playlist</a>)"+
				"</td></tr>\n",
			hash, p, html.EscapeString(strings.Join(dir, "/")),
			hash, p)
	}
}

func torrentEntry(ctx context.Context, w http.ResponseWriter,
	t *tor.Torrent, dir []string) error {
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
		fmt.Fprintf(w, "%v bytes in %v+%v/%v pieces (%v bytes each), ",
			t.Pieces.Bytes(),
			c, t.Pieces.Count()-c,
			(t.Pieces.Length()+int64(t.Pieces.PieceSize())-1)/
				int64(t.Pieces.PieceSize()),
			t.Pieces.PieceSize())
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
			torrentFile(w, t.Hash, []string{t.Name},
				t.Pieces.Length(),
				available.AvailableRange(t,
					0, t.Pieces.Length()))
		}
	} else {
		a := make([]int, 0, len(t.Files))
		for i := range t.Files {
			if within(t.Files[i].Path, dir) {
				a = append(a, i)
			}
		}
		sort.Slice(a, func(i, j int) bool {
			return less(t.Files[a[i]].Path, t.Files[a[j]].Path)
		})
		lastdir := []string{}
		available, _ := t.GetAvailable()
		for _, i := range a {
			if err := ctx.Err(); err != nil {
				return err
			}
			f := t.Files[i]
			path := f.Path
			dir := path[:len(path)-1]
			if !equal(dir, lastdir) {
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
		var useDht, dhtPassive, useTrackers, useWebseeds string
		if conf.UseDht == peer.ConfTrue {
			useDht = " checked"
		}
		if conf.DhtPassive == peer.ConfTrue {
			dhtPassive = " checked"
		}
		if conf.UseTrackers == peer.ConfTrue {
			useTrackers = " checked"
		}
		if conf.UseWebseeds == peer.ConfTrue {
			useWebseeds = " checked"
		}
		fmt.Fprintf(w, "<form action=\"/?q=set-torrent\" method=\"post\">\n")
		if dht.Available() {
			fmt.Fprintf(w, "<input type=\"checkbox\" id=\"use-dht-%v\" name=\"use-dht\"%v/><label for=\"use-dht-%v\">Use DHT</label> ", t.Hash, useDht, t.Hash)
			fmt.Fprintf(w, "<input type=\"checkbox\" id=\"dht-passive-%v\" name=\"dht-passive\"%v/><label for=\"dht-passive-%v\">Passive DHT</label> ", t.Hash, dhtPassive, t.Hash)
		}
		fmt.Fprintf(w, "<input type=\"checkbox\" id=\"use-trackers-%v\" name=\"use-trackers\"%v/><label for=\"use-trackers-%v\">Use trackers (%v)</label> ", t.Hash, useTrackers, t.Hash, stats.NumTrackers)
		fmt.Fprintf(w, "<input type=\"checkbox\" id=\"use-webseeds-%v\" name=\"use-webseeds\"%v/><label for=\"use-webseeds-%v\">Use webseeds (%v)</label> ", t.Hash, useWebseeds, t.Hash, stats.NumWebseeds)
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

	fmt.Fprintf(w, "<form action=\"/?q=add\" method=\"post\">Magnet or URL: <input type=\"text\" name=\"url\"/> <input type=\"submit\"/></form> ")
	fmt.Fprintf(w, "<form action=\"/?q=set\" method=\"post\">Idle download: <input type=\"text\" name=\"idle\"/> Upload: <input type=\"text\" name=\"upload\"/> <input type=\"submit\"/></form>\n")

	fmt.Fprintf(w, "<p>Download %.0f/%.0f, upload %.0f/%.0f (unchoking %v), ",
		peer.DownloadEstimator.Estimate(), config.IdleRate(),
		peer.UploadEstimator.Estimate(), config.UploadRate(),
		peer.NumUnchoking())

	if dht.Available() {
		g4, g6, d4, d6, i4, i6 := dht.Count()
		fmt.Fprintf(w, "DHT %v+%v/%v %v+%v/%v, ",
			g4, i4, g4+d4,
			g6, i6, g6+d6)
	}
	fmt.Fprintf(w, "%v/%v bytes allocated.</p>\n",
		alloc.Bytes(), config.MemoryHighMark())

	var tors []*tor.Torrent
	tor.Range(func(k hash.Hash, t *tor.Torrent) bool {
		tors = append(tors, t)
		return true
	})
	sort.Slice(tors, func(i, j int) bool {
		return tors[i].Name < tors[j].Name ||
			(tors[i].Name == tors[j].Name &&
				bytes.Compare(tors[i].Hash, tors[j].Hash) < 0)
	})
	for _, t := range tors {
		err := torrentEntry(ctx, w, t, []string{})
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

	sort.Slice(ps, func(i, j int) bool {
		return bytes.Compare(ps[i].Id, ps[j].Id) < 0
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

	sort.Slice(kps, func(i, j int) bool {
		v41 := kps[i].Addr.IP.To4()
		v42 := kps[j].Addr.IP.To4()
		var a1, a2 []byte
		if v41 == nil && v42 != nil {
			return true
		} else if v41 != nil && v42 == nil {
			return false
		} else if v41 != nil && v42 != nil {
			a1 = v41
			a2 = v42
		} else {
			a1 = kps[i].Addr.IP.To16()
			a2 = kps[j].Addr.IP.To16()
		}
		c := bytes.Compare(a1, a2)
		if c < 0 {
			return true
		} else if c > 0 {
			return false
		}
		return kps[i].Addr.Port < kps[j].Addr.Port
	})
	fmt.Fprintf(w, "<p><table>\n")
	for _, k := range kps {
		hknown(w, &k, t)
	}
	fmt.Fprintf(w, "</table></p>\n")

	footer(w)
}

func parseId(id []byte) string {
	if id[0] == '-' && id[7] == '-' {
		return string(id[1:7])
	}
	return ""
}

func hpeer(w http.ResponseWriter, p *peer.Peer, t *tor.Torrent) {
	kp, _ := t.GetKnown(p.Id, p.IP, p.GetPort())
	var addr string
	if p.Port == 0 {
		if kp != nil {
			addr = kp.Addr.String()
		} else {
			addr = p.IP.String()
		}
	} else {
		a := net.TCPAddr{IP: p.IP, Port: p.GetPort()}
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
	var version string
	if kp != nil {
		version = kp.Version
	}
	if version == "" {
		version = parseId(p.Id)
	}
	fmt.Fprintf(w, "<td>%v</td>", html.EscapeString(version))
	fmt.Fprintf(w, "<td>%v</td></tr>", p.Id)
}

func recent(tm time.Time) bool {
	return time.Since(tm) < 35*time.Minute
}

func hknown(w http.ResponseWriter, kp *known.Peer, t *tor.Torrent) {
	buf := new(bytes.Buffer)
	if kp.Attempts > 0 {
		fmt.Fprintf(buf, ", %v", kp.Attempts)
	}
	if recent(kp.SeenTime) || recent(kp.ActiveTime) {
		fmt.Fprintf(buf, ", Seen")
	}
	if recent(kp.HeardTime) {
		fmt.Fprintf(buf, ", Heard")
	}
	if recent(kp.TrackerTime) {
		fmt.Fprintf(buf, ", T")
	}
	if recent(kp.DHTTime) {
		fmt.Fprintf(buf, ", DHT")
	}
	if recent(kp.PEXTime) {
		fmt.Fprintf(buf, ", PEX")
	}
	if kp.Bad() {
		fmt.Fprintf(buf, ", Bad")
	}
	var flags string
	if buf.Len() > 2 {
		flags = string(buf.Bytes()[2:])
	} else {
		flags = buf.String()
	}

	fmt.Fprintf(w, "<tr><td>%v</td><td>%v</td><td>%v</td><td>%v</td></tr>\n",
		kp.Addr.String(), flags, html.EscapeString(kp.Version), kp.Id)
}

func torfile(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if path[0] != '/' || len(path) <= 41 {
		http.NotFound(w, r)
		return
	}

	hash := hash.Parse(path[1:41])
	if hash == nil {
		http.NotFound(w, r)
		return
	}

	t := tor.Get(hash)
	if t == nil {
		http.NotFound(w, r)
		return
	}

	if !t.InfoComplete() {
		http.Error(w, "torrent metadata incomplete",
			http.StatusGatewayTimeout)
		return
	}

	w.Header().Set("content-type", "application/x-bittorrent")

	if r.Method == "HEAD" {
		return
	}

	err := tor.WriteTorrent(w, t)
	if err != nil {
		panic(http.ErrAbortHandler)
	}
}

func m3uentry(w http.ResponseWriter, host string, hash hash.Hash, path []string) {
	fmt.Fprintf(w, "#EXTINF:-1,%v\n",
		strings.Replace(path[len(path)-1], ",", "", -1))
	fmt.Fprintf(w, "http://%v/%v/%v\n",
		host, hash, pathUrl(path))
}

func playlist(w http.ResponseWriter, r *http.Request,
	hash hash.Hash, dir []string) {

	t := tor.Get(hash)
	if t == nil {
		http.NotFound(w, r)
		return
	}

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
			if within(f.Path, dir) {
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

	if r.Method == "HEAD" {
		return
	}

	fmt.Fprintf(w, "#EXTM3U\n")
	if t.Files == nil {
		m3uentry(w, r.Host, hash, []string{t.Name})
	} else {
		a := make([]int, len(t.Files))
		for i := range a {
			a[i] = i
		}
		sort.Slice(a, func(i, j int) bool {
			return less(t.Files[a[i]].Path,
				t.Files[a[j]].Path)
		})
		for _, i := range a {
			path := t.Files[i].Path
			if within(path, dir) {
				m3uentry(w, r.Host, hash, path)
			}
		}
	}
}

func file(w http.ResponseWriter, r *http.Request, hash hash.Hash, path []string) {
	t := tor.Get(hash)
	if t == nil {
		http.NotFound(w, r)
		return
	}

	if !t.InfoComplete() {
		http.Error(w, "torrent metadata incomplete", 504)
		return
	}

	offset, length, etag, err := fileParms(t, hash, path)
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

	w.Header().Set("etag", etag)

	reader := t.NewReader(r.Context(), offset, length)
	defer reader.Close()
	http.ServeContent(w, r, strings.Join(path, "/"), time.Time{}, reader)
}

func fileParms(t *tor.Torrent, hash hash.Hash,
	path []string) (offset int64, length int64, etag string, err error) {
	var file *tor.Torfile

	if t.Files == nil {
		if len(path) != 1 || path[0] != t.Name {
			err = os.ErrNotExist
			return
		}
		offset = 0
		length = t.Pieces.Length()
	} else {
		for _, f := range t.Files {
			if equal(path, f.Path) {
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
	etag = fmt.Sprintf("\"%v-%v\"", hash.String(), offset)
	return
}
