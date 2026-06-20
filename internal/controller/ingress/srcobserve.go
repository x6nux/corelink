package ingress

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"

	genv1 "github.com/x6nux/corelink/pkg/proto/gen"
	"google.golang.org/grpc/peer"
)

// errNoPeer is returned when the gRPC peer (and thus the caller's source
// address) cannot be determined from the context.
var errNoPeer = errors.New("no peer address in context")

// sourceAddrFromContext extracts the caller's source address from the gRPC peer
// stored in ctx and returns it as a genv1.SourceAddr (host + numeric port).
//
// It is factored out of ObserveSource so that tests can inject a fake source
// address via peer.NewContext without standing up a real gRPC connection.
func sourceAddrFromContext(ctx context.Context) (*genv1.SourceAddr, error) {
	p, ok := peer.FromContext(ctx)
	if !ok || p == nil || p.Addr == nil {
		return nil, errNoPeer
	}

	host, portStr, err := net.SplitHostPort(p.Addr.String())
	if err != nil {
		return nil, fmt.Errorf("split peer addr %q: %w", p.Addr.String(), err)
	}
	port, err := strconv.ParseUint(portStr, 10, 32)
	if err != nil {
		return nil, fmt.Errorf("parse peer port %q: %w", portStr, err)
	}

	return &genv1.SourceAddr{Host: host, Port: uint32(port)}, nil
}
