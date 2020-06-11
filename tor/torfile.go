package tor

import (
	"context"
	"crypto/sha1"
	"errors"
	"fmt"
	"github.com/zeebo/bencode"
	"io"
	"net/http"
	nurl "net/url"
	"strings"
	"sync/atomic"

	"github.com/jech/storrent/config"
	"github.com/jech/storrent/hash"
	"github.com/jech/storrent/httpclient"
	"github.com/jech/storrent/tracker"
	"github.com/jech/storrent/webseed"
)

type BTorrent struct {
	Info         bencode.RawMessage `bencode:"info"`
	CreationDate int64              `bencode:"creation date,omitempty"`
	Announce     string             `bencode:"announce,omitempty"`
	AnnounceList [][]string         `bencode:"announce-list,omitempty"`
	URLList      listOrString       `bencode:"url-list,omitempty"`
	HTTPSeeds    listOrString       `bencode:"httpseeds,omitempty"`
}

type BInfo struct {
	Name        string  `bencode:"name"`
	Name8       string  `bencode:"name.utf-8,omitempty"`
	PieceLength uint32  `bencode:"piece length"`
	Pieces      []byte  `bencode:"pieces"`
	Length      int64   `bencode:"length"`
	Files       []BFile `bencode:"files,omitempty"`
}

type BFile struct {
	Path   []string `bencode:"path"`
	Path8  []string `bencode:"path.utf-8,omitempty"`
	Length int64    `bencode:"length"`
	Attr   string   `bencode:"attr,omitempty"`
}

// Always encodes as a list
type listOrString []string

func (ls *listOrString) UnmarshalBencode(v []byte) error {
	var s string
	err := bencode.DecodeBytes(v, &s)
	if err == nil {
		*ls = []string{s}
		return nil
	}
	var l []string
	err = bencode.DecodeBytes(v, &l)
	if err != nil {
		return err
	}
	*ls = l
	return nil
}

func webseedList(urls []string, getright bool) []webseed.Webseed {
	var l []webseed.Webseed
	for _, u := range urls {
		ws := webseed.New(u, getright)
		if ws == nil {
			continue
		}
		l = append(l, ws)
	}
	return l
}

type UnknownSchemeError struct {
	Scheme string
}

func (e UnknownSchemeError) Error() string {
	if e.Scheme == "" {
		return "missing scheme"
	}
	return fmt.Sprintf("unknown scheme %v", e.Scheme)
}

type ParseURLError struct {
	Err error
}

func (e ParseURLError) Error() string {
	return e.Err.Error()
}

func (e ParseURLError) Unwrap() error {
	return e.Err
}

func GetTorrent(ctx context.Context, proxy string, url string) (*Torrent, error) {
	u, err := nurl.Parse(url)
	if err == nil && u.Scheme != "http" && u.Scheme != "https" {
		err = UnknownSchemeError{u.Scheme}
	}
	if err != nil {
		return nil, ParseURLError{err}
	}

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header["User-Agent"] = nil
	req.Close = true

	client := httpclient.Get(proxy)
	if client == nil {
		return nil, errors.New("couldn't create HTTP client")
	}

	r, err := client.Do(req.WithContext(ctx))
	if err != nil {
		return nil, err
	}
	defer r.Body.Close()

	if r.StatusCode != 200 {
		return nil, errors.New("upstream server returned: " + r.Status)
	}

	t, err := ReadTorrent(proxy, r.Body)
	if err != nil {
		return nil, err
	}
	return t, nil
}

func ReadTorrent(proxy string, r io.Reader) (*Torrent, error) {
	decoder := bencode.NewDecoder(r)
	var torrent BTorrent
	err := decoder.Decode(&torrent)
	if err != nil {
		return nil, err
	}
	if torrent.Info == nil {
		return nil, errors.New("couldn't find info")
	}

	var announce [][]tracker.Tracker
	if torrent.AnnounceList != nil {
		announce =
			make([][]tracker.Tracker, len(torrent.AnnounceList))
		for i, v := range torrent.AnnounceList {
			for _, w := range v {
				tr := tracker.New(w)
				if tr != nil {
					announce[i] = append(announce[i], tr)
				}
			}
		}
	} else if torrent.Announce != "" {
		tr := tracker.New(torrent.Announce)
		if tr != nil {
			announce = [][]tracker.Tracker{[]tracker.Tracker{tr}}
		}
	}

	webseeds := webseedList(torrent.URLList, true)
	webseeds = append(webseeds, webseedList(torrent.HTTPSeeds, false)...)

	hsh := sha1.Sum(torrent.Info)
	h := hash.Hash(hsh[:])

	t, err := New(proxy, h, "", torrent.Info, torrent.CreationDate,
		announce, webseeds)
	if err != nil {
		return nil, err
	}

	err = t.Complete()
	if err != nil {
		t.Kill(context.Background())
		return nil, err
	}

	return t, nil
}

