package main

import (
	"crypto/tls"
	"flag"
	"log"
	"net/http"
	"time"

	"github.com/dizziedbliss/meow-pspsps/internal/mock"
)

// cmdMock runs inside the meow-judge-mock container: fake Telegram on :80, fake Google APIs on :443,
// and the worker's admin API on :8080.
func cmdMock(args []string) error {
	fs := flag.NewFlagSet("mock", flag.ExitOnError)
	cert := fs.String("cert", "", "TLS certificate (PEM)")
	key := fs.String("key", "", "TLS key (PEM)")
	_ = fs.Parse(args)
	pair, err := tls.LoadX509KeyPair(*cert, *key)
	if err != nil {
		return err
	}
	srv := mock.New()
	public, admin := srv.Handlers()
	errc := make(chan error, 3)
	serve := func(s *http.Server, tlsOn bool) {
		if tlsOn {
			errc <- s.ListenAndServeTLS("", "")
		} else {
			errc <- s.ListenAndServe()
		}
	}
	go serve(&http.Server{Addr: ":80", Handler: public, ReadHeaderTimeout: 10 * time.Second}, false)
	go serve(&http.Server{Addr: ":443", Handler: public, ReadHeaderTimeout: 10 * time.Second,
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12}}, true)
	go serve(&http.Server{Addr: ":8080", Handler: admin, ReadHeaderTimeout: 10 * time.Second}, false)
	log.Printf("mock up: telegram :80, google :443, admin :8080")
	return <-errc
}
