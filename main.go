package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"tunnel/relay"
)

func main() {
	localF := flag.String("L", "", "Local forwarding: [bind:]port:host:hostport")
	dynamicF := flag.String("D", "", "Dynamic SOCKS5 proxy (not yet implemented)")

	var (
		_ = flag.String("R", "", "Remote forwarding (not yet implemented)")
		_ = flag.String("s", "", "Server address for remote forwarding")
	)
	tlsF := flag.Bool("tls", false, "Enable TLS encryption")
	udpF := flag.Bool("udp", false, "Use UDP instead of TCP")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, `Tunnel - port forwarding tool

Usage:
  tunnel -L [bind:]port:host:hostport    Local forwarding (ad-hoc)
  tunnel -R port:host:hostport -s addr   Remote forwarding (ad-hoc)
  tunnel -D port                         Dynamic SOCKS5 proxy (ad-hoc)
  tunnel start                           Start daemon (managed mode)
  tunnel stop                            Stop daemon

Examples:
  tunnel -L 3306:db.internal:3306
  tunnel -L 127.0.0.1:3306:db.internal:3306
  tunnel -L 8080:localhost:80 --tls
  tunnel -R 9090:localhost:8080 -s server.example.com:9000
  tunnel -D 1080

Flags:
`)
		flag.PrintDefaults()
	}

	if len(os.Args) < 2 {
		flag.Usage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "start":
		cmdStart()
	case "stop":
		cmdStop()
	default:
		// Parse flags from os.Args[1:] since first arg isn't a subcommand
		flag.CommandLine.Parse(os.Args[1:])
		switch {
		case *localF != "":
			runAdhocLocal(*localF, *tlsF, *udpF)
		case *dynamicF != "":
			runAdhocDynamic(*dynamicF)
		default:
			flag.Usage()
			os.Exit(1)
		}
	}
}

func parseLocalSpec(spec string) (listenAddr, dialAddr string, err error) {
	hostPort := ""
	for i := len(spec) - 1; i >= 0; i-- {
		if spec[i] == ':' {
			if hostPort == "" {
				hostPort = spec[i+1:]
				spec = spec[:i]
			} else {
				dialAddr = spec[i+1:] + ":" + hostPort
				spec = spec[:i]
				break
			}
		}
	}
	if dialAddr == "" {
		return "", "", fmt.Errorf("invalid -L format: %q (expected [bind:]port:host:hostport)", spec+":"+hostPort)
	}
	lastColon := -1
	for i := len(spec) - 1; i >= 0; i-- {
		if spec[i] == ':' {
			lastColon = i
			break
		}
	}
	if lastColon >= 0 {
		listenAddr = spec[:lastColon] + ":" + spec[lastColon+1:]
	} else {
		listenAddr = "127.0.0.1:" + spec
	}
	return listenAddr, dialAddr, nil
}

func runAdhocLocal(spec string, tls bool, udp bool) {
	if udp {
		log.Fatal("UDP forwarding not yet implemented")
	}
	if tls {
		log.Print("TLS mode requested, using plain TCP for now (TLS coming in Phase 3)")
	}
	listenAddr, dialAddr, err := parseLocalSpec(spec)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("Listening on %s, forwarding to %s", listenAddr, dialAddr)
	proxy, err := relay.NewProxy(listenAddr, dialAddr)
	if err != nil {
		log.Fatalf("Failed to start proxy: %v", err)
	}
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		log.Printf("Received %v, shutting down...", sig)
		proxy.Close()
	}()
	if err := proxy.Serve(); err != nil {
		log.Fatal(err)
	}
}

func runAdhocDynamic(portSpec string) {
	listenAddr := portSpec
	if portSpec[0] != ':' {
		listenAddr = "127.0.0.1:" + portSpec
	}

	log.Printf("SOCKS5 proxy on %s", listenAddr)
	proxy, err := relay.NewSocksProxy(listenAddr)
	if err != nil {
		log.Fatalf("Failed to start SOCKS proxy: %v", err)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		log.Printf("Received %v, shutting down...", sig)
		proxy.Close()
	}()
	if err := proxy.Serve(); err != nil {
		log.Fatal(err)
	}
}

func cmdStart() {
	log.Fatal("daemon mode not yet implemented")
}

func cmdStop() {
	log.Fatal("daemon mode not yet implemented")
}
