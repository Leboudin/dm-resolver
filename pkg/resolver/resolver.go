package resolver

import (
	"log"
	"net"
	"sort"
	"sync"
	"time"

	"github.com/cperez08/dm-resolver/pkg/list"
	"google.golang.org/grpc/resolver"
)

// DomainResolver is a custom resolver library that helps to resolve a
// domain returning a list of IPs associated with it, also with posibilty to watch for DNS
// changes, the library can be used either by the resolver builder
// for gRPC or as a independent library also implement resolver.Resolver
type DomainResolver struct {
	m           sync.Mutex
	cc          resolver.ClientConn
	target      resolver.Target
	ticker      *time.Ticker
	Addresses   []string
	isDone      chan bool
	needWatcher bool // indicates if the library needs to watch for domain changes
	address     string
	port        string
	updateState bool      // false when the library is used outside gRPC context
	listener    chan bool // lister that can be used to watch changes in the Address list
	needLookup  bool      // indicates if need to look up for new ips in the watcher, no valid for address type IP
}

// NewResolver creates a new resolver instance, if needWatcher is true
// a time in seconds is expected in the refreshRate parameter
// the ticker field is exported in case want to be updated or stoped
func NewResolver(address, port string, needWatcher bool, refreshRate *time.Duration, listener chan bool) *DomainResolver {
	d := &DomainResolver{address: address, port: port, updateState: false}
	if net.ParseIP(address) != nil {
		d.Addresses = append(d.Addresses, address)
		d.needLookup = false
	} else {
		d.needLookup = true
		d.listener = listener
		if needWatcher {
			d.needWatcher = true
			d.ticker = time.NewTicker(time.Second * (*refreshRate))
			d.isDone = make(chan bool)
		}
	}

	return d
}

// StartResolver resolves by first time the given domain
func (r *DomainResolver) StartResolver() {
	if !r.needLookup {
		addrs := []resolver.Address{{Addr: r.Addresses[0]}}
		if r.updateState {
			r.cc.UpdateState(resolver.State{Addresses: addrs})
		}
		return
	}

	addrs := r.resolve()
	for _, a := range addrs {
		r.Addresses = append(r.Addresses, a.Addr)
	}

	if r.needWatcher {
		go r.watch()
	}

	sort.Strings(r.Addresses)
	if r.updateState {
		r.cc.UpdateState(resolver.State{Addresses: addrs}) // update the state in the start, only gRPC
	}
}

// ResolveNow is empty since we are going to rely on our own ticker
// to standardise the refresh rate
func (r *DomainResolver) ResolveNow(o resolver.ResolveNowOptions) {
	// st, apply := r.getState()
	// if apply && r.updateState {
	// 	r.cc.UpdateState(st)
	// }
}

// Close stops watching for changes in the domain
func (r *DomainResolver) Close() {
	if r.isDone != nil && r.needWatcher {
		r.isDone <- true
	}
}

// GetNewState get a new resolver state
func (r *DomainResolver) getState() (_ resolver.State, isUpdated bool) {
	addrs := r.resolve()

	r.m.Lock()
	defer r.m.Unlock()
	addrstr := list.FromAddrToString(addrs)

	// experimental, let's skip changes in case of 0 records,
	// to avoid cleaning state in case of errors
	if len(addrstr) == 0 {
		return resolver.State{}, false
	}

	if hasDiff := list.CompareListStr(r.Addresses, addrstr); !hasDiff {
		return resolver.State{}, false
	}

	r.Addresses = addrstr

	if r.listener != nil {
		// let know to the listener the Addresses were updated
		r.listener <- true
	}

	return resolver.State{Addresses: addrs}, true
}

// resolve resolves the domain looking for
// the Ipv4 and Ipv6 records
func (r *DomainResolver) resolve() []resolver.Address {
	addrs := []resolver.Address{}
	if r.needLookup {
		ips := lookUpByIP(r.address)
		for _, ip := range ips {
			addr := ip + ":" + r.port
			addrs = append(addrs, resolver.Address{Addr: addr})
		}
	}

	return addrs
}

// watch watches every X secods for changes in the domain
// in order to update the state if enabled
func (r *DomainResolver) watch() {
	for {
		select {
		case <-r.isDone:
			r.ticker.Stop()
			return
		case <-r.ticker.C:
			st, apply := r.getState()
			if apply && r.updateState { // only applicable for gRPC
				r.cc.UpdateState(st)
			}
		}
	}
}

// lookUpByIP ...
func lookUpByIP(host string) []string {
	ips, err := net.LookupIP(host)
	if err != nil {
		log.Println("[grpc-resolver]: error looking up for ips ", err)
		return []string{}
	}

	return pushRecords(ips)
}

func pushRecords(ips []net.IP) []string {
	records := []string{}
	for _, ip := range ips {
		if ip.To4() != nil {
			records = append(records, ip.String())
		} else {
			records = append(records, "["+ip.String()+"]")
		}
	}

	return records
}
