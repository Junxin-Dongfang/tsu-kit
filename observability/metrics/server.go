package metrics

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Server exposes a registry on a standalone /metrics HTTP listener.
type Server struct {
	addr string
	reg  *Registry

	mu       sync.Mutex
	listener net.Listener
	server   *http.Server
}

// NewServer returns a due component for services that do not own an HTTP port.
func NewServer(addr string, reg *Registry) *Server {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		addr = ":9091"
	}
	if reg == nil {
		reg = Init(Config{Namespace: defaultNamespace, Service: "unknown"})
	}
	return &Server{addr: addr, reg: reg}
}

func (s *Server) Name() string { return "observability-metrics" }

func (s *Server) Init() {}

func (s *Server) Start() {
	mux := http.NewServeMux()
	mux.Handle("/metrics", s.reg.PromHandler())
	server := &http.Server{
		Addr:              s.addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	listener, err := net.Listen("tcp", s.addr)
	if err != nil {
		log.Fatalf("metrics server listen %s: %v", s.addr, err)
	}

	s.mu.Lock()
	s.listener = listener
	s.server = server
	s.mu.Unlock()

	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("metrics server serve %s: %v", listener.Addr().String(), err)
		}
	}()
}

func (s *Server) Close() {
	s.mu.Lock()
	server := s.server
	s.mu.Unlock()
	if server == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = server.Shutdown(ctx)
}

func (s *Server) Destroy() { s.Close() }

func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener != nil {
		return s.listener.Addr().String()
	}
	return s.addr
}
