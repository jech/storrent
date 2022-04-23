//go:build unix || linux
// +build unix linux

package fuse

import (
	"context"
	"hash/fnv"
	"io"
	"os"
	"syscall"
	"time"

	"bazil.org/fuse"
	"bazil.org/fuse/fs"

	"github.com/jech/storrent/hash"
	"github.com/jech/storrent/path"
	"github.com/jech/storrent/tor"
)

func Serve(mountpoint string) error {
	conn, err := fuse.Mount(
		mountpoint,
		fuse.Subtype("storrent"),
		fuse.ReadOnly(),
		fuse.MaxReadahead(128*1024),
		fuse.AsyncRead(),
	)
	if err != nil {
		return err
	}
	go func(conn *fuse.Conn) {
		defer conn.Close()
		fs.Serve(conn, filesystem(0))
	}(conn)

	return nil
}

func Close(mountpoint string) error {
	return fuse.Unmount(mountpoint)
}

type filesystem int

func (fs filesystem) Root() (fs.Node, error) {
	return root(0), nil
}

func fileInode(hash hash.Hash, path path.Path) uint64 {
	h := fnv.New64a()
	h.Write(hash)
	for _, n := range path {
		h.Write([]byte(n))
		h.Write([]byte{0})
	}
	return h.Sum64()
}

type root int

func setuid(a *fuse.Attr) {
	uid := os.Getuid()
	if uid >= 0 {
		a.Uid = uint32(uid)
	}
	gid := os.Getgid()
	if gid >= 0 {
		a.Gid = uint32(gid)
	}
}

func (dir root) Attr(ctx context.Context, a *fuse.Attr) error {
	a.Inode = 1
	a.Mode = os.ModeDir | 0555
	setuid(a)
	return nil
}

func (dir root) Lookup(ctx context.Context, name string) (fs.Node, error) {
	t := tor.GetByName(name)
	if t == nil {
		return nil, fuse.ENOENT
	}

	var h [20]byte
	copy(h[:], t.Hash)

	if t.Files == nil {
		return file{h, t.Name}, nil
	}

	return directory{hash: h}, nil
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
				Inode: fileInode(t.Hash, nil),
			})
		}
		return true
	})
	return ents, nil
}

type directory struct {
	hash [20]byte
	name string
}

func (dir directory) Hash() hash.Hash {
	h := hash.Hash(make([]byte, 20))
	copy(h, dir.hash[:])
	return h
}

func (dir directory) Attr(ctx context.Context, a *fuse.Attr) error {
	t := tor.Get(dir.Hash())
	if t == nil || !t.InfoComplete() {
		return fuse.ENOENT
	}

	a.Inode = fileInode(dir.Hash(), path.Parse(dir.name))
	a.Mode = os.ModeDir | 0555
	setuid(a)
	if t.CreationDate > 0 {
		a.Mtime = time.Unix(t.CreationDate, 0)
		a.Ctime = time.Unix(t.CreationDate, 0)
	}
	return nil
}

func (dir directory) Lookup(ctx context.Context, name string) (fs.Node, error) {
	t := tor.Get(dir.Hash())
	if t == nil || !t.InfoComplete() {
		return nil, fuse.ENOENT
	}

	var h [20]byte
	copy(h[:], t.Hash)

	pth := path.Parse(dir.name)
	for _, f := range t.Files {
		if f.Path.Within(pth) && f.Path[len(pth)] == name {
			p := append(path.Path(nil), pth...)
			p = append(p, name)
			filename := p.String()
			if len(f.Path) > len(pth)+1 {
				return directory{h, filename}, nil
			} else {
				return file{h, filename}, nil
			}
		}
	}
	return nil, fuse.ENOENT
}

