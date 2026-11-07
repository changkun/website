// Copyright 2020 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// Main serves changkun.de: the embedded static site, the folders of WWW_DIR,
// and, when configured, the ideas API under /ideas/ and the library under
// /library/.
package main

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"changkun.de/x/website/internal/ideas"
	"changkun.de/x/website/internal/library"
)

func main() {
	l := log.New(os.Stdout, "", log.LstdFlags|log.Lshortfile)

	ctx, stop := notifyShutdown(context.Background())
	defer stop()

	if err := run(ctx, os.Getenv("WEBSITE_ADDR"), l); err != nil {
		l.Fatalf("changkun.de stopped: %v", err)
	}
	l.Println("goodbye!")
}

// run serves on addr until ctx is done. An empty addr means 0.0.0.0:80.
func run(ctx context.Context, addr string, l *log.Logger) error {
	if addr == "" {
		addr = "0.0.0.0:80"
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	return serve(ctx, ln, l)
}

// notifyShutdown returns a context cancelled by SIGINT or SIGTERM. The handler
// is removed after the first signal, so a second one kills the process.
func notifyShutdown(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-ctx.Done()
		stop()
	}()
	return ctx, stop
}

// serve serves on ln until ctx is done, then drains in-flight requests.
func serve(ctx context.Context, ln net.Listener, l *log.Logger) error {
	site, err := withWWW(l, FileServer(fsys))
	if err != nil {
		ln.Close()
		return err
	}
	lib, err := library.New(l)
	if err != nil {
		ln.Close()
		return err
	}
	lib.Run(ctx)

	r := http.NewServeMux()
	r.Handle("/", site)
	r.Handle(ideas.Prefix, ideas.Handler(ideas.New(l), l))
	r.Handle(library.Prefix, library.Handler(lib))
	s := &http.Server{
		Handler:           logging(l)(r),
		ErrorLog:          l,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      2 * time.Minute, // /ideas/improve waits on a model
		IdleTimeout:       time.Minute,
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		<-ctx.Done()
		l.Println("changkun.de is shutting down...")

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		s.SetKeepAlivesEnabled(false)
		if err := s.Shutdown(ctx); err != nil {
			l.Printf("cannot gracefully shutdown changkun.de: %v", err)
		}
	}()

	l.Printf("changkun.de is serving on %s...", ln.Addr())
	err = s.Serve(ln)
	<-done
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// logging logs the client IP, method, and path of every request.
func logging(logger *log.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				logger.Println(readIP(r), r.Method, r.URL.Path)
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// readIP returns the client IP from X-Forwarded-For, X-Real-Ip, or the remote
// address, in that order, or "unknown".
func readIP(r *http.Request) string {
	forwarded, _, _ := strings.Cut(r.Header.Get("X-Forwarded-For"), ",")
	if clientIP := strings.TrimSpace(forwarded); clientIP != "" {
		return clientIP
	}
	if clientIP := strings.TrimSpace(r.Header.Get("X-Real-Ip")); clientIP != "" {
		return clientIP
	}
	ip, _, err := net.SplitHostPort(strings.TrimSpace(r.RemoteAddr))
	if err != nil {
		return "unknown"
	}
	return ip
}
