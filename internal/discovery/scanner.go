package discovery

import (
	"cmp"
	"context"
	"net/netip"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultPorts são as portas RTSP mais comuns em câmeras IP domésticas.
var DefaultPorts = []int{554, 8554, 10554, 5554, 7447}

// Options controla um scan.
type Options struct {
	Prefixes []netip.Prefix
	Ports    []int
	Timeout  time.Duration
	Workers  int

	// OnFound e OnProgress são chamados de várias goroutines.
	OnFound    func(Device)
	OnProgress func(done, total int)
}

type target struct {
	addr netip.Addr
	port int
}

// Scan sonda todos os pares endereço/porta dos prefixos e devolve os
// dispositivos RTSP encontrados, ordenados por endereço.
func Scan(ctx context.Context, opts Options) ([]Device, error) {
	ports := opts.Ports
	if len(ports) == 0 {
		ports = DefaultPorts
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	workers := opts.Workers
	if workers <= 0 {
		workers = 256
	}

	var targets []target
	for _, prefix := range opts.Prefixes {
		addrs, err := Hosts(prefix)
		if err != nil {
			return nil, err
		}
		for _, addr := range addrs {
			for _, port := range ports {
				targets = append(targets, target{addr, port})
			}
		}
	}
	if len(targets) == 0 {
		return nil, nil
	}
	if workers > len(targets) {
		workers = len(targets)
	}

	queue := make(chan target)
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		found   []Device
		done    atomic.Int64
		scanned = len(targets)
	)

	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for t := range queue {
				dev, err := Probe(ctx, t.addr.String(), t.port, timeout)
				if opts.OnProgress != nil {
					opts.OnProgress(int(done.Add(1)), scanned)
				}
				if err != nil || dev == nil {
					continue
				}
				mu.Lock()
				found = append(found, *dev)
				mu.Unlock()
				if opts.OnFound != nil {
					opts.OnFound(*dev)
				}
			}
		}()
	}

	for _, t := range targets {
		select {
		case queue <- t:
		case <-ctx.Done():
			close(queue)
			wg.Wait()
			return sortDevices(found), ctx.Err()
		}
	}
	close(queue)
	wg.Wait()
	return sortDevices(found), nil
}

func sortDevices(devs []Device) []Device {
	slices.SortFunc(devs, func(a, b Device) int {
		ap, _ := netip.ParseAddrPort(a.Address)
		bp, _ := netip.ParseAddrPort(b.Address)
		if c := ap.Addr().Compare(bp.Addr()); c != 0 {
			return c
		}
		return cmp.Compare(ap.Port(), bp.Port())
	})
	return devs
}