func (dir directory) ReadDirAll(ctx context.Context) ([]fuse.Dirent, error) {
	t := tor.Get(dir.Hash())
	if t == nil || !t.InfoComplete() {
		return nil, fuse.ENOENT
	}

	pth := path.Parse(dir.name)

	ents := make([]fuse.Dirent, 0)

	ents = append(ents, fuse.Dirent{
		Name:  ".",
		Type:  fuse.DT_Dir,
		Inode: fileInode(t.Hash, pth),
	})
	parentInode := uint64(1)
	if len(pth) > 0 {
		parentInode = fileInode(t.Hash, pth[:len(pth)-1])
	}
	ents = append(ents, fuse.Dirent{
		Name:  "..",
		Type:  fuse.DT_Dir,
		Inode: parentInode,
	})

	dirs := make(map[string]bool)
	for _, f := range t.Files {
		if f.Padding {
			continue
		}
		if !f.Path.Within(pth) {
			continue
		}
		name := f.Path[len(pth)]
		tpe := fuse.DT_File
		if len(f.Path) > len(pth)+1 {
			if dirs[name] {
				continue
			}
			dirs[name] = true
			tpe = fuse.DT_Dir
		}
		ents = append(ents, fuse.Dirent{
			Name:  name,
			Type:  tpe,
			Inode: fileInode(t.Hash, f.Path[:len(pth)+1]),
		})
	}
	return ents, nil
}

type file struct {
	hash [20]byte
	name string
}

func (file file) Hash() hash.Hash {
	h := hash.Hash(make([]byte, 20))
	copy(h, file.hash[:])
	return h
}

func findFile(t *tor.Torrent, path path.Path) *tor.Torfile {
	for _, f := range t.Files {
		if path.Equal(f.Path) {
			return &f
		}
	}
	return nil
}

func (file file) Attr(ctx context.Context, a *fuse.Attr) error {
	t := tor.Get(file.Hash())
	if t == nil || !t.InfoComplete() {
		return fuse.ENOENT
	}

	pth := path.Parse(file.name)

	var size uint64
	if t.Files == nil {
		size = uint64(t.Pieces.Length())
	} else {
		f := findFile(t, pth)
		if f == nil {
			return fuse.ENOENT
		}
		size = uint64(f.Length)
	}

	a.Inode = fileInode(t.Hash, pth)
	a.Mode = 0444
	setuid(a)
	a.Size = size
	a.Blocks = (size + 511) / 512
	if t.CreationDate > 0 {
		a.Mtime = time.Unix(t.CreationDate, 0)
		a.Ctime = time.Unix(t.CreationDate, 0)
	}
	return nil
}

type handle struct {
	file file

	// using a semaphore here gives better ordering than a mutex.
	// Another benefit is to avoid polluting the mutex profile.
	sema   chan struct{}
	reader *tor.Reader
}

func (file file) Open(ctx context.Context, req *fuse.OpenRequest, resp *fuse.OpenResponse) (fs.Handle, error) {
	t := tor.Get(file.Hash())
	if t == nil || !t.InfoComplete() {
		return nil, fuse.ENOENT
	}

	if !req.Flags.IsReadOnly() {
		return nil, fuse.Errno(syscall.EACCES)
	}

	var offset, length int64

	if t.Files == nil {
		offset = 0
		length = t.Pieces.Length()
	} else {
		f := findFile(t, path.Parse(file.name))
		if f == nil {
			return nil, fuse.ENOENT
		}
		offset = f.Offset
		length = f.Length
	}
	reader := t.NewReader(context.Background(), offset, length)
	if reader == nil {
		return nil, fuse.EIO
	}

	resp.Flags |= fuse.OpenKeepCache

	return &handle{
		file:   file,
		reader: reader,
		sema:   make(chan struct{}, 1),
	}, nil
}

func (handle *handle) Read(ctx context.Context, req *fuse.ReadRequest, resp *fuse.ReadResponse) error {
	select {
	case <-ctx.Done():
		return fuse.EINTR
	case handle.sema <- struct{}{}:
	}
	defer func() {
		<-handle.sema
	}()

	_, err := handle.reader.Seek(req.Offset, io.SeekStart)
	if err != nil {
		return err
	}

	handle.reader.SetContext(ctx)
	defer handle.reader.SetContext(context.Background())

	resp.Data = resp.Data[:req.Size]
	n, err := io.ReadFull(handle.reader, resp.Data)
	resp.Data = resp.Data[:n]
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		err = nil
	}
	return err
}

func (handle *handle) Release(ctx context.Context, req *fuse.ReleaseRequest) error {
	handle.sema <- struct{}{}
	defer func() {
		<-handle.sema
	}()

	err := handle.reader.Close()
	handle.reader = nil
	return err
}
