package main

import (
	"crypto/tls"
	"crypto/x509"
	"flag"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"time"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:8000", "")
	upstream := flag.String("upstream", "", "")
	cert := flag.String("cert", "", "")
	key := flag.String("key", "", "")
	ca := flag.String("ca", "", "")
	flag.Parse()
	target, err := url.Parse(*upstream)
	if err != nil || target == nil || target.Host == "" || (target.Scheme != "http" && target.Scheme != "https") {
		log.Fatal("upstream must be an absolute HTTP(S) URL")
	}
	if (*cert == "") != (*key == "") || (*cert == "") != (*ca == "") {
		log.Fatal("TLS termination requires cert, key and client CA together")
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.FlushInterval = -1
	server := &http.Server{Addr: *listen, Handler: proxy, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second}
	if *cert != "" {
		pool := x509.NewCertPool()
		raw, err := os.ReadFile(*ca)
		if err != nil {
			log.Fatal(err)
		}
		if !pool.AppendCertsFromPEM(raw) {
			log.Fatal("invalid CA")
		}
		server.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: pool}
		log.Fatal(server.ListenAndServeTLS(*cert, *key))
	}
	log.Fatal(server.ListenAndServe())
}
