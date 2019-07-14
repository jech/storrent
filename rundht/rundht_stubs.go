// +build !cgo

package rundht

import (
	"context"
	"net"
)

func Read(filename string) ([]byte, []net.TCPAddr, error) {
	return nil, nil, nil
}

func Run(ctx context.Context, id []byte, port int) (<-chan struct{}, error) {
	return nil, nil
}

func Handle(dhtevent <-chan struct{}) {
	return
}

func Bootstrap(ctx context.Context, nodes []net.TCPAddr) {
	return
}

func Write(filename string, id []byte) error {
	return nil
}

