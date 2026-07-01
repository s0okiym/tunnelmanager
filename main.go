package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"tunnel/manager"
	"tunnel/relay"
)

func main() {
	localF := flag.String("L", "", "Local forwarding: [bind:]port:host:hostport")
	remoteF := flag.String("R", "", "Remote forwarding: [bind:]port:host:hostport")
	dynamicF := flag.String("D", "", "Dynamic SOCKS5 proxy")
	serverF := flag.String("s", "", "Server address for remote forwarding")
	tokenF := flag.String("token", "", "Auth token for remote forwarding")
	tlsF := flag.Bool("tls", false, "Enable TLS encryption")
	udpF := flag.Bool("udp", false, "Use UDP instead of TCP")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, `Tunnel - port forwarding tool

Usage:
  tunnel -L [bind:]port:host:hostport    Local forwarding (ad-hoc)
  tunnel -R port:host:hostport -s addr   Remote forwarding (ad-hoc)
  tunnel -D port                         Dynamic SOCKS5 proxy (ad-hoc)
  tunnel start [--background]            Start daemon (managed mode)
  tunnel stop                            Stop daemon
  tunnel ls                              List tunnels
  tunnel add -L ... [--name X]           Add tunnel
  tunnel rm <name>                       Remove tunnel

Examples:
  tunnel -L 3306:db.internal:3306
  tunnel -R 9090:localhost:8080 -s server.example.com:9000
  tunnel -D 1080
  tunnel start --background

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
	case "ls", "list":
		cmdList()
	case "add":
		cmdAdd()
	case "rm", "remove":
		cmdRemove()
	default:
		flag.CommandLine.Parse(os.Args[1:])
		switch {
		case *localF != "":
			runAdhocLocal(*localF, *tlsF, *udpF)
		case *dynamicF != "":
			runAdhocDynamic(*dynamicF)
		case *remoteF != "" && *serverF != "":
			runAdhocRemote(*remoteF, *serverF, *tokenF, *tlsF)
		case *remoteF == "" && *serverF != "":
			runAdhocRemoteServer(*serverF, *tokenF, *tlsF)
		default:
			flag.Usage()
			os.Exit(1)
		}
	}
}

// ─── Daemon commands ─────────────────────────────────────────────────

func cmdStart() {
	background := false
	for _, a := range os.Args[2:] {
		if a == "--background" || a == "-background" {
			background = true
		}
	}

	if manager.IsRunning() {
		log.Fatal("tunneld is already running")
	}

	cfg, err := manager.LoadConfig("")
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	if background {
		// fork+exec with --foreground to prevent recursive backgrounding
		args := append([]string{os.Args[0], "start", "--foreground"}, os.Args[3:]...)
		proc, err := os.StartProcess(os.Args[0], args, &os.ProcAttr{
			Files: []*os.File{nil, nil, nil},
			Sys:   &syscall.SysProcAttr{Setsid: true},
		})
		if err != nil {
			log.Fatalf("fork: %v", err)
		}
		fmt.Printf("tunneld started (pid %d)\n", proc.Pid)
		os.Exit(0)
	}

	runDaemon(cfg)
}

func cmdStop() {
	_, err := manager.SendControl("stop", nil)
	if err != nil {
		log.Fatalf("stop: %v", err)
	}
	manager.RemovePidfile()
	fmt.Println("tunneld stopped")
}

func cmdList() {
	result, err := manager.SendControl("list", nil)
	if err != nil {
		log.Fatalf("list: %v", err)
	}
	var statuses []manager.TunnelStatus
	if err := json.Unmarshal(result, &statuses); err != nil {
		log.Fatalf("parse status: %v", err)
	}
	if len(statuses) == 0 {
		fmt.Println("No tunnels configured.")
		return
	}
	fmt.Printf("%-20s %-8s %-22s %-22s %s\n", "NAME", "MODE", "LOCAL", "REMOTE", "STATUS")
	for _, s := range statuses {
		fmt.Printf("%-20s %-8s %-22s %-22s %s\n", s.Name, s.Mode, s.Local, s.Remote, s.Status)
	}
}

func cmdAdd() {
	// parse remaining args like -L, -R, -D
	subArgs := os.Args[2:]
	subFlags := flag.NewFlagSet("add", flag.ExitOnError)
	l := subFlags.String("L", "", "Local forwarding")
	r := subFlags.String("R", "", "Remote forwarding")
	d := subFlags.String("D", "", "Dynamic SOCKS5")
	name := subFlags.String("name", "", "Tunnel name")
	subFlags.Parse(subArgs)

	var tc manager.TunnelConfig

	switch {
	case *l != "":
		tc.Mode = "local"
		listen, target, err := parseLocalSpec(*l)
		if err != nil {
			log.Fatal(err)
		}
		tc.Local = listen
		tc.Remote = target
	case *r != "":
		tc.Mode = "remote"
		tc.Remote = *r
		// Check if -s was passed (should be in remaining args, but it might not be)
		// For simplicity, require server to be part of the spec
	default:
		log.Fatal("specify -L, -R, or -D")
	}

	if *name != "" {
		tc.Name = *name
	} else if *l != "" {
		tc.Name = fmt.Sprintf("local-%s", *l)
	} else if *r != "" {
		tc.Name = fmt.Sprintf("remote-%s", *r)
	} else if *d != "" {
		tc.Name = fmt.Sprintf("dynamic-%s", *d)
	}

	tc.Autostart = true

	_, err := manager.SendControl("add", tc)
	if err != nil {
		log.Fatalf("add: %v", err)
	}
	fmt.Printf("Tunnel %q added and started.\n", tc.Name)
}

func cmdRemove() {
	if len(os.Args) < 3 {
		log.Fatal("usage: tunnel rm <name>")
	}
	name := os.Args[2]
	_, err := manager.SendControl("remove", map[string]string{"name": name})
	if err != nil {
		log.Fatalf("remove: %v", err)
	}
	fmt.Printf("Tunnel %q removed.\n", name)
}

// ─── Daemon runner ───────────────────────────────────────────────────

func runDaemon(cfg *manager.Config) {
	if err := manager.WritePidfile(); err != nil {
		log.Fatalf("pidfile: %v", err)
	}
	defer manager.RemovePidfile()

	mgr := manager.NewManager(cfg, "")
	if err := mgr.Start(); err != nil {
		log.Fatalf("manager start: %v", err)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)

	go func() {
		for sig := range sigCh {
			switch sig {
			case syscall.SIGHUP:
				log.Print("reloading config...")
				newCfg, err := manager.LoadConfig("")
				if err != nil {
					log.Printf("reload: %v", err)
					continue
				}
				mgr.Stop()
				mgr = manager.NewManager(newCfg, "")
				mgr.Start()
			case syscall.SIGINT, syscall.SIGTERM:
				log.Print("shutting down...")
				mgr.Stop()
				os.Exit(0)
			}
		}
	}()

	log.Fatal(manager.ServeControl(mgr.HandleControl))
}

// ─── Ad-hoc mode helpers ─────────────────────────────────────────────

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

func parseRemoteSpec(spec string) (remotePort uint16, targetAddr string, err error) {
	hostPort := ""
	for i := len(spec) - 1; i >= 0; i-- {
		if spec[i] == ':' {
			if hostPort == "" {
				hostPort = spec[i+1:]
				spec = spec[:i]
			} else {
				targetAddr = spec[i+1:] + ":" + hostPort
				spec = spec[:i]
				break
			}
		}
	}
	if targetAddr == "" {
		return 0, "", fmt.Errorf("invalid -R format: %q (expected [bind:]port:host:hostport)", spec+":"+hostPort)
	}
	lastColon := -1
	for i := len(spec) - 1; i >= 0; i-- {
		if spec[i] == ':' {
			lastColon = i
			break
		}
	}
	portStr := spec
	if lastColon >= 0 {
		portStr = spec[lastColon+1:]
	}
	var p int
	for _, c := range portStr {
		p = p*10 + int(c-'0')
	}
	return uint16(p), targetAddr, nil
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

func runAdhocRemote(remoteSpec, serverAddr, token string, tls bool) {
	remotePort, targetAddr, err := parseRemoteSpec(remoteSpec)
	if err != nil {
		log.Fatal(err)
	}

	var tlsCfg *relay.TLSConfig
	if tls {
		cert, err := relay.GenerateCert()
		if err != nil {
			log.Fatalf("Failed to generate TLS cert: %v", err)
		}
		tlsCfg = &relay.TLSConfig{Enabled: true, Cert: cert, Insecure: true}
	}

	tunnels := []relay.RemoteTunnel{
		{RemotePort: remotePort, TargetAddr: targetAddr},
	}

	log.Printf("Remote tunnel: %s:%d -> %s (via %s)", "0.0.0.0", remotePort, targetAddr, serverAddr)
	client := relay.NewRemoteClient(serverAddr, token, tlsCfg, tunnels)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Print("Shutting down...")
		client.Close()
	}()

	client.Run()
}

func runAdhocRemoteServer(serverAddr, token string, tls bool) {
	var tlsCfg *relay.TLSConfig
	if tls {
		cert, err := relay.GenerateCert()
		if err != nil {
			log.Fatalf("Failed to generate TLS cert: %v", err)
		}
		tlsCfg = &relay.TLSConfig{Enabled: true, Cert: cert, Insecure: true}
	}

	srv, err := relay.NewRemoteServer(serverAddr, token, tlsCfg)
	if err != nil {
		log.Fatalf("Failed to start remote server: %v", err)
	}

	log.Printf("Remote server listening on %s", serverAddr)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		log.Printf("Received %v, shutting down...", sig)
		srv.Close()
	}()

	if err := srv.Serve(); err != nil {
		log.Fatal(err)
	}
	srv.Wait()
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
