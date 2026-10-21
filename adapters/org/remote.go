package org

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/goxang/rig/api"
	"github.com/goxang/rig/protocol"
)

type answer struct {
	frame *protocol.Frame
	at    time.Time
	stale bool
}

// ponytail: answers live in this plugin process only; a restart while a child is down shows nothing for it until it is back
var (
	cacheMu sync.Mutex
	cache   = map[string]*answer{}
	clients sync.Map
)

func client(ch Child) *api.Client {
	key := ch.URL + "\x00" + ch.Token + "\x00" + ch.Env
	if c, ok := clients.Load(key); ok {
		return c.(*api.Client)
	}
	c, _ := clients.LoadOrStore(key, &api.Client{URL: strings.TrimSuffix(ch.URL, "/"), Token: ch.Token, Env: ch.Env, Fingerprint: ch.Fingerprint})
	return c.(*api.Client)
}

func plugin(ch Child) string { return first(ch.Plugin, "org") }

// child asks the child unit name for one of its unit/1 sources, through the cache: a fresh answer
// within the TTL, else a new one, else (the child is down) the last one, marked stale.
func (n *node) child(name, source string, args protocol.Args) (*protocol.Frame, error) {
	ch, ok := n.cfg.Children[name]
	if !ok {
		return nil, fmt.Errorf("%s is not a child of %s", name, n.cfg.Unit)
	}
	if ch.URL == "" {
		return nil, fmt.Errorf("children.%s has no url", name)
	}
	ttl := time.Minute
	if d, err := time.ParseDuration(n.cfg.Cache); err == nil {
		ttl = d
	}
	b, _ := json.Marshal(args)
	key := name + "\x00" + source + "\x00" + string(b)
	cacheMu.Lock()
	a := cache[key]
	cacheMu.Unlock()
	if a != nil && !a.stale && time.Since(a.at) < ttl {
		return a.frame, nil
	}
	f, err := client(ch).Query(n.ctx, plugin(ch)+"."+source, args)
	cacheMu.Lock()
	defer cacheMu.Unlock()
	if err != nil {
		if a != nil {
			a.stale = true
			return a.frame, nil
		}
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	cache[key] = &answer{frame: f, at: time.Now()}
	return f, nil
}

// cachedAt says when the child's answer to source was fetched and whether the child failed since.
func cachedAt(name, source string) (time.Time, bool) {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	a := cache[name+"\x00"+source+"\x00null"]
	if a == nil {
		return time.Time{}, false
	}
	return a.at, a.stale
}

// forget drops what a child answered, after a change there (a request sent).
func forget(name string) {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	for k := range cache {
		if strings.HasPrefix(k, name+"\x00") {
			delete(cache, k)
		}
	}
}
