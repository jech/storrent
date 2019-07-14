package webseed

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	nurl "net/url"
	"strconv"

	"storrent/httpclient"
)

type Hoffman struct {
	base
}

func (ws *Hoffman) Get(ctx context.Context, proxy string, hash []byte,
	index, offset, length uint32, w io.Writer) (int64, error) {
	ws.start()
	defer ws.stop()

	url, err := nurl.Parse(ws.url)
	if err != nil {
		return 0, err
	}

	v := nurl.Values{}
	v.Add("info_hash", string(hash))
	v.Add("piece", fmt.Sprintf("%v", index))
	v.Add("ranges", fmt.Sprintf("%v-%v", offset, offset+length))
	url.RawQuery = v.Encode()

	req, err := http.NewRequest("GET", url.String(), nil)
	if err != nil {
		ws.error(true)
		return 0, err
	}
	req.Header["User-Agent"] = nil

	client := httpclient.Get(proxy)
	if client == nil {
		return 0, errors.New("couldn't get HTTP client")
	}

	r, err := client.Do(req.WithContext(ctx))
	if err != nil {
		ws.error(true)
		return 0, err
	}
	defer r.Body.Close()

	if r.StatusCode != http.StatusOK {
		ws.error(true)
		return 0, errors.New(r.Status)
	}

	l := int64(-1)
	cl := r.Header.Get("Content-Length")
	if cl != "" {
		var err error
		l, err = strconv.ParseInt(cl, 10, 64)
		if err != nil {
			ws.error(true)
			return 0, err
		}
		if l != int64(length) {
			ws.error(true)
			return 0, errors.New("length mismatch")
		}
	}

	reader := io.Reader(r.Body)
	if l < 0 {
		reader = io.LimitReader(r.Body, int64(length))
	}

	n, err := io.Copy(w, reader)
	ws.Accumulate(int(n))
	if n > 0 {
		ws.error(false)
	}
	return n, nil
}
