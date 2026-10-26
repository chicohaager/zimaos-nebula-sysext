// Command nebula-web serves the Nebula tile: set this host up as the
// lighthouse of a new network (its own CA) or join an existing one, sign
// further nodes and hand out a ready config.yml for each, switch Nebula on,
// off and on at boot (nebula-zima). The ZFW rule for the listen port is set
// by the page itself.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chicohaager/lintux-modkit/httpx"
	"github.com/chicohaager/lintux-modkit/webmod"
)

// version is set at build time from VERSION (-ldflags -X main.version=...).
var version = "dev"

// Paths and commands; tests point them elsewhere.
var (
	confDir = "/DATA/AppData/nebula"
	unitDir = "/etc/systemd/system"
	run     = func(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
		c := exec.CommandContext(ctx, name, args...)
		c.Dir = dir
		return c.CombinedOutput()
	}
)

// caDuration: nebula-cert's default CA lifetime is one year (measured with
// v1.11.2: notAfter one year out), and every node certificate ends with its
// CA. The tile's CA lasts ten years.
const caDuration = "87600h"

const dev = "nebula1"

var mu sync.Mutex

func main() {
	webmod.Run(webmod.Options{
		Name: "nebula", Service: "nebula-web", Binary: "/usr/bin/nebula-web",
		Route: "/nebula", Version: version, API: api,
		// nebula keeps the old binary when zpkg replaces the module (it restarts only
		// nebula-web): webmod restarts it then (lintux-modkit stale).
		Restart: []string{"nebula.service"},
	})
}

func api(verify func(http.Handler) http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/nebula/status", verify(http.HandlerFunc(statusHandler)))
	mux.Handle("/nebula/setup", verify(post(setupHandler)))
	mux.Handle("/nebula/nodes", verify(post(signNode)))
	mux.Handle("/nebula/action", verify(post(actionHandler)))
	mux.Handle("/nebula/reset", verify(post(resetHandler)))
	return mux
}

func post(h func(http.ResponseWriter, *http.Request)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			httpx.MethodNotAllowed(w)
			return
		}
		h(w, r)
	})
}

func p(name string) string { return filepath.Join(confDir, name) }

func ctxFor(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), 60*time.Second)
}

// Tile is what the tile wrote at setup (tile.json next to the config).
type Tile struct {
	Mode       string `json:"mode"` // lighthouse | node
	Network    string `json:"network"`
	CIDR       string `json:"cidr"`
	ListenPort int    `json:"listen_port"`
	Lighthouse string `json:"lighthouse,omitempty"`          // overlay IP of the lighthouse (node mode)
	Endpoint   string `json:"lighthouse_endpoint,omitempty"` // its public host:port (node mode)
}

// Node is a certificate signed here.
type Node struct {
	Name     string    `json:"name"`
	IP       string    `json:"ip"`
	Signed   time.Time `json:"signed"`
	NotAfter string    `json:"not_after"`
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(path, string(b)+"\n", 0o600)
}

