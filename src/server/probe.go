package server

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"
)

const (
	probeInterval      = 20 * time.Second
	probeTimeout       = 2 * time.Second
	probeWorkers       = 8
	probeTimeoutStatus = -1
	probeErrorStatus   = -2
)

func (n *navigator) runProbes(ctx context.Context) {
	tick := time.NewTicker(probeInterval)
	defer tick.Stop()
	for {
		n.probeAll(ctx, n.store.current.Load())
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-n.store.changed:
		}
	}
}

func (n *navigator) probeAll(ctx context.Context, c *catalog) {
	workers := min(len(c.apps), probeWorkers)
	var wg sync.WaitGroup
	for i := range workers {
		wg.Go(func() {
			for j := i; j < len(c.apps) && ctx.Err() == nil; j += workers {
				n.probe(ctx, c.apps[j])
			}
		})
	}
	wg.Wait()
}

func (n *navigator) probe(parent context.Context, a *app) {
	target := a.upstream()
	if target == nil {
		a.status.Store(probeErrorStatus)
		return
	}
	ctx, cancel := context.WithTimeout(parent, probeTimeout)
	defer cancel()
	// 直接取响应头，不发送凭据，也不跟随重定向。
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, target.String()+"/", nil)
	if err != nil {
		a.status.Store(probeErrorStatus)
		return
	}
	req.Host = a.host
	req.Header.Set("X-Forwarded-Proto", "https")
	res, err := n.transport.RoundTrip(req)
	if err == nil {
		res.Body.Close()
		a.status.Store(int32(res.StatusCode))
		return
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		a.status.Store(probeTimeoutStatus)
		return
	}
	a.status.Store(probeErrorStatus)
}
