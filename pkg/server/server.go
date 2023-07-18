package server

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"time"
)

// func Start(router *http.ServeMux, port int) {
// 	startServer(router, port)
// }

type key int

type Server struct {
	router        *http.ServeMux
	port          int
	useTLs        bool
	listenAddr    string
	requestIDKey  key
	accessLogging bool

	runningTask int32
	maxHoldoff  time.Duration
}

func NewServer(router *http.ServeMux, port int, useTLs bool) *Server {

	var server = &Server{
		router: router,
		port:   port,
		useTLs: useTLs,

		maxHoldoff: 5 * time.Minute,
	}

	router.Handle("/_serverconfigs", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("accessLogging") == "true" {
			server.accessLogging = true
		}
	}))

	return server
}

func (s *Server) Enable_accessLogging() {
	s.accessLogging = true
}
func (s *Server) RunningTaskAdd(cnt int) {
	atomic.AddInt32(&s.runningTask, int32(cnt))
}
func (s *Server) Set_maxHoldoff(t time.Duration) {
	s.maxHoldoff = t
}
func (s *Server) Start() {

	log.Println("[server] starting...")

	nextRequestID := func() string {
		b := make([]byte, 8)
		binary.LittleEndian.PutUint64(b, uint64(time.Now().UnixNano()))
		return base64.RawStdEncoding.EncodeToString(b)
	}

	handler := s.tracing(nextRequestID)(s.router)
	if s.accessLogging {
		handler = s.logging()(handler)
	}

	server := &http.Server{
		Addr:         s.listenAddr,
		Handler:      handler,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 0 * time.Second,
		IdleTimeout:  3600 * time.Second,
	}
	if s.useTLs {
		server.TLSConfig = &tls.Config{
			MinVersion: tls.VersionTLS12,
			CipherSuites: []uint16{
				tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
				tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
				tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
				tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
				tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305,
				tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305,
			},
		}

		// haproxy has problem with h2+tls
		// (http2: server: error reading preface from client ... read: connection reset by peer)
		// disabling h2 here for now
		// TODO: figure it out, probably need new haproxy version
		server.TLSNextProto = make(map[string]func(*http.Server, *tls.Conn, http.Handler))
	}

	done := make(chan bool)
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt)

	go func() {
		<-quit
		log.Println("[server] shutting down...")

		for s.runningTask > 0 {
			log.Printf("[server] waiting, RunningTask: %v\n", s.runningTask)
			time.Sleep(5 * time.Second)
		}

		ctx, cancel := context.WithTimeout(context.Background(), s.maxHoldoff)
		defer cancel()

		server.SetKeepAlivesEnabled(false)
		if err := server.Shutdown(ctx); err != nil {
			panic("[server] failed to gracefully shutdown the server")
		}
		close(done)
	}()
	log.Println("[server] ready to handle requests at: " + s.listenAddr)

	if s.useTLs {
		if err := server.ListenAndServeTLS("cert.pem", "key.pem"); err != nil && err != http.ErrServerClosed {
			log.Panicf("[server] cannot listen on %s: %v\n", s.listenAddr, err)
		}
	} else {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Panicf("[server] cannot listen on %s: %v\n", s.listenAddr, err)
		}
	}
	<-done
	log.Println("[server] stopped")
}
func (s *Server) tracing(nextRequestID func() string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requestID := r.Header.Get("X-Request-Id")
			if requestID == "" {
				requestID = nextRequestID()
			}
			ctx := context.WithValue(r.Context(), s.requestIDKey, requestID)
			w.Header().Set("X-Request-Id", requestID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
func (s *Server) logging() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if s.accessLogging {
				defer func() {
					requestID, ok := r.Context().Value(s.requestIDKey).(string)
					if !ok {
						requestID = "unknown"
					}
					log.Println("(" + requestID + "):" + r.Method + "@" + r.URL.Path + " <- " + r.RemoteAddr)
				}()
			}
			next.ServeHTTP(w, r)
		})
	}
}