func writeFile(path, text string, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.WriteString(text); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// ---------- certificates ----------

// CertInfo is the part of `nebula-cert print -json` the tile shows.
type CertInfo struct {
	Name     string   `json:"name"`
	Networks []string `json:"networks"`
	NotAfter string   `json:"not_after"`
	IsCA     bool     `json:"is_ca"`
}

func certInfo(ctx context.Context, path string) (CertInfo, error) {
	out, err := run(ctx, confDir, "nebula-cert", "print", "-json", "-path", path)
	if err != nil {
		return CertInfo{}, fmt.Errorf("nebula-cert print: %s", strings.TrimSpace(string(out)))
	}
	var certs []struct {
		Details struct {
			Name     string   `json:"name"`
			Networks []string `json:"networks"`
			NotAfter string   `json:"notAfter"`
			IsCA     bool     `json:"isCa"`
		} `json:"details"`
	}
	if err := json.Unmarshal(out, &certs); err != nil || len(certs) == 0 {
		return CertInfo{}, fmt.Errorf("nebula-cert print: unreadable output")
	}
	d := certs[0].Details
	return CertInfo{Name: d.Name, Networks: d.Networks, NotAfter: d.NotAfter, IsCA: d.IsCA}, nil
}

// ---------- status ----------

func statusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.MethodNotAllowed(w)
		return
	}
	ctx, cancel := ctxFor(r)
	defer cancel()
	out := map[string]any{"conf_dir": confDir, "configured": false}
	if _, err := os.Stat(p("config.yml")); err != nil {
		httpx.WriteJSON(w, http.StatusOK, out)
		return
	}
	out["configured"] = true
	var t Tile
	if err := readJSON(p("tile.json"), &t); err == nil {
		out["tile"] = t
	} else {
		out["tile"] = nil // a config the tile did not write: status only
	}
	a, _ := run(ctx, "", "systemctl", "is-active", "nebula.service")
	out["active"] = strings.TrimSpace(string(a))
	_, err := os.Stat(filepath.Join(unitDir, "nebula-watchdog.timer"))
	out["boot"] = err == nil
	if ci, err := certInfo(ctx, p("host.crt")); err == nil {
		out["host"] = ci
	} else {
		out["host_error"] = err.Error()
	}
	if ci, err := certInfo(ctx, p("ca.crt")); err == nil {
		out["ca"] = ci
	}
	_, err = os.Stat(p("ca.key"))
	out["can_sign"] = err == nil
	var nodes []Node
	_ = readJSON(p("nodes.json"), &nodes)
	if nodes == nil {
		nodes = []Node{}
	}
	out["nodes"] = nodes
	if a, err := run(ctx, "", "ip", "-br", "addr", "show", "dev", dev); err == nil {
		out["interface"] = strings.Join(strings.Fields(string(a)), " ")
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// ---------- setup ----------

var (
	nameRe = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,40}$`)
	netRe  = regexp.MustCompile(`^[\p{L}\p{N} _.-]{1,60}$`)
)

func validEndpoint(s string) bool {
	host, port, ok := strings.Cut(s, ":")
	if strings.HasPrefix(s, "[") {
		i := strings.LastIndex(s, "]:")
		if i < 0 {
			return false
		}
		host, port, ok = s[1:i], s[i+2:], true
	}
	n, err := strconv.Atoi(port)
	if !ok || host == "" || err != nil || n < 1 || n > 65535 {
		return false
	}
	if strings.HasPrefix(s, "[") { // an IPv6 address: hex digits and colons only
		a, err := netip.ParseAddr(host)
		return err == nil && a.Is6()
	}
	// The endpoint lands inside a quoted YAML list: nothing that could end it.
	return !strings.ContainsAny(host, " \t\n#\"'{}[],:")
}

func setupHandler(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Mode       string `json:"mode"` // new | join
		Network    string `json:"network"`
		CIDR       string `json:"cidr"`
		NodeName   string `json:"node_name"`
		ListenPort int    `json:"listen_port"`
		CACert     string `json:"ca_crt"`
		HostCert   string `json:"host_crt"`
		HostKey    string `json:"host_key"`
		Lighthouse string `json:"lighthouse"`
		Endpoint   string `json:"lighthouse_endpoint"`
	}
	if !httpx.Decode(w, r, &in) {
		return
	}
	if in.ListenPort < 0 || in.ListenPort > 65535 {
		httpx.WriteError(w, http.StatusBadRequest, "bad_port", "listen port: 0–65535 (0: any free port, fine for a node nobody connects to)")
		return
	}
	mu.Lock()
	defer mu.Unlock()
	if _, err := os.Stat(p("config.yml")); err == nil {
		httpx.WriteError(w, http.StatusConflict, "configured", "Nebula is set up already — reset it first")
		return
	}
	if err := os.MkdirAll(confDir, 0o700); err != nil {
		httpx.WriteErr(w, err)
		return
	}
	ctx, cancel := ctxFor(r)
	defer cancel()
	switch in.Mode {
	case "new":
		setupNew(ctx, w, in.Network, in.CIDR, in.NodeName, in.ListenPort)
	case "join":
		setupJoin(ctx, w, in.CACert, in.HostCert, in.HostKey, in.Lighthouse, in.Endpoint, in.ListenPort)
	default:
		httpx.WriteError(w, http.StatusBadRequest, "bad_mode", "mode: new or join")
	}
}

func setupNew(ctx context.Context, w http.ResponseWriter, network, cidr, node string, port int) {
	if !netRe.MatchString(network) || !nameRe.MatchString(node) {
		httpx.WriteError(w, http.StatusBadRequest, "bad_name", "network name: up to 60 letters, digits, spaces; node name: up to 40 letters, digits and _.-")
		return
	}
	pre, err := netip.ParsePrefix(cidr)
	if err != nil || !pre.Addr().Is4() || pre.Bits() < 8 || pre.Bits() > 29 {
		httpx.WriteError(w, http.StatusBadRequest, "bad_cidr", "network: an IPv4 range like 10.42.0.0/24")
		return
	}
	pre = pre.Masked()
	ip := pre.Addr().Next()
	if port == 0 {
		httpx.WriteError(w, http.StatusBadRequest, "bad_port", "a lighthouse needs a fixed listen port, e.g. 4242")
		return
	}
	steps := [][]string{
		{"nebula-cert", "ca", "-name", network, "-duration", caDuration, "-out-crt", p("ca.crt"), "-out-key", p("ca.key")},
		{"nebula-cert", "sign", "-name", node, "-networks", netip.PrefixFrom(ip, pre.Bits()).String(),
			"-ca-crt", p("ca.crt"), "-ca-key", p("ca.key"), "-out-crt", p("host.crt"), "-out-key", p("host.key")},
	}
	for _, s := range steps {
		if out, err := run(ctx, confDir, s[0], s[1:]...); err != nil {
			cleanupSetup()
			httpx.WriteError(w, http.StatusBadGateway, "nebula_cert", strings.TrimSpace(string(out)))
			return
		}
	}
	cfg := configYAML(p("ca.crt"), p("host.crt"), p("host.key"), true, "", "", port)
	if err := finishSetup(ctx, cfg, Tile{Mode: "lighthouse", Network: network, CIDR: pre.String(), ListenPort: port}); err != nil {
		cleanupSetup()
		httpx.WriteError(w, http.StatusBadGateway, "config", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "set up", "ip": ip.String()})
}

func setupJoin(ctx context.Context, w http.ResponseWriter, ca, crt, key, lighthouse, endpoint string, port int) {
	if !strings.Contains(ca, "-----BEGIN NEBULA CERTIFICATE") || !strings.Contains(crt, "-----BEGIN NEBULA CERTIFICATE") ||
		!strings.Contains(key, "-----BEGIN NEBULA") || !strings.Contains(key, "PRIVATE KEY") {
		httpx.WriteError(w, http.StatusBadRequest, "bad_pem", "paste ca.crt, this node's .crt and its .key as nebula-cert wrote them")
		return
	}
	lh, err := netip.ParseAddr(lighthouse)
	if err != nil || !lh.Is4() || !validEndpoint(endpoint) {
		httpx.WriteError(w, http.StatusBadRequest, "bad_lighthouse", "lighthouse: its overlay IP (e.g. 10.42.0.1) and how to reach it, host:port")
		return
	}
	for name, text := range map[string]string{"ca.crt": ca, "host.crt": crt, "host.key": key} {
		if err := writeFile(p(name), strings.TrimSpace(text)+"\n", 0o600); err != nil {
			cleanupSetup()
			httpx.WriteErr(w, err)
			return
		}
	}
	ci, err := certInfo(ctx, p("host.crt"))
	if err != nil || len(ci.Networks) == 0 {
		cleanupSetup()
		httpx.WriteError(w, http.StatusBadRequest, "bad_cert", "this node's certificate is unreadable")
		return
	}
	cidr, _ := netip.ParsePrefix(ci.Networks[0])
	cfg := configYAML(p("ca.crt"), p("host.crt"), p("host.key"), false, lh.String(), endpoint, port)
	t := Tile{Mode: "node", CIDR: cidr.Masked().String(), ListenPort: port, Lighthouse: lh.String(), Endpoint: endpoint}
	if err := finishSetup(ctx, cfg, t); err != nil {
		cleanupSetup()
		httpx.WriteError(w, http.StatusBadRequest, "config", err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "joined", "ip": cidr.Addr().String()})
}

// finishSetup writes config.yml and tile.json and checks the config with
// `nebula -test` before anything starts.
func finishSetup(ctx context.Context, cfg string, t Tile) error {
	if err := writeFile(p("config.yml"), cfg, 0o600); err != nil {
		return err
	}
	if out, err := run(ctx, confDir, "nebula", "-test", "-config", p("config.yml")); err != nil {
		return fmt.Errorf("nebula -test: %s", strings.TrimSpace(string(out)))
	}
	return writeJSON(p("tile.json"), t)
}

func cleanupSetup() {
	for _, f := range []string{"ca.crt", "ca.key", "host.crt", "host.key", "config.yml", "tile.json"} {
		_ = os.Remove(p(f))
	}
}

// configYAML renders a config. ca/cert/key are paths or inline PEM (nebula
// accepts both: pki.go checks for "-----BEGIN"). A lighthouse has no
// lighthouse to report to; a node names one with its public endpoint.
func configYAML(ca, cert, key string, isLighthouse bool, lighthouse, endpoint string, port int) string {
	var b strings.Builder
	b.WriteString("# Written by the Nebula tile (nebula module for ZimaOS).\npki:\n")
	for _, kv := range [][2]string{{"ca", ca}, {"cert", cert}, {"key", key}} {
		if strings.Contains(kv[1], "-----BEGIN") {
			fmt.Fprintf(&b, "  %s: |\n", kv[0])
			for _, l := range strings.Split(strings.TrimSpace(kv[1]), "\n") {
				b.WriteString("    " + l + "\n")
			}
		} else {
			fmt.Fprintf(&b, "  %s: %s\n", kv[0], kv[1])
		}
	}
	if isLighthouse {
		b.WriteString("static_host_map: {}\nlighthouse:\n  am_lighthouse: true\n")
	} else {
		fmt.Fprintf(&b, "static_host_map:\n  \"%s\": [\"%s\"]\nlighthouse:\n  am_lighthouse: false\n  interval: 60\n  hosts: [\"%s\"]\n", lighthouse, endpoint, lighthouse)
	}
	fmt.Fprintf(&b, `listen:
  host: 0.0.0.0
  port: %d
punchy:
  punch: true
tun:
  dev: %s
firewall:
  outbound:
    - {port: any, proto: any, host: any}
  inbound:
    - {port: any, proto: any, host: any}
`, port, dev)
	return b.String()
}

// ---------- nodes ----------

func signNode(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name     string `json:"name"`
		IP       string `json:"ip"`       // optional: next free
		Endpoint string `json:"endpoint"` // this lighthouse as the node reaches it
	}
	if !httpx.Decode(w, r, &in) {
		return
	}
	if !nameRe.MatchString(in.Name) || !validEndpoint(in.Endpoint) {
		httpx.WriteError(w, http.StatusBadRequest, "bad_request", "name: up to 40 letters, digits and _.-; endpoint: how the node reaches this host, host:port")
		return
	}
	mu.Lock()
	defer mu.Unlock()
	var t Tile
	if err := readJSON(p("tile.json"), &t); err != nil || t.Mode != "lighthouse" {
		httpx.WriteError(w, http.StatusConflict, "not_lighthouse", "only a lighthouse set up here with its own CA can sign nodes")
		return
	}
	if _, err := os.Stat(p("ca.key")); err != nil {
		httpx.WriteError(w, http.StatusConflict, "no_ca_key", "the CA key is not on this host")
		return
	}
	var nodes []Node
	_ = readJSON(p("nodes.json"), &nodes)
	pre, _ := netip.ParsePrefix(t.CIDR)
	ip, err := pickIP(pre, in.IP, nodes)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "bad_ip", err.Error())
		return
	}
	ctx, cancel := ctxFor(r)
	defer cancel()
	tmp, err := os.MkdirTemp(confDir, ".sign-*")
	if err != nil {
		httpx.WriteErr(w, err)
		return
	}
	defer os.RemoveAll(tmp)
	crtPath, keyPath := filepath.Join(tmp, "node.crt"), filepath.Join(tmp, "node.key")
	if out, err := run(ctx, confDir, "nebula-cert", "sign", "-name", in.Name, "-networks", netip.PrefixFrom(ip, pre.Bits()).String(),
		"-ca-crt", p("ca.crt"), "-ca-key", p("ca.key"), "-out-crt", crtPath, "-out-key", keyPath); err != nil {
		httpx.WriteError(w, http.StatusBadGateway, "nebula_cert", strings.TrimSpace(string(out)))
		return
	}
	ca, err1 := os.ReadFile(p("ca.crt"))
	crt, err2 := os.ReadFile(crtPath)
	key, err3 := os.ReadFile(keyPath)
	if err1 != nil || err2 != nil || err3 != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "read", "reading the signed files failed")
		return
	}
	lighthouse := pre.Addr().Next() // the tile gives the lighthouse the first address
	cfg := configYAML(string(ca), string(crt), string(key), false, lighthouse.String(), in.Endpoint, 0)
	notAfter := ""
	if ci, err := certInfo(ctx, crtPath); err == nil {
		notAfter = ci.NotAfter
	}
	nodes = append(nodes, Node{Name: in.Name, IP: ip.String(), Signed: time.Now().UTC(), NotAfter: notAfter})
	if err := writeJSON(p("nodes.json"), nodes); err != nil {
		httpx.WriteErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "signed", "ip": ip.String(), "config": cfg})
}

// pickIP returns the wanted address after checks, or the next free one; the
// lighthouse holds the first address of the range.
func pickIP(pre netip.Prefix, want string, nodes []Node) (netip.Addr, error) {
	used := map[netip.Addr]bool{pre.Addr().Next(): true}
	for _, n := range nodes {
		if a, err := netip.ParseAddr(n.IP); err == nil {
			used[a] = true
		}
	}
	last := lastAddr(pre)
	if want != "" {
		a, err := netip.ParseAddr(want)
		if err != nil || !pre.Contains(a) || a == pre.Addr() || a == last {
			return netip.Addr{}, fmt.Errorf("IP: an address inside %s", pre)
		}
		if used[a] {
			return netip.Addr{}, fmt.Errorf("%s is taken", a)
		}
		return a, nil
	}
	for a := pre.Addr().Next(); pre.Contains(a) && a != last; a = a.Next() {
		if !used[a] {
			return a, nil
		}
	}
	return netip.Addr{}, fmt.Errorf("no free address left in %s", pre)
}

func lastAddr(p netip.Prefix) netip.Addr {
	b := p.Addr().As4()
	for i := p.Bits(); i < 32; i++ {
		b[i/8] |= 1 << (7 - i%8)
	}
	return netip.AddrFrom4(b)
}

// ---------- actions ----------

func actionHandler(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Action string `json:"action"`
	}
	if !httpx.Decode(w, r, &in) {
		return
	}
	var cmd []string
	switch in.Action {
	case "enable":
		cmd = []string{"nebula-zima", "enable"}
	case "disable":
		cmd = []string{"nebula-zima", "disable"}
	case "restart":
		cmd = []string{"systemctl", "restart", "nebula.service"}
	default:
		httpx.WriteError(w, http.StatusBadRequest, "bad_action", "action: enable, disable or restart")
		return
	}
	mu.Lock()
	defer mu.Unlock()
	ctx, cancel := ctxFor(r)
	defer cancel()
	if out, err := run(ctx, "", cmd[0], cmd[1:]...); err != nil {
		httpx.WriteError(w, http.StatusBadGateway, "failed", strings.TrimSpace(string(out)))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// resetHandler switches Nebula off and moves the whole setup aside — it holds
// the CA key and node keys, so it is kept, not deleted.
func resetHandler(w http.ResponseWriter, r *http.Request) {
	mu.Lock()
	defer mu.Unlock()
	ctx, cancel := ctxFor(r)
	defer cancel()
	if out, err := run(ctx, "", "nebula-zima", "disable"); err != nil {
		httpx.WriteError(w, http.StatusBadGateway, "disable_failed", strings.TrimSpace(string(out)))
		return
	}
	dst := filepath.Join(confDir, ".reset", time.Now().UTC().Format("20060102T150405Z"))
	if err := os.MkdirAll(dst, 0o700); err != nil {
		httpx.WriteErr(w, err)
		return
	}
	moved := 0
	for _, f := range []string{"ca.crt", "ca.key", "host.crt", "host.key", "config.yml", "tile.json", "nodes.json"} {
		if err := os.Rename(p(f), filepath.Join(dst, f)); err == nil {
			moved++
		}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"status": "reset", "kept_in": dst, "files": moved})
}
