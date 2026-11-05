package dnsproxy

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/Ati054/sb-gateway-openwrt/internal/dnsroute"
)

var errDNSRouteChanged = errors.New("DNS route generation changed")

type routeClient struct {
	stamp  dnsroute.Stamp
	client *http.Client
}

func usableRoute(stamp dnsroute.Stamp) bool {
	return stamp.Epoch != 0 && stamp.Tag != "" && stamp.Tag != "block" && stamp.Tag != "direct-wan"
}

// Called under the registry writer lock. No network wait; active streams keep
// their old client while its idle connections and queued jobs are retired.
func (s *server) routeChanged(selector string, stamp dnsroute.Stamp) func() {
	s.routeMu.Lock()
	defer s.routeMu.Unlock()
	if s.routeClients == nil {
		s.routeClients = make(map[string]routeClient)
	}
	previous := s.routeClients[selector]
	if previous.stamp == stamp {
		return nil
	}
	var closeIdle func()
	if previous.client != nil {
		if owner, ok := previous.client.Transport.(*recoveringTransport); ok {
			owner.mu.Lock()
			owner.closed = true
			transport := owner.current
			owner.mu.Unlock()
			closeIdle = transport.CloseIdleConnections
		} else {
			closeIdle = previous.client.CloseIdleConnections
		}
	}
	policy := strings.TrimPrefix(selector, "resproxy-")
	var client *http.Client
	if usableRoute(stamp) {
		template := s.proxy
		if specific := s.policyProxies[policy]; specific != nil {
			template = specific
		}
		if owner, ok := template.Transport.(*recoveringTransport); ok {
			owner.mu.Lock()
			transport := owner.current.Clone()
			owner.mu.Unlock()
			client = newDoHClient(transport)
		} else {
			// Production clients always own a recoveringTransport; retain this
			// branch for injected deterministic test transports only.
			copy := *template
			client = &copy
		}
	}
	s.routeClients[selector] = routeClient{stamp, client}
	s.cacheMu.Lock()
	for key, entry := range s.cache {
		if strings.HasPrefix(key, "proxy:"+policy+":") {
			s.removeCacheEntry(entry)
		}
	}
	s.cacheMu.Unlock()
	deliverQueued := s.requests().retireQueuedPath("proxy:" + policy)
	// TLS close_notify may write to a socket. Registry runs this cleanup only
	// after releasing its route lock, so cache/queries never wait for it.
	if deliverQueued == nil {
		return closeIdle
	}
	return func() {
		deliverQueued()
		if closeIdle != nil {
			closeIdle()
		}
	}
}

func (s *server) routeLookup(policy string, query []byte) (key string, client *http.Client, stamp dnsroute.Stamp, cached []byte) {
	selector := "resproxy-" + policy
	s.config.Routes.Read(selector, func(current dnsroute.Stamp) {
		stamp = current
		if !usableRoute(current) {
			return
		}
		s.routeMu.Lock()
		owner := s.routeClients[selector]
		s.routeMu.Unlock()
		if owner.stamp != current {
			return
		}
		client = owner.client
		key = fmt.Sprintf("proxy:%s:%d:", policy, current.Epoch) + string(query[2:])
		cached = s.cached(key, query[:2])
	})
	return
}

func (s *server) routeCurrent(policy string, stamp dnsroute.Stamp, use func()) bool {
	valid := false
	s.config.Routes.Read("resproxy-"+policy, func(current dnsroute.Stamp) {
		valid = usableRoute(current) && current == stamp
		if valid && use != nil {
			use()
		}
	})
	return valid
}

func (s *server) closeRouteClients() {
	s.routeMu.Lock()
	defer s.routeMu.Unlock()
	for id, route := range s.routeClients {
		if route.client != nil {
			route.client.CloseIdleConnections()
		}
		delete(s.routeClients, id)
	}
}
