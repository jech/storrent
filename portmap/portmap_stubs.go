// +build !cgo nonatpmp

package portmap

import (
	"context"
	"errors"
)

const Do = false

func Map(ctx context.Context) error {
	return errors.New("Port-mapping not supported in this build")
}

