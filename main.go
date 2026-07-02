package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

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
	tlsCertF := flag.String("tls-cert", "", "TLS certificate file (PEM)")
	tlsKeyF := flag.String("tls-key", "", "TLS key file (PEM)")
	tlsVerifyF := flag.Bool("tls-verify", false, "Verify TLS peer certificate (requires trusted CA)")
	udpF := flag.Bool("udp", false, "Use UDP instead of TCP")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, `Tunnel - port forwarding tool

FORWARDING MODES (ad-hoc):

  tunnel -L [bind:]port:host:hostport
        Local TCP forwarding, like ssh -L.
        Forward a local port to a remote target.
        Examples:
          tunnel -L 3306:db.internal:3306         # mysql
          tunnel -L 0.0.0.0:8080:web:80            # bind all interfaces

  tunnel -D port
        Dynamic SOCKS5 proxy, like ssh -D.
        Start a SOCKS5 proxy on the given port.
        Example:
          tunnel -D 1080                           # curl --socks5 127.0.0.1:1080

  tunnel -R port:host:hostport -s server:port
        Remote TCP forwarding (NAT穿透), like ssh -R.
        Expose a local port through a public server.
        First start the server, then connect the client:
          tunnel -s 0.0.0.0:9000                   # server (public machine)
          tunnel -R 9090:localhost:8080 -s vps:9000  # client (NAT'd machine)

TLS ENCRYPTION (all forwarding modes):

  --tls                            Enable TLS 1.3 (auto-generates self-signed cert)
  --tls-cert <file> --tls-key <file>  Use real PEM cert+key instead of auto-generating
  --tls-verify                     Verify peer certificate (requires trusted CA)

  Examples:
    tunnel -L 8080:web:80 --tls                           # self-signed, skip verify
    tunnel -L 443:web:443 --tls --tls-cert cert.pem --tls-key key.pem --tls-verify
    tunnel -R 9090:localhost:8080 -s vps:9000 --tls --tls-verify

AUTH TOKEN (remote forwarding):

  --token <str>                    Require token for client connections
    tunnel -s 0.0.0.0:9000 --token mytoken              # server
    tunnel -R 9090:localhost:8080 -s vps:9000 --token mytoken  # client

DAEMON MODE (managed tunnels via YAML config):

  tunnel start [--background]      Start daemon
  tunnel stop                      Stop daemon
  tunnel ls                        List configured tunnels and their status
  tunnel add -L/-R/-D ... [--name X]  Add and start a tunnel
  tunnel rm <name>                 Remove a tunnel
  tunnel reload                    Hot-reload ~/.config/tunnel/config.yaml
  tunnel start-group <group>       Start all tunnels in a group
  tunnel stop-group <group>        Stop all tunnels in a group

  Config file: ~/.config/tunnel/config.yaml
  Example:
    tunnels:
      - name: web
        mode: local
        local: 8080
        remote: web.internal:80
        autostart: true
      - name: socks
        mode: dynamic
        local: 1080
        autostart: true
      - name: remote-web
        mode: remote
        server: vps.example.com:9000
        remote: 9090:localhost:8080
        token: mytoken
        tls: true
        tls_verify: true
        autostart: true

SYSTEMD INTEGRATION:

  tunnel init --systemd            Print systemd unit (pipe to create service)
    tunnel init --systemd | sudo tee /etc/systemd/system/tunnel.service
    sudo systemctl enable --now tunnel

  tunnel init                      Print installation hint

OTHER FLAGS:
`)
		flag.PrintDefaults()
	}

	if len(os.Args) < 2 {
		flag.Usage()
		os.Exit(1)
	}

	applyGlobalConfig()

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
	case "init":
		cmdInit()
	case "reload":
		cmdReload()
	case "start-group":
		cmdStartGroup()
	case "stop-group":
		cmdStopGroup()
	default:
		flag.CommandLine.Parse(os.Args[1:])
		switch {
		case *localF != "":
			runAdhocLocal(*localF, *tlsF, *tlsCertF, *tlsKeyF, *tlsVerifyF, *udpF)
		case *dynamicF != "":
			runAdhocDynamic(*dynamicF)
		case *remoteF != "" && *serverF != "":
			runAdhocRemote(*remoteF, *serverF, *tokenF, *tlsF, *tlsCertF, *tlsKeyF, *tlsVerifyF)
		case *remoteF == "" && *serverF != "":
			runAdhocRemoteServer(*serverF, *tokenF, *tlsF, *tlsCertF, *tlsKeyF, *tlsVerifyF)
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
	subArgs := os.Args[2:]
	subFlags := flag.NewFlagSet("add", flag.ExitOnError)
	l := subFlags.String("L", "", "Local forwarding")
	r := subFlags.String("R", "", "Remote forwarding")
	d := subFlags.String("D", "", "Dynamic SOCKS5")
	name := subFlags.String("name", "", "Tunnel name")
	server := subFlags.String("s", "", "Server address (for -R)")
	token := subFlags.String("token", "", "Auth token (for -R)")
	tlsF := subFlags.Bool("tls", false, "Enable TLS (for -R)")
	tlsVerify := subFlags.Bool("tls-verify", false, "Verify TLS peer (for -R)")
	tlsCert := subFlags.String("tls-cert", "", "TLS cert file (for -R)")
	tlsKey := subFlags.String("tls-key", "", "TLS key file (for -R)")
	subFlags.Parse(subArgs)

	tc, err := buildTunnelConfig(addParams{
		local: *l, remote: *r, dynamic: *d, name: *name,
		server: *server, token: *token,
		tls: *tlsF, tlsVerify: *tlsVerify, tlsCert: *tlsCert, tlsKey: *tlsKey,
	})
	if err != nil {
		log.Fatal(err)
	}

	if _, err := manager.SendControl("add", tc); err != nil {
		log.Fatalf("add: %v", err)
	}
	fmt.Printf("Tunnel %q added and started.\n", tc.Name)
}

