package main

import "testing"

func TestBuildTunnelConfigLocal(t *testing.T) {
	tc, err := buildTunnelConfig(addParams{local: "8080:web:80", autostart: true})
	if err != nil {
		t.Fatal(err)
	}
	if tc.Mode != "local" {
		t.Fatalf("mode=%s", tc.Mode)
	}
	if tc.Local != "127.0.0.1:8080" {
		t.Fatalf("local=%s", tc.Local)
	}
	if tc.Remote != "web:80" {
		t.Fatalf("remote=%s", tc.Remote)
	}
	if !tc.Autostart {
		t.Fatal("autostart should be true")
	}
	if tc.Name != "local-8080:web:80" {
		t.Fatalf("name=%s", tc.Name)
	}
}

// -D previously hit the default branch and fatally errored; it must now work.
func TestBuildTunnelConfigDynamic(t *testing.T) {
	tc, err := buildTunnelConfig(addParams{dynamic: "1080", autostart: true})
	if err != nil {
		t.Fatalf("dynamic add should not error: %v", err)
	}
	if tc.Mode != "dynamic" {
		t.Fatalf("mode=%s", tc.Mode)
	}
	if tc.Local != "127.0.0.1:1080" {
		t.Fatalf("local=%s", tc.Local)
	}
	if !tc.Autostart {
		t.Fatal("autostart should be true")
	}
}

func TestBuildTunnelConfigDynamicWithBind(t *testing.T) {
	tc, err := buildTunnelConfig(addParams{dynamic: "127.0.0.1:1080"})
	if err != nil {
		t.Fatalf("dynamic add with bind should not error: %v", err)
	}
	if tc.Local != "127.0.0.1:1080" {
		t.Fatalf("local=%s", tc.Local)
	}
}

// Remote add must capture -s (previously it was silently dropped).
func TestBuildTunnelConfigRemoteRequiresServer(t *testing.T) {
	if _, err := buildTunnelConfig(addParams{remote: "9090:localhost:8080"}); err == nil {
		t.Fatal("remote add without -s should error")
	}
}

func TestBuildTunnelConfigRemote(t *testing.T) {
	tc, err := buildTunnelConfig(addParams{
		remote: "9090:localhost:8080", server: "vps:9000",
		token: "sekret", tls: true, tlsVerify: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if tc.Mode != "remote" {
		t.Fatalf("mode=%s", tc.Mode)
	}
	if tc.Server != "vps:9000" {
		t.Fatalf("server=%s", tc.Server)
	}
	if tc.Token != "sekret" {
		t.Fatalf("token=%s", tc.Token)
	}
	if !tc.TLS || !tc.TLSVerify {
		t.Fatal("tls flags not captured")
	}
	if tc.Remote != "9090:localhost:8080" {
		t.Fatalf("remote=%s", tc.Remote)
	}
}

func TestBuildTunnelConfigNone(t *testing.T) {
	if _, err := buildTunnelConfig(addParams{}); err == nil {
		t.Fatal("no mode should error")
	}
}

func TestBuildTunnelConfigGroupAndAutostart(t *testing.T) {
	tc, err := buildTunnelConfig(addParams{
		local: "8080:web:80", group: "g1", autostart: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if tc.Group != "g1" {
		t.Fatalf("group=%s", tc.Group)
	}
	if tc.Autostart {
		t.Fatal("autostart should be false")
	}
}

func TestParseLocalSpec(t *testing.T) {
	cases := []struct {
		spec       string
		listenAddr string
		dialAddr   string
	}{
		{"8080:web:80", "127.0.0.1:8080", "web:80"},
		{"0.0.0.0:8080:web:80", "0.0.0.0:8080", "web:80"},
		{"3306:db.internal:3306", "127.0.0.1:3306", "db.internal:3306"},
	}
	for _, c := range cases {
		l, d, err := parseLocalSpec(c.spec)
		if err != nil {
			t.Fatalf("parseLocalSpec(%q) error: %v", c.spec, err)
		}
		if l != c.listenAddr || d != c.dialAddr {
			t.Fatalf("parseLocalSpec(%q) = %q,%q, want %q,%q", c.spec, l, d, c.listenAddr, c.dialAddr)
		}
	}
}

func TestParseLocalSpecInvalid(t *testing.T) {
	invalid := []string{
		"",
		"8080",
		"abc:web:80",
		"8080:web:abc",
		"8080:web:99999",
		"[::1]:8080:[::1]:80",
	}
	for _, spec := range invalid {
		if _, _, err := parseLocalSpec(spec); err == nil {
			t.Fatalf("parseLocalSpec(%q) should error", spec)
		}
	}
}

func TestParseRemoteSpec(t *testing.T) {
	bind, port, target, err := parseRemoteSpec("9090:localhost:8080")
	if err != nil {
		t.Fatal(err)
	}
	if bind != "0.0.0.0" {
		t.Fatalf("bind=%s", bind)
	}
	if port != 9090 || target != "localhost:8080" {
		t.Fatalf("got port=%d target=%s", port, target)
	}
}

func TestParseRemoteSpecWithBind(t *testing.T) {
	bind, port, target, err := parseRemoteSpec("127.0.0.1:9090:localhost:8080")
	if err != nil {
		t.Fatal(err)
	}
	if bind != "127.0.0.1" {
		t.Fatalf("bind=%s", bind)
	}
	if port != 9090 || target != "localhost:8080" {
		t.Fatalf("got port=%d target=%s", port, target)
	}
}

func TestParseRemoteSpecInvalid(t *testing.T) {
	invalid := []string{
		"",
		"9090",
		"abc:localhost:8080",
		"9090:localhost:abc",
		"9090:localhost:99999",
		"[::1]:9090:localhost:8080",
	}
	for _, spec := range invalid {
		if _, _, _, err := parseRemoteSpec(spec); err == nil {
			t.Fatalf("parseRemoteSpec(%q) should error", spec)
		}
	}
}

func TestBuildTunnelConfigRemoteFingerprint(t *testing.T) {
	tc, err := buildTunnelConfig(addParams{
		remote: "9090:localhost:8080", server: "vps:9000",
		tls: true, tlsTrustOnFirstUse: true,
		tlsServerFingerprint: "SHA256:ABC123",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !tc.TLS {
		t.Fatal("tls should be true")
	}
	if !tc.TLSTrustOnFirstUse {
		t.Fatal("trust-on-first-use not captured")
	}
	if tc.TLSServerFingerprint != "SHA256:ABC123" {
		t.Fatalf("server fingerprint not captured: %s", tc.TLSServerFingerprint)
	}
}