func (torrent *Torrent) Complete() error {
	var info BInfo
	err := bencode.DecodeBytes(torrent.Info, &info)
	if err != nil {
		return err
	}

	if len(info.Pieces)%20 != 0 {
		return errors.New("pieces has an odd size")
	}
	if info.PieceLength%config.ChunkSize != 0 {
		return errors.New("odd sized piece")
	}
	hashes := make([]hash.Hash, 0, len(info.Pieces)/20)
	for i := 0; i < len(info.Pieces)/20; i++ {
		hashes = append(hashes, info.Pieces[i*20:(i+1)*20])
	}

	var files []Torfile

	var length int64
	if info.Length > 0 {
		if info.Files != nil {
			return errors.New("both length and files")
		}
		length = info.Length
	} else {
		if info.Files == nil {
			return errors.New("neither length nor files")
		}
		length = 0
		for _, f := range info.Files {
			path := f.Path8
			if path == nil {
				path = f.Path
			}
			if path == nil {
				return errors.New("file has no path")
			}
			files = append(files,
				Torfile{Path: path,
					Offset:  length,
					Length:  f.Length,
					Padding: strings.Contains(f.Attr, "p")})
			length += f.Length
		}
	}

	chunks := (length + int64(config.ChunkSize) - 1) /
		int64(config.ChunkSize)
	if chunks != int64(uint32(chunks)) || chunks != int64(int(chunks)) {
		return errors.New("torrent too large")
	}
	torrent.inFlight = make([]uint8, chunks)

	torrent.PieceHashes = hashes
	// torrent.Name may have been populated earlier
	if info.Name8 != "" {
		torrent.Name = info.Name8
	} else {
		torrent.Name = info.Name
	}
	if torrent.Name == "" {
		return errors.New("torrent has no name")
	}
	torrent.Pieces.Complete(info.PieceLength, length)
	torrent.Files = files

	atomic.StoreUint32(&torrent.infoComplete, 1)

	return nil
}

func ReadMagnet(proxy string, m string) (*Torrent, error) {
	h := hash.Parse(m)
	var dn string
	var announce [][]tracker.Tracker
	var webseeds []webseed.Webseed
	if h == nil {
		url, err := nurl.Parse(m)
		if err != nil {
			return nil, nil
		}
		if url.Scheme != "magnet" {
			return nil, nil
		}
		q := url.Query()
		if q == nil {
			return nil, errors.New("couldn't parse magnet link")
		}
		xt := q["xt"]
		for _, v := range xt {
			if strings.HasPrefix(v, "urn:btih:") {
				hh := v[9:]
				h = hash.Parse(hh)
				if h != nil {
					break
				}
			}
		}
		if h == nil {
			return nil, errors.New("couldn't find btih field")
		}
		tr := q["tr"]
		for _, v := range tr {
			t := tracker.New(v)
			if t != nil {
				announce = append(announce,
					[]tracker.Tracker{t})
			}
		}
		as := q["as"]
		webseeds = append(webseeds, webseedList(as, true)...)
		ws := q["ws"]
		webseeds = append(webseeds, webseedList(ws, true)...)
		dn = q.Get("dn")
	}
	return New(proxy, h, dn, nil, 0, announce, webseeds)
}

func WriteTorrent(w io.Writer, t *Torrent) error {
	encoder := bencode.NewEncoder(w)
	var a string
	var al [][]string

	as := make([][]string, len(t.trackers))
	for i, v := range t.trackers {
		as[i] = make([]string, len(v))
		for j, w := range v {
			as[i][j] = w.URL()
		}
	}

	if len(t.trackers) == 1 && len(t.trackers[0]) == 1 {
		a = as[0][0]
	} else {
		if len(t.trackers) > 0 && len(t.trackers[0]) > 0 {
			a = as[0][0]
		}
		al = as
	}

	var ul []string
	var hs []string
	for _, ws := range t.webseeds {
		switch ws.(type) {
		case *webseed.GetRight:
			ul = append(ul, ws.URL())
		case *webseed.Hoffman:
			hs = append(hs, ws.URL())
		default:
			panic("Eek")
		}
	}

	return encoder.Encode(&BTorrent{
		Info:         t.Info,
		CreationDate: t.CreationDate,
		Announce:     a,
		AnnounceList: al,
		URLList:      ul,
		HTTPSeeds:    hs,
	})
}
