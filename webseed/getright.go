package webseed

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"storrent/httpclient"
)

type GetRight struct {
	base
}

var ErrParse = errors.New("parse error")

type ErrRange struct {
	url string
}

func (err ErrRange) Error() string {
	return fmt.Sprintf("server didn't honour range request (%v)", err.url)
}

type ErrMismatch struct {
	url      string
	got      int64
	expected int64
}

func (err ErrMismatch) Error() string {
	return fmt.Sprintf("size mismatch (%v, got %d, expected %d)",
		err.url, err.got, err.expected)
}

func parseContentRange(cr string) (offset int64, length int64, fl int64,
	err error) {
	offset = -1
	length = -1
	fl = -1

	var end int64
	n, err := fmt.Sscanf(cr, "bytes %d-%d/%d\n", &offset, &end, &fl)
	if err == nil {
		if n != 3 || end < offset || end >= fl {
			err = ErrParse
			return
		}
		length = end - offset + 1
		return
	}

	n, err = fmt.Sscanf(cr, "bytes %d-%d/*\n", &offset, &end)
	if err == nil {
		if n != 2 || end < offset {
			err = ErrParse
			return
		}
		length = end - offset + 1
		return
	}

	n, err = fmt.Sscanf(cr, "bytes */%d\n", &fl)
	if err == nil {
		if n != 1 {
			err = ErrParse
			return
		}
		return
	}

	err = ErrParse
	return
}

func buildUrl(url string, name string, file []string) string {
	if !strings.HasSuffix(url, "/") {
		if file == nil {
			return url
		}
		url = url + "/"
	}

	url = url + name
	if file == nil {
		return url
	}

	if !strings.HasSuffix(url, "/") {
		url = url + "/"
	}

	url = url + strings.Join(file, "/")
	return url
}

func (ws *GetRight) Get(ctx context.Context, proxy string,
	name string, file []string, flength, offset, length int64,
	w io.Writer) (int64, error) {
	ws.start()
	defer ws.stop()

	url := buildUrl(ws.url, name, file)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		ws.error(true)
		return 0, err
	}

	req.Header.Set("Range",
		fmt.Sprintf("bytes=%d-%d", offset, offset+int64(length)-1))
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

	fl := int64(-1)
	l := int64(-1)
	if r.StatusCode == http.StatusOK {
		if offset != 0 {
			ws.error(true)
			return 0, ErrRange{strings.Join(file, "/")}
		}
		cl := r.Header.Get("Content-Length")
		if cl != "" {
			var err error
			fl, err = strconv.ParseInt(cl, 10, 64)
			if err != nil {
				return 0, err
			}
			l = fl
		}
	} else if r.StatusCode == http.StatusPartialContent {
		rng := r.Header.Get("Content-Range")
		if rng == "" {
			ws.error(true)
			return 0, errors.New("206 with no Content-Range")
		}
		var o int64
		o, l, fl, err = parseContentRange(rng)
		if err != nil {
			ws.error(true)
			return 0, err
		}
		if o != offset {
			ws.error(true)
			return 0, ErrRange{strings.Join(file, "/")}
		}
	} else if r.StatusCode == http.StatusRequestedRangeNotSatisfiable {
		var err error
		rng := r.Header.Get("Content-Range")
		if rng != "" {
			_, _, fl, err2 := parseContentRange(rng)
			if err2 == nil {
				err = ErrMismatch{url, fl, flength}
			}
		}
		if err == nil {
			err = errors.New(r.Status)
		}
		ws.error(true)
		return 0, err
	} else {
		ws.error(true)
		return 0, errors.New(r.Status)
	}

	if l < 0 {
		l = flength - offset
	}

	if fl >= 0 {
		if fl != flength {
			ws.error(true)
			return 0, ErrMismatch{url, fl, flength}
		}
	}

	reader := io.Reader(r.Body)
	if l > length {
		reader = io.LimitReader(reader, length)
	}

	n, err := io.Copy(w, reader)
	ws.Accumulate(int(n))
	if n > 0 {
		ws.error(false)
	}
	return n, nil
}
