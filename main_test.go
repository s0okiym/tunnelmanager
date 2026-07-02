package main

import "testing"

func TestBuildTunnelConfigLocal(t *testing.T) {
	tc, err := buildTunnelConfig(addParams{local: "8080:web:80"})
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
	tc, err := buildTunnelConfig(addParams{dynamic: "1080"})
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
