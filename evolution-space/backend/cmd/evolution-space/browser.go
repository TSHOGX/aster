package main

import (
	"aster/contracts/appidentity"
	"aster/evolution-space/internal/httpapi"
	"aster/evolution-space/internal/projections"
	"fmt"
	"net"
	"net/http"
	"time"
)

func browserServer(store *projections.Store, listen, origin, frontend, endpoint, secret string, options httpapi.BrowserOptions) (*http.Server, net.Listener, error) {
	host, _, err := net.SplitHostPort(listen)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return nil, nil, fmt.Errorf("browser gateway requires a loopback listener behind the TLS front door")
	}
	client, err := appidentity.NewClient(appidentity.Config{Issuer: origin, Endpoint: endpoint, ClientID: "evolution-space", ClientSecret: secret})
	if err != nil {
		return nil, nil, err
	}
	gateway, err := (appidentity.Gateway{ApplicationID: "evolution-space", Sessions: store.BrowserSessions(client), PublicOrigin: origin, Frontend: frontend, API: httpapi.BrowserAPIWith(client, store, options)}).Handler()
	if err != nil {
		return nil, nil, err
	}
	listener, err := net.Listen("tcp", listen)
	if err != nil {
		return nil, nil, err
	}
	return &http.Server{Handler: gateway, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: time.Minute, MaxHeaderBytes: 32 << 10}, listener, nil
}
