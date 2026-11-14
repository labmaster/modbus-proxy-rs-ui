package main

// Client hostnames via reverse DNS (in a home network usually the router).
// Results are cached so that the connections page (refreshed every 3 s)
// does not constantly send DNS queries.

import (
	"context"
	"net"
	"strings"
	"sync"
	"time"
)

const (
	hostCacheOK   = 10 * time.Minute
	hostCacheFail = 2 * time.Minute
	hostLookupMax = 800 * time.Millisecond
)

type hostEntry struct {
	name    string
	expires time.Time
}

type hostCache struct {
	mu sync.Mutex
	m  map[string]hostEntry
}

func newHostCache() *hostCache { return &hostCache{m: map[string]hostEntry{}} }

// lookupAll resolves all IPs in parallel and returns IP -> hostname ("" = unknown).
func (c *hostCache) lookupAll(ips []string) map[string]string {
	res := map[string]string{}
	var todo []string
	now := time.Now()
	c.mu.Lock()
	for _, ip := range ips {
		if e, ok := c.m[ip]; ok && now.Before(e.expires) {
			res[ip] = e.name
		} else {
			todo = append(todo, ip)
		}
	}
	if len(c.m) > 1000 { // limit memory
		for k, e := range c.m {
			if now.After(e.expires) {
				delete(c.m, k)
			}
		}
	}
	c.mu.Unlock()

	var wg sync.WaitGroup
	var rmu sync.Mutex
	for _, ip := range todo {
		wg.Add(1)
		go func(ip string) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), hostLookupMax)
			defer cancel()
			name := ""
			if names, err := net.DefaultResolver.LookupAddr(ctx, ip); err == nil && len(names) > 0 {
				name = strings.TrimSuffix(names[0], ".")
			}
			ttl := hostCacheOK
			if name == "" {
				ttl = hostCacheFail
			}
			c.mu.Lock()
			c.m[ip] = hostEntry{name: name, expires: time.Now().Add(ttl)}
			c.mu.Unlock()
			rmu.Lock()
			res[ip] = name
			rmu.Unlock()
		}(ip)
	}
	wg.Wait()
	return res
}
