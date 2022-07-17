package monitoring

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/pion/logging"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Monitoring is an instance of STUNner monitoring
type Backend struct {
	httpServer *http.Server
	Endpoint   string
}

// NewMonitoring initiates the monitoring subsystem
func NewBackend(endpoint string) (*Backend, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		b := &Backend{
			httpServer: nil,
			Endpoint:   endpoint,
		}
		return b, errors.New(fmt.Sprintf("unable to parse: %s", endpoint))
	}

	addr := u.Hostname()
	if addr == "" {
		// omitted value means no monitoring, in this case we
		// return a dummy Backend
		b := &Backend{
			httpServer: nil,
			Endpoint:   endpoint,
		}
		return b, nil
	}
	port := u.Port()
	if port != "" {
		addr = addr + ":" + port
	}
	path := u.EscapedPath()
	if path == "" {
		path = "/metrics"
	}

	mux := http.NewServeMux()
	mux.Handle(path, promhttp.Handler())

	server := &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	m := &Backend{
		httpServer: server,
		Endpoint:   endpoint,
	}

	return m, nil
}

func (b *Backend) Reload(endpoint string, log logging.LeveledLogger) *Backend {
	// stop if endpoint is unset
	if endpoint == "" {
		b.Stop()
		return b
	} else {
		// otherwise reinit at new address
		if b.Endpoint != endpoint {
			// new endpoint, restart monitoring server
			b.Stop()
			if m, err := NewBackend(endpoint); err == nil {
				b = m
				b.Start()
			} else {
				log.Warn("failed to create monitoring server")
			}
		}
	}
	return b
}

func (b *Backend) Start() {
	if b.httpServer == nil {
		return
	}
	// serve Prometheus metrics over HTTP
	go func() {
		b.httpServer.ListenAndServe()
	}()
}

func (b *Backend) Stop() {
	if b.httpServer == nil {
		return
	}
	b.httpServer.Shutdown(context.Background())
}