type addParams struct {
	local, remote, dynamic, name string
	server, token                string
	tls, tlsVerify               bool
	tlsCert, tlsKey              string
}

// buildTunnelConfig turns `tunnel add` flags into a TunnelConfig. It supports
// all three modes (-L/-R/-D) and requires -s for remote tunnels.
func buildTunnelConfig(p addParams) (manager.TunnelConfig, error) {
	var tc manager.TunnelConfig
	switch {
	case p.local != "":
		tc.Mode = "local"
		listen, target, err := parseLocalSpec(p.local)
		if err != nil {
			return tc, err
		}
		tc.Local = listen
		tc.Remote = target
		tc.Name = defaultName(p.name, "local-"+p.local)
	case p.remote != "":
		if p.server == "" {
			return tc, fmt.Errorf("-R requires -s server:port")
		}
		tc.Mode = "remote"
		tc.Remote = p.remote
		tc.Server = p.server
		tc.Token = p.token
		tc.TLS = p.tls
		tc.TLSVerify = p.tlsVerify
		tc.TLSCert = p.tlsCert
		tc.TLSKey = p.tlsKey
		tc.Name = defaultName(p.name, "remote-"+p.remote)
	case p.dynamic != "":
		tc.Mode = "dynamic"
		tc.Local = normalizeListenAddr(p.dynamic)
		tc.Name = defaultName(p.name, "dynamic-"+p.dynamic)
	default:
		return tc, fmt.Errorf("specify -L, -R, or -D")
	}
	tc.Autostart = true
	return tc, nil
}

func defaultName(name, fallback string) string {
	if name != "" {
		return name
	}
	return fallback
}

