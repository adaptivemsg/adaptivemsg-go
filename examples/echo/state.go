package echo

import (
	"sync"
	"sync/atomic"
)

type StatMgr struct {
	clients   map[string]struct{}
	clientsMu sync.Mutex
	subs      map[chan string]struct{}
	subsMu    sync.Mutex
	counter   atomic.Uint64
}

func NewStatMgr() *StatMgr {
	return &StatMgr{
		clients: make(map[string]struct{}),
		subs:    make(map[chan string]struct{}),
	}
}

func (s *StatMgr) IncCounter() {
	s.counter.Add(1)
}

func (s *StatMgr) Counter() uint64 {
	return s.counter.Load()
}

func (s *StatMgr) OnConnect(addr string) {
	s.clientsMu.Lock()
	s.clients[addr] = struct{}{}
	s.clientsMu.Unlock()
	s.subsMu.Lock()
	for ch := range s.subs {
		select {
		case ch <- addr:
		default:
		}
	}
	s.subsMu.Unlock()
}

func (s *StatMgr) OnDisconnect(addr string) {
	s.clientsMu.Lock()
	delete(s.clients, addr)
	s.clientsMu.Unlock()
}

func (s *StatMgr) Subscribe() chan string {
	ch := make(chan string, 1024)
	s.subsMu.Lock()
	s.subs[ch] = struct{}{}
	s.subsMu.Unlock()
	return ch
}

func (s *StatMgr) Unsubscribe(ch chan string) {
	s.subsMu.Lock()
	delete(s.subs, ch)
	s.subsMu.Unlock()
	close(ch)
}

func (s *StatMgr) ListClients() string {
	s.clientsMu.Lock()
	defer s.clientsMu.Unlock()
	out := ""
	for c := range s.clients {
		if out != "" {
			out += " "
		}
		out += c
	}
	return out
}
