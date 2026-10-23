package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The handler tests run the real nebula-cert and nebula when NEBULA_BIN points
// at a directory holding both (build.sh sets it to the downloaded release);
// without them they are skipped, the pure tests still run.
func realRun(t *testing.T) {
	t.Helper()
	dir := os.Getenv("NEBULA_BIN")
	if dir == "" {
		t.Skip("NEBULA_BIN not set")
	}
	confDir = t.TempDir()
	unitDir = t.TempDir()
	run = func(ctx context.Context, wd, name string, args ...string) ([]byte, error) {
		switch name {
		case "nebula", "nebula-cert":
			name = filepath.Join(dir, name)
		case "systemctl":
			return []byte("inactive\n"), nil
		case "ip", "nebula-zima":
			return nil, nil
		}
		c := exec.CommandContext(ctx, name, args...)
		c.Dir = wd
		return c.CombinedOutput()
	}
}

func call(t *testing.T, method, path string, body any) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	w := httptest.NewRecorder()
	api(func(h http.Handler) http.Handler { return h }).ServeHTTP(w, httptest.NewRequest(method, path, &buf))
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w, out
}

func TestPickIP(t *testing.T) {
	pre := netip.MustParsePrefix("10.42.0.0/29")
	a, err := pickIP(pre, "", []Node{{IP: "10.42.0.2"}})
	if err != nil || a.String() != "10.42.0.3" {
		t.Fatalf("next = %v %v", a, err)
	}
	for _, bad := range []string{"10.42.0.1", "10.42.0.2", "10.42.0.0", "10.42.0.7", "10.43.0.5", "x"} {
		if _, err := pickIP(pre, bad, []Node{{IP: "10.42.0.2"}}); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
	full := []Node{{IP: "10.42.0.2"}, {IP: "10.42.0.3"}, {IP: "10.42.0.4"}, {IP: "10.42.0.5"}, {IP: "10.42.0.6"}}
	if _, err := pickIP(pre, "", full); err == nil {
		t.Fatal("full range gave an address")
	}
}

func TestValidEndpointRejectsYAMLBreakers(t *testing.T) {
	for _, s := range []string{"vpn.example.org:4242", "203.0.113.5:4242", "[2001:db8::1]:4242"} {
		if !validEndpoint(s) {
			t.Errorf("%q rejected", s)
		}
	}
	for _, s := range []string{"h:0", "h", "a\"]:1", "h\nx:1", "a,b:1", "h}:1"} {
		if validEndpoint(s) {
			t.Errorf("%q accepted", s)
		}
	}
}

func TestNewNetworkSignsANodeWithAWorkingConfig(t *testing.T) {
	realRun(t)
	w, out := call(t, "POST", "/nebula/setup", map[string]any{"mode": "new", "network": "Home net", "cidr": "10.42.0.0/24", "node_name": "zima", "listen_port": 4242})
	if w.Code != 200 || out["ip"] != "10.42.0.1" {
		t.Fatalf("setup: %d %s", w.Code, w.Body)
	}
	for _, f := range []string{"ca.crt", "ca.key", "host.crt", "host.key", "config.yml", "tile.json"} {
		fi, err := os.Stat(filepath.Join(confDir, f))
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("%s: %v %v", f, fi, err)
		}
	}
	// the CA lasts ten years, not nebula-cert's default one
	ci, err := certInfo(context.Background(), filepath.Join(confDir, "ca.crt"))
	if err != nil || !strings.HasPrefix(ci.NotAfter, "2036") && !strings.HasPrefix(ci.NotAfter, "2035") {
		t.Errorf("CA not after %q, %v", ci.NotAfter, err)
	}
	if w, _ := call(t, "POST", "/nebula/setup", map[string]any{"mode": "new", "network": "x", "cidr": "10.42.0.0/24", "node_name": "y", "listen_port": 4242}); w.Code != http.StatusConflict {
		t.Fatalf("second setup: %d", w.Code)
	}

	w, out = call(t, "POST", "/nebula/nodes", map[string]any{"name": "laptop", "endpoint": "vpn.example.org:4242"})
	if w.Code != 200 || out["ip"] != "10.42.0.2" {
		t.Fatalf("sign: %d %s", w.Code, w.Body)
	}
	cfg := out["config"].(string)
	if !strings.Contains(cfg, `"10.42.0.1": ["vpn.example.org:4242"]`) || strings.Count(cfg, "-----BEGIN") != 3 || strings.Contains(cfg, "ca.key") {
		t.Fatalf("node config:\n%s", cfg)
	}
	// nebula itself must accept the handed-out file
	p := filepath.Join(t.TempDir(), "node.yml")
	if err := os.WriteFile(p, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if b, err := run(context.Background(), "", "nebula", "-test", "-config", p); err != nil {
		t.Fatalf("nebula -test on the node config: %s", b)
	}
	if entries, _ := filepath.Glob(filepath.Join(confDir, ".sign-*")); len(entries) != 0 {
		t.Fatalf("signing left key material behind: %v", entries)
	}
	_, st := call(t, "GET", "/nebula/status", nil)
	if st["configured"] != true || st["can_sign"] != true || len(st["nodes"].([]any)) != 1 {
		t.Fatalf("status %v", st)
	}
}

func TestSetupRejectsBadInput(t *testing.T) {
	realRun(t)
	for _, in := range []map[string]any{
		{"mode": "new", "network": "n", "cidr": "10.42.0.0/31", "node_name": "z", "listen_port": 4242},
		{"mode": "new", "network": "n", "cidr": "fd00::/64", "node_name": "z", "listen_port": 4242},
		{"mode": "new", "network": "n\nx", "cidr": "10.42.0.0/24", "node_name": "z", "listen_port": 4242},
		{"mode": "new", "network": "n", "cidr": "10.42.0.0/24", "node_name": "z", "listen_port": 0},
		{"mode": "join", "ca_crt": "x", "host_crt": "y", "host_key": "z", "lighthouse": "10.42.0.1", "lighthouse_endpoint": "h:4242"},
		{"mode": "other"},
	} {
		if w, _ := call(t, "POST", "/nebula/setup", in); w.Code != http.StatusBadRequest {
			t.Errorf("%v: %d %s", in, w.Code, w.Body)
		}
	}
	if _, err := os.Stat(filepath.Join(confDir, "config.yml")); err == nil {
		t.Fatal("a rejected setup left a config")
	}
}

func TestJoinWithAConfigFromALighthouse(t *testing.T) {
	realRun(t)
	// a lighthouse elsewhere: its CA signs this node
	lh := t.TempDir()
	if b, err := run(context.Background(), lh, "nebula-cert", "ca", "-name", "other"); err != nil {
		t.Fatal(string(b))
	}
	if b, err := run(context.Background(), lh, "nebula-cert", "sign", "-name", "zima", "-networks", "10.50.0.7/24"); err != nil {
		t.Fatal(string(b))
	}
	read := func(f string) string { b, _ := os.ReadFile(filepath.Join(lh, f)); return string(b) }
	w, out := call(t, "POST", "/nebula/setup", map[string]any{"mode": "join", "ca_crt": read("ca.crt"), "host_crt": read("zima.crt"), "host_key": read("zima.key"),
		"lighthouse": "10.50.0.1", "lighthouse_endpoint": "lh.example.org:4242", "listen_port": 0})
	if w.Code != 200 || out["ip"] != "10.50.0.7" {
		t.Fatalf("join: %d %s", w.Code, w.Body)
	}
	if w, _ := call(t, "POST", "/nebula/nodes", map[string]any{"name": "x", "endpoint": "h:1"}); w.Code != http.StatusConflict {
		t.Fatalf("a node without the CA key signed: %d", w.Code)
	}
}

func TestResetKeepsTheKeys(t *testing.T) {
	realRun(t)
	call(t, "POST", "/nebula/setup", map[string]any{"mode": "new", "network": "n", "cidr": "10.42.0.0/24", "node_name": "z", "listen_port": 4242})
	w, out := call(t, "POST", "/nebula/reset", map[string]any{})
	if w.Code != 200 || out["files"] != float64(6) {
		t.Fatalf("reset: %d %s", w.Code, w.Body)
	}
	if _, err := os.Stat(filepath.Join(out["kept_in"].(string), "ca.key")); err != nil {
		t.Fatal("CA key not kept")
	}
	if _, st := call(t, "GET", "/nebula/status", nil); st["configured"] != false {
		t.Fatalf("still configured: %v", st)
	}
}
