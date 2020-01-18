// +build unix linux

package fuse

import (
	"context"
	"hash/fnv"
	"io"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"bazil.org/fuse"
	"bazil.org/fuse/fs"

	"storrent/hash"
	"storrent/tor"
)

func Serve(mountpoint string) error {
	conn, err := fuse.Mount(
		mountpoint,
		fuse.Subtype("storrent"),
		fuse.ReadOnly(),
	)
	if err != nil {
		return err
	}
	<-conn.Ready
	if conn.MountError != nil {
		conn.Close()
		return conn.MountError
	}

	go func(conn *fuse.Conn) {
		defer conn.Close()
		fs.Serve(conn, filesystem(0))
	}(conn)

	return conn.MountError
}

func Close(mountpoint string) error {
	return fuse.Unmount(mountpoint)
}

type filesystem int

func (fs filesystem) Root() (fs.Node, error) {
	return root(0), nil
}

// distinguish different torrents with same name
func fileInode(hash hash.Hash, name string) uint64 {
	h := fnv.New64a()
	h.Write(hash)
	h.Write([]byte(name))
	return h.Sum64()
}

type root int

func (dir root) Attr(ctx context.Context, a *fuse.Attr) error {
	a.Inode = 1
	a.Mode = os.ModeDir | 0555
	return nil
}

func (dir root) Lookup(ctx context.Context, name string) (fs.Node, error) {
	t := tor.GetByName(name)
	if t == nil {
		return nil, fuse.ENOENT
	}

	if t.Files == nil {
		return file{t: t}, nil
	}

	return directory{t: t}, nil
}

func (dir root) ReadDirAll(ctx context.Context) ([]fuse.Dirent, error) {
	ents := make([]fuse.Dirent, 0)
	tor.Range(func(h hash.Hash, t *tor.Torrent) bool {
		if t.InfoComplete() && t.Name != "" {
			tpe := fuse.DT_Dir
			if t.Files == nil {
				tpe = fuse.DT_File
			}
			ents = append(ents, fuse.Dirent{
				Name:  t.Name,
				Type:  tpe,
				Inode: fileInode(t.Hash, ""),
			})
		}
		return true
	})
	return ents, nil
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

type directory struct {
	t    *tor.Torrent
	name string
}

func (dir directory) Attr(ctx context.Context, a *fuse.Attr) error {
	a.Inode = fileInode(dir.t.Hash, dir.name)
	a.Mode = os.ModeDir | 0555
	if dir.t.CreationDate > 0 {
		a.Mtime = time.Unix(dir.t.CreationDate, 0)
		a.Ctime = time.Unix(dir.t.CreationDate, 0)
	}
	return nil
}

func (dir directory) Lookup(ctx context.Context, name string) (fs.Node, error) {
	if !dir.t.InfoComplete() {
		return nil, fuse.EIO
	}

	path := toPath(dir.name)
	for _, f := range dir.t.Files {
		if within(f.Path, path) && f.Path[len(path)] == name {
			n := name
			if dir.name != "" {
				n = dir.name + "/" + name
			}
			if len(f.Path) > len(path)+1 {
				return directory{dir.t, n}, nil
			} else {
				return file{dir.t, n}, nil
			}
		}
	}
	return nil, fuse.ENOENT
}

func (dir directory) ReadDirAll(ctx context.Context) ([]fuse.Dirent, error) {
	if !dir.t.InfoComplete() {
		return nil, fuse.EIO
	}

	path := toPath(dir.name)

	ents := make([]fuse.Dirent, 0)
	dirs := make(map[string]bool)
	for _, f := range dir.t.Files {
		if f.Padding {
			continue
		}
		if !within(f.Path, path) {
			continue
		}
		name := f.Path[len(path)]
		tpe := fuse.DT_File
		if len(f.Path) > len(path)+1 {
			if dirs[name] {
				continue
			}
			dirs[name] = true
			tpe = fuse.DT_Dir
		}
		ents = append(ents, fuse.Dirent{
			Name: name,
			Type: tpe,
			Inode: fileInode(dir.t.Hash,
				strings.Join(f.Path[:len(path)+1], "/")),
		})
	}
	return ents, nil
}

type file struct {
	t    *tor.Torrent
	name string
}

func findFile(t *tor.Torrent, name string) *tor.Torfile {
	for _, f := range t.Files {
		if strings.Join(f.Path, "/") == name {
			return &f
		}
	}
	return nil
}

func (file file) Attr(ctx context.Context, a *fuse.Attr) error {
	if !file.t.InfoComplete() {
		return fuse.EIO
	}

	var size uint64
	if file.t.Files == nil {
		if file.name != "" {
			return fuse.ENOENT
		}
		size = uint64(file.t.Pieces.Length())
	} else {
		f := findFile(file.t, file.name)
		if f == nil {
			return fuse.ENOENT
		}
		size = uint64(f.Length)
	}

	a.Inode = fileInode(file.t.Hash, file.name)
	a.Mode = 0444
	a.Size = size
	a.Blocks = (size + 511) / 512
	if file.t.CreationDate > 0 {
		a.Mtime = time.Unix(file.t.CreationDate, 0)
		a.Ctime = time.Unix(file.t.CreationDate, 0)
	}
	return nil
}

type handle struct {
	file   file
	reader *tor.Reader
}

var cachedmu sync.Mutex
var cached = make(map[string]hash.Hash)

func (file file) Open(ctx context.Context, req *fuse.OpenRequest, resp *fuse.OpenResponse) (fs.Handle, error) {
	if !req.Flags.IsReadOnly() {
		return nil, fuse.Errno(syscall.EACCES)
	}

	if !file.t.InfoComplete() {
		return nil, fuse.EIO
	}

	var offset, length int64

	if file.t.Files == nil {
		if file.name != "" {
			return nil, fuse.ENOENT
		}
		offset = 0
		length = file.t.Pieces.Length()
	} else {
		f := findFile(file.t, file.name)
		if f == nil {
			return nil, fuse.ENOENT
		}
		offset = f.Offset
		length = f.Length
	}
	reader := file.t.NewReader(context.Background(), offset, length)
	if reader == nil {
		return nil, fuse.EIO
	}
	n := file.t.Name + "/" + file.name

	cachedmu.Lock()
	defer cachedmu.Unlock()
	h, ok := cached[n]
	if ok && h.Equals(file.t.Hash) {
		resp.Flags |= fuse.OpenKeepCache
	} else {
		cached[n] = file.t.Hash
	}

	return handle{file, reader}, nil
}

func (handle handle) Read(ctx context.Context, req *fuse.ReadRequest, resp *fuse.ReadResponse) error {
	_, err := handle.reader.Seek(req.Offset, io.SeekStart)
	if err != nil {
		return err
	}

	handle.reader.SetContext(ctx)

	resp.Data = resp.Data[:req.Size]
	n, err := io.ReadFull(handle.reader, resp.Data)
	resp.Data = resp.Data[:n]
	if err == tor.ErrTorrentDead {
		err = fuse.ESTALE
	} else if err == io.ErrUnexpectedEOF {
		err = io.EOF
	}
	return err
}

func (handle handle) Release(ctx context.Context, req *fuse.ReleaseRequest) error {
	return handle.reader.Close()
}
