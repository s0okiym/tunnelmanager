package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"tunnel/manager"
	"tunnel/relay"
)

var knownHosts *relay.KnownHosts

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
	tlsTrustOnFirstUseF := flag.Bool("trust-on-first-use", false, "Trust and remember server certificate fingerprint on first connect (remote client)")
	tlsServerFingerprintF := flag.String("server-fingerprint", "", "Pinned server certificate fingerprint SHA256:... (remote client)")
	udpF := flag.Bool("udp", false, "Use UDP instead of TCP")
	socksUserF := flag.String("socks-user", "", "SOCKS5 username (for -D)")
	socksPassF := flag.String("socks-pass", "", "SOCKS5 password (for -D)")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, `Tunnel - port forwarding tool

FORWARDING MODES (ad-hoc):

  tunnel -L [bind:]port:host:hostport [--udp]
        Local TCP/UDP forwarding, like ssh -L.
        Forward a local port to a remote target.
        Examples:
          tunnel -L 3306:db.internal:3306         # mysql (TCP)
          tunnel -L 0.0.0.0:8080:web:80            # bind all interfaces (TCP)
          tunnel -L 127.0.0.1:53:8.8.8.8:53 --udp  # DNS (UDP)

  tunnel -D port
        Dynamic SOCKS5 proxy, like ssh -D.
        Start a SOCKS5 proxy on the given port.
        Examples:
          tunnel -D 1080                           # no-auth proxy
          tunnel -D 1080 --socks-user u --socks-pass p  # authenticated proxy

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
  --server-fingerprint SHA256:...  Pin the remote server identity (no CA needed)
  --trust-on-first-use             Remember server fingerprint on first connect

  Examples:
    tunnel -L 8080:web:80 --tls                           # self-signed, skip verify
    tunnel -L 443:web:443 --tls --tls-cert cert.pem --tls-key key.pem --tls-verify
    tunnel -R 9090:localhost:8080 -s vps:9000 --tls --server-fingerprint SHA256:...

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
  tunnel enable <name>             Enable autostart and start a tunnel
  tunnel disable <name>            Disable autostart and stop a tunnel
  tunnel restart <name>            Restart a tunnel
  tunnel save                      Persist current runtime config to disk
  tunnel status <name>             Show detailed status of a tunnel
  tunnel logs <name> [--lines N]   Show recent log lines for a tunnel
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
	case "enable":
		cmdEnable()
	case "disable":
		cmdDisable()
	case "restart":
		cmdRestart()
	case "save":
		cmdSave()
	case "status":
		cmdStatus()
	case "logs":
		cmdLogs()
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
			protocol := "tcp"
			if *udpF {
				protocol = "udp"
			}
			runAdhocLocal(*localF, protocol, *tlsF, *tlsCertF, *tlsKeyF, *tlsVerifyF)
		case *dynamicF != "":
			runAdhocDynamic(*dynamicF, *socksUserF, *socksPassF)
		case *remoteF != "" && *serverF != "":
			runAdhocRemote(*remoteF, *serverF, *tokenF, *tlsF, *tlsCertF, *tlsKeyF, *tlsVerifyF, *tlsTrustOnFirstUseF, *tlsServerFingerprintF)
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
	fmt.Printf("%-20s %-8s %-22s %-22s %-14s %-10s %s\n", "NAME", "MODE", "LOCAL", "REMOTE", "STATUS", "SINCE", "RECONNECTS")
	for _, s := range statuses {
		since := ""
		if !s.Since.IsZero() {
			since = time.Since(s.Since).Round(time.Second).String()
		}
		reconnects := ""
		if s.ReconnectCount > 0 {
			reconnects = strconv.FormatInt(s.ReconnectCount, 10)
		}
		status := s.Status
		if s.Error != "" {
			status = fmt.Sprintf("%s (%s)", status, s.Error)
		}
		fmt.Printf("%-20s %-8s %-22s %-22s %-14s %-10s %s\n", s.Name, s.Mode, s.Local, s.Remote, status, since, reconnects)
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
	tlsTrustOnFirstUse := subFlags.Bool("trust-on-first-use", false, "Trust server fingerprint on first connect (for -R)")
	tlsServerFingerprint := subFlags.String("server-fingerprint", "", "Pinned server fingerprint SHA256:... (for -R)")
	socksUser := subFlags.String("socks-user", "", "SOCKS5 username (for -D)")
	socksPass := subFlags.String("socks-pass", "", "SOCKS5 password (for -D)")
	udp := subFlags.Bool("udp", false, "Use UDP (for -L)")
	group := subFlags.String("group", "", "Tunnel group")
	autostart := subFlags.Bool("autostart", true, "Autostart tunnel")
	subFlags.Parse(subArgs)

	protocol := "tcp"
	if *udp {
		protocol = "udp"
	}

	tc, err := buildTunnelConfig(addParams{
		local: *l, remote: *r, dynamic: *d, name: *name,
		server: *server, token: *token,
		tls: *tlsF, tlsVerify: *tlsVerify, tlsCert: *tlsCert, tlsKey: *tlsKey,
		tlsTrustOnFirstUse: *tlsTrustOnFirstUse, tlsServerFingerprint: *tlsServerFingerprint,
		socksUser: *socksUser, socksPass: *socksPass,
		protocol:  protocol,
		group:     *group,
		autostart: *autostart,
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
	tlsTrustOnFirstUse           bool
	tlsServerFingerprint         string
	socksUser, socksPass         string
	protocol                     string
	group                        string
	autostart                    bool
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
		tc.Protocol = p.protocol
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
		tc.TLSTrustOnFirstUse = p.tlsTrustOnFirstUse
		tc.TLSServerFingerprint = p.tlsServerFingerprint
		tc.Name = defaultName(p.name, "remote-"+p.remote)
	case p.dynamic != "":
		tc.Mode = "dynamic"
		tc.Local = normalizeListenAddr(p.dynamic)
		tc.SocksUser = p.socksUser
		tc.SocksPass = p.socksPass
		tc.Name = defaultName(p.name, "dynamic-"+p.dynamic)
	default:
		return tc, fmt.Errorf("specify -L, -R, or -D")
	}
	tc.Group = p.group
	tc.Autostart = p.autostart
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

func cmdEnable() {
	if len(os.Args) < 3 {
		log.Fatal("usage: tunnel enable <name>")
	}
	name := os.Args[2]
	_, err := manager.SendControl("enable", map[string]string{"name": name})
	if err != nil {
		log.Fatalf("enable: %v", err)
	}
	fmt.Printf("Tunnel %q enabled.\n", name)
}

func cmdDisable() {
	if len(os.Args) < 3 {
		log.Fatal("usage: tunnel disable <name>")
	}
	name := os.Args[2]
	_, err := manager.SendControl("disable", map[string]string{"name": name})
	if err != nil {
		log.Fatalf("disable: %v", err)
	}
	fmt.Printf("Tunnel %q disabled.\n", name)
}

func cmdRestart() {
	if len(os.Args) < 3 {
		log.Fatal("usage: tunnel restart <name>")
	}
	name := os.Args[2]
	_, err := manager.SendControl("restart", map[string]string{"name": name})
	if err != nil {
		log.Fatalf("restart: %v", err)
	}
	fmt.Printf("Tunnel %q restarted.\n", name)
}

func cmdSave() {
	_, err := manager.SendControl("save", nil)
	if err != nil {
		log.Fatalf("save: %v", err)
	}
	fmt.Println("Config saved.")
}

func cmdStatus() {
	if len(os.Args) < 3 {
		// Summary mode: count total/running/stopped.
		result, err := manager.SendControl("list", nil)
		if err != nil {
			log.Fatalf("status: %v", err)
		}
		var statuses []manager.TunnelStatus
		if err := json.Unmarshal(result, &statuses); err != nil {
			log.Fatalf("parse status: %v", err)
		}
		running := 0
		for _, s := range statuses {
			if s.Status != "stopped" {
				running++
			}
		}
		fmt.Printf("%d tunnels configured, %d running, %d stopped\n", len(statuses), running, len(statuses)-running)
		return
	}

	name := os.Args[2]
	result, err := manager.SendControl("status", map[string]string{"name": name})
	if err != nil {
		log.Fatalf("status: %v", err)
	}
	var st manager.TunnelStatus
	if err := json.Unmarshal(result, &st); err != nil {
		log.Fatalf("parse status: %v", err)
	}
	fmt.Printf("Name:    %s\n", st.Name)
	fmt.Printf("Mode:    %s\n", st.Mode)
	fmt.Printf("Local:   %s\n", st.Local)
	fmt.Printf("Remote:  %s\n", st.Remote)
	fmt.Printf("Group:   %s\n", st.Group)
	fmt.Printf("Status:  %s\n", st.Status)
	if !st.Since.IsZero() {
		fmt.Printf("Since:   %s\n", time.Since(st.Since).Round(time.Second))
	}
	if st.ReconnectCount > 0 {
		fmt.Printf("Reconnects: %d\n", st.ReconnectCount)
	}
	if st.Error != "" {
		fmt.Printf("Error:   %s\n", st.Error)
	}
}

func cmdLogs() {
	subFlags := flag.NewFlagSet("logs", flag.ExitOnError)
	linesF := subFlags.Int("lines", 100, "Number of recent log lines to show")
	if err := subFlags.Parse(os.Args[2:]); err != nil {
		log.Fatalf("logs: %v", err)
	}
	name := subFlags.Arg(0)

	result, err := manager.SendControl("logs", map[string]any{"name": name, "lines": *linesF})
	if err != nil {
		log.Fatalf("logs: %v", err)
	}
	var lines []string
	if err := json.Unmarshal(result, &lines); err != nil {
		log.Fatalf("parse logs: %v", err)
	}
	if len(lines) == 0 {
		fmt.Println("No log lines available.")
		return
	}
	for _, line := range lines {
		fmt.Println(line)
	}
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
	manager.InstallLogCapture()

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
// control socket path, the persistent TLS identity directory, and the known_hosts
// path. Errors are ignored so commands still work with defaults.
func applyGlobalConfig() {
	cfg, err := manager.LoadConfig("")
	if err != nil {
		initTLSPaths()
		return
	}
	if cfg.Global.ControlSocket != "" {
		manager.SetControlSocketPath(cfg.Global.ControlSocket)
	}
	initTLSPaths()
}

func initTLSPaths() {
	relay.DefaultIdentityDir = filepath.Join(manager.DefaultConfigDir, "identity")
	kh, _ := relay.LoadKnownHosts(filepath.Join(manager.DefaultConfigDir, "known_hosts"))
	knownHosts = kh
}

// ─── Ad-hoc mode helpers ─────────────────────────────────────────────

// forwardSpec holds the pieces of a [bind:]port:host:hostport forwarding spec.
type forwardSpec struct {
	bind     string // empty means default bind will be applied
	port     uint16 // listen (local) or remote port
	host     string // target host
	hostPort uint16 // target port
}

// parseForwardSpec parses a forwarding spec of the form [bind:]port:host:hostport.
// If allowBind is false and a bind prefix is present, it returns an error.
// IPv6 literals are not supported and are rejected.
func parseForwardSpec(spec, defaultBind string, allowBind bool) (forwardSpec, error) {
	if spec == "" {
		return forwardSpec{}, fmt.Errorf("empty forwarding spec")
	}
	if strings.ContainsAny(spec, "[]") {
		return forwardSpec{}, fmt.Errorf("IPv6 literals are not supported: %q", spec)
	}

	// Split from the right: port:host:hostport.
	// First rightmost colon separates host and hostPort.
	hostPortSep := strings.LastIndexByte(spec, ':')
	if hostPortSep < 0 {
		return forwardSpec{}, fmt.Errorf("invalid format %q (expected [bind:]port:host:hostport)", spec)
	}
	hostPortStr := spec[hostPortSep+1:]
	mid := spec[:hostPortSep]

	// Second rightmost colon separates port and host.
	portHostSep := strings.LastIndexByte(mid, ':')
	if portHostSep < 0 {
		return forwardSpec{}, fmt.Errorf("invalid format %q (expected [bind:]port:host:hostport)", spec)
	}
	host := mid[portHostSep+1:]
	front := mid[:portHostSep]

	// Front is [bind:]port.
	bindSep := strings.LastIndexByte(front, ':')
	portStr := front
	var bind string
	if bindSep >= 0 {
		bind = front[:bindSep]
		portStr = front[bindSep+1:]
	}

	if host == "" {
		return forwardSpec{}, fmt.Errorf("invalid format %q: target host is empty", spec)
	}
	if bind != "" && !allowBind {
		return forwardSpec{}, fmt.Errorf("invalid format %q: bind prefix is not supported for this mode", spec)
	}
	if bind == "" {
		bind = defaultBind
	}

	port, err := parsePort(portStr)
	if err != nil {
		return forwardSpec{}, fmt.Errorf("invalid listen port in %q: %w", spec, err)
	}
	hostPort, err := parsePort(hostPortStr)
	if err != nil {
		return forwardSpec{}, fmt.Errorf("invalid target port in %q: %w", spec, err)
	}

	return forwardSpec{bind: bind, port: port, host: host, hostPort: hostPort}, nil
}

func parsePort(s string) (uint16, error) {
	if s == "" {
		return 0, fmt.Errorf("port is empty")
	}
	p, err := strconv.ParseUint(s, 10, 16)
	if err != nil {
		return 0, fmt.Errorf("%q is not a valid port", s)
	}
	return uint16(p), nil
}

func parseLocalSpec(spec string) (listenAddr, dialAddr string, err error) {
	fs, err := parseForwardSpec(spec, "127.0.0.1", true)
	if err != nil {
		return "", "", err
	}
	return net.JoinHostPort(fs.bind, strconv.Itoa(int(fs.port))),
		net.JoinHostPort(fs.host, strconv.Itoa(int(fs.hostPort))), nil
}

func parseRemoteSpec(spec string) (bind string, remotePort uint16, targetAddr string, err error) {
	fs, err := parseForwardSpec(spec, "0.0.0.0", true)
	if err != nil {
		return "", 0, "", err
	}
	return fs.bind, fs.port, net.JoinHostPort(fs.host, strconv.Itoa(int(fs.hostPort))), nil
}

func runAdhocLocal(spec, protocol string, tls bool, tlsCert, tlsKey string, tlsVerify bool) {
	if protocol == "udp" {
		if tls {
			log.Fatal("TLS is not supported with UDP forwarding")
		}
		listenAddr, dialAddr, err := parseLocalSpec(spec)
		if err != nil {
			log.Fatal(err)
		}
		log.Printf("UDP listening on %s, forwarding to %s", listenAddr, dialAddr)
		proxy, err := relay.NewUDPProxy(listenAddr, dialAddr)
		if err != nil {
			log.Fatalf("Failed to start UDP proxy: %v", err)
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
		return
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

func runAdhocRemote(remoteSpec, serverAddr, token string, tls bool, tlsCert, tlsKey string, tlsVerify bool, trustOnFirstUse bool, serverFingerprint string) {
	bind, remotePort, targetAddr, err := parseRemoteSpec(remoteSpec)
	if err != nil {
		log.Fatal(err)
	}

	var tlsCfg *relay.TLSConfig
	if tls {
		tlsCfg, err = relay.SetupTLS(tlsCert, tlsKey, tlsVerify)
		if err != nil {
			log.Fatalf("TLS setup: %v", err)
		}
		tlsCfg.TrustOnFirstUse = trustOnFirstUse
		tlsCfg.ServerFingerprint = serverFingerprint
		tlsCfg.KnownHosts = knownHosts
	}

	tunnels := []relay.RemoteTunnel{
		{RemotePort: remotePort, BindAddr: bind, TargetAddr: targetAddr},
	}

	log.Printf("Remote tunnel: %s:%d -> %s (via %s)", bind, remotePort, targetAddr, serverAddr)
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
		if tlsCfg.Fingerprint != "" {
			log.Printf("Remote server TLS identity fingerprint: %s", tlsCfg.Fingerprint)
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

func runAdhocDynamic(portSpec, user, pass string) {
	if portSpec == "" {
		log.Fatal("dynamic port cannot be empty")
	}
	listenAddr := normalizeListenAddr(portSpec)

	log.Printf("SOCKS5 proxy on %s", listenAddr)
	proxy, err := relay.NewSocksProxyWithAuth(listenAddr, user, pass)
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
