//go:build !unix

package daemon

import "context"

func lockMachineProxy(context.Context, string) (func(), error) { return nil, ErrMachineProxyInvalid }