func normalizeListenAddr(spec string) string {
	if strings.Contains(spec, ":") {
		return spec
	}
	return "127.0.0.1:" + spec
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

// ─── New commands ───────────────────────────────────────────────────

func cmdInit() {
	subFlags := flag.NewFlagSet("init", flag.ExitOnError)
	systemd := subFlags.Bool("systemd", false, "Generate systemd unit")
	systemdUser := subFlags.Bool("systemd-user", false, "Generate user systemd unit")
	subFlags.Parse(os.Args[2:])

	switch {
	case *systemd:
		fmt.Print(manager.GenerateSystemdUnit())
	case *systemdUser:
		if err := manager.WriteSystemdUnitUser(""); err != nil {
			log.Fatalf("write user unit: %v", err)
		}
		fmt.Println("Wrote tunnel.service to ~/.config/systemd/user/")
	default:
		fmt.Println(manager.SystemdInstallHint())
	}
}

func cmdReload() {
	_, err := manager.SendControl("reload", nil)
	if err != nil {
		log.Fatalf("reload: %v", err)
	}
	fmt.Println("Config reloaded.")
}

func cmdStartGroup() {
	if len(os.Args) < 3 {
		log.Fatal("usage: tunnel start-group <group>")
	}
	group := os.Args[2]
	_, err := manager.SendControl("start-group", map[string]string{"group": group})
	if err != nil {
		log.Fatalf("start-group: %v", err)
	}
	fmt.Printf("Group %q started.\n", group)
}

func cmdStopGroup() {
	if len(os.Args) < 3 {
		log.Fatal("usage: tunnel stop-group <group>")
	}
	group := os.Args[2]
	_, err := manager.SendControl("stop-group", map[string]string{"group": group})
	if err != nil {
		log.Fatalf("stop-group: %v", err)
	}
	fmt.Printf("Group %q stopped.\n", group)
}

// ─── Daemon runner ───────────────────────────────────────────────────

func runDaemon(cfg *manager.Config) {
	if cfg.Global.LogFile != "" {
		if f, err := manager.SetupLogFile(cfg.Global.LogFile); err != nil {
			log.Printf("log file: %v", err)
		} else {
			defer f.Close()
		}
	}

	if err := manager.WritePidfile(); err != nil {
		log.Fatalf("pidfile: %v", err)
	}
	defer manager.RemovePidfile()

	mgr := manager.NewManager(cfg, "")
	if err := mgr.Start(); err != nil {
		log.Fatalf("manager start: %v", err)
	}

	shutdown := make(chan struct{})
	var shutdownOnce sync.Once
	triggerShutdown := func() { shutdownOnce.Do(func() { close(shutdown) }) }

	// Control server. A "stop" request stops the tunnels (in HandleControl) and
	// then shuts the daemon down. The shutdown is deferred briefly so the
	// response is flushed to the client before the process exits.
	handler := func(req manager.Request) manager.Response {
		resp := mgr.HandleControl(req)
		if req.Method == "stop" {
			go func() {
				time.Sleep(100 * time.Millisecond)
				triggerShutdown()
			}()
		}
		return resp
	}
	go func() {
		if err := manager.ServeControl(handler, shutdown); err != nil {
			log.Printf("control server: %v", err)
			triggerShutdown()
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		for sig := range sigCh {
			switch sig {
			case syscall.SIGHUP:
				log.Print("reloading config...")
				if err := mgr.Reload(); err != nil {
					log.Printf("reload: %v", err)
				}
			case syscall.SIGINT, syscall.SIGTERM:
				triggerShutdown()
			}
		}
	}()

	<-shutdown
	log.Print("shutting down...")
	mgr.Stop()
}

// applyGlobalConfig loads the config (best-effort) and applies process-wide
// settings that the daemon and CLI clients must agree on — currently a custom
// control socket path. Errors are ignored so commands still work with defaults.
func applyGlobalConfig() {
	cfg, err := manager.LoadConfig("")
	if err != nil {
		return
	}
	if cfg.Global.ControlSocket != "" {
		manager.SetControlSocketPath(cfg.Global.ControlSocket)
	}
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

func runAdhocLocal(spec string, tls bool, tlsCert, tlsKey string, tlsVerify bool, udp bool) {
	if udp {
		log.Fatal("UDP forwarding not yet implemented")
	}
	listenAddr, dialAddr, err := parseLocalSpec(spec)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("Listening on %s, forwarding to %s", listenAddr, dialAddr)
	var proxy *relay.Proxy
	if tls {
		tlsCfg, err := relay.SetupTLS(tlsCert, tlsKey, tlsVerify)
		if err != nil {
			log.Fatalf("TLS setup: %v", err)
		}
		proxy, err = relay.NewTLSProxy(listenAddr, dialAddr, tlsCfg)
	} else {
		proxy, err = relay.NewProxy(listenAddr, dialAddr)
	}
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

func runAdhocRemote(remoteSpec, serverAddr, token string, tls bool, tlsCert, tlsKey string, tlsVerify bool) {
	remotePort, targetAddr, err := parseRemoteSpec(remoteSpec)
	if err != nil {
		log.Fatal(err)
	}

	var tlsCfg *relay.TLSConfig
	if tls {
		tlsCfg, err = relay.SetupTLS(tlsCert, tlsKey, tlsVerify)
		if err != nil {
			log.Fatalf("TLS setup: %v", err)
		}
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

func runAdhocRemoteServer(serverAddr, token string, tls bool, tlsCert, tlsKey string, tlsVerify bool) {
	var tlsCfg *relay.TLSConfig
	if tls {
		var err error
		tlsCfg, err = relay.SetupTLS(tlsCert, tlsKey, tlsVerify)
		if err != nil {
			log.Fatalf("TLS setup: %v", err)
		}
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
