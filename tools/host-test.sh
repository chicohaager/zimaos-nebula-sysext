#!/bin/sh
# Run on a ZimaOS host as root after `zpkg install /tmp/nebula.raw`.
# The host becomes a lighthouse; a second nebula node runs in its own network namespace
# behind a veth pair, so the packets cross the overlay and need a handshake.
#   sudo sh host-test.sh          set up, test, leave nebula running (for a reboot test)
#   sudo sh host-test.sh cleanup  remove everything this test created
# Test names: namespace nbtpeer, veth vnbt0/vnbt1, tun nebt0/nebp0, 10.234.0.0/30 and 10.233.0.0/24,
# test files in /DATA/AppData/nebula (config.yml, test CA) and /DATA/AppData/nebula-testpeer.
set -u
D=/DATA/AppData/nebula; P=/DATA/AppData/nebula-testpeer; NS=nbtpeer
ok=0; bad=0
pass() { ok=$((ok + 1)); echo "OK   $1"; }
fail() { bad=$((bad + 1)); echo "FAIL $1"; }

# TCP probe with python3 (ZimaOS' ping is a BusyBox applet that exits 1 without output, even
# for --help, measured 1.8.0). The host answers "pong"; the client connects from the namespace.
probe() { # <ns> <ip> <port> -> 0 when "pong" came back
  python3 -c 'import socket,sys
s=socket.socket(); s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
s.bind(("0.0.0.0", int(sys.argv[1]))); s.listen(1); s.settimeout(8)
try:
    c,_=s.accept(); c.sendall(b"pong\n")
except OSError: pass' "$3" &
  lp=$!; sleep 0.5
  r=$(ip netns exec "$1" python3 -c 'import socket,sys
try:
    c=socket.create_connection((sys.argv[1], int(sys.argv[2])), timeout=3); print(c.recv(10).decode().strip())
except OSError as e: print("no:", e)' "$2" "$3")
  kill "$lp" 2>/dev/null; wait "$lp" 2>/dev/null
  [ "$r" = pong ]
}
# ZFW (if installed) drops what it does not know: open the test paths for the duration of the test,
# marked with a comment so cleanup removes exactly these. zfw.service restores its own rules at boot.
zfw_open() { iptables -S ZFW-IN >/dev/null 2>&1 || return 0
  for spec in "$@"; do eval "iptables -I ZFW-IN 1 $spec -m comment --comment module-host-test -j ACCEPT"; done; }
zfw_close() { iptables -S ZFW-IN 2>/dev/null | grep -- '--comment module-host-test' | sed 's/^-A /-D /' | while read -r r; do eval "iptables $r"; done; }

if [ "${1:-}" = cleanup ]; then
  zfw_close
  nebula-zima disable
  if [ -f "$P/peer.pid" ]; then pid=$(cat "$P/peer.pid"); [ "$(ps -p "$pid" -o comm=)" = nebula ] && kill "$pid"; fi
  ip netns del "$NS" 2>/dev/null; ip link del vnbt0 2>/dev/null
  rm -f "$D/config.yml" "$D/ca.crt" "$D/host.crt" "$D/host.key" "$P/peer.yml" "$P/peer.crt" "$P/peer.key" "$P/ca.crt" "$P/peer.pid" "$P/peer.log"
  rmdir "$D" "$P" 2>/dev/null
  echo "cleanup done: $(ip -o link show | grep -c 'nebt0\|vnbt0') test interfaces left"
  exit 0
fi

echo "zfw: $(systemctl is-active zfw.service 2>/dev/null || true), chain ZFW-IN: $(iptables -S ZFW-IN >/dev/null 2>&1 && echo present || echo absent)"
umask 077; mkdir -p "$D" "$P"
[ -e "$D/config.yml" ] && { echo "refusing: $D/config.yml exists (not a test setup)"; exit 1; }
T=$(mktemp -d)
( cd "$T" && nebula-cert ca -name zima-test-ca >/dev/null \
  && nebula-cert sign -name host -networks 10.233.0.1/24 -out-crt host.crt -out-key host.key \
  && nebula-cert sign -name peer -networks 10.233.0.2/24 -out-crt peer.crt -out-key peer.key ) || fail "nebula-cert"
cp "$T/ca.crt" "$T/host.crt" "$T/host.key" "$D/"; cp "$T/ca.crt" "$T/peer.crt" "$T/peer.key" "$P/"
rm -f "$T"/*; rmdir "$T"     # the test CA key is not kept

fw='firewall:
  outbound: [{port: any, proto: any, host: any}]
  inbound: [{port: 7833, proto: tcp, host: any}]'
cat > "$D/config.yml" <<EOF
pki: {ca: $D/ca.crt, cert: $D/host.crt, key: $D/host.key}
lighthouse: {am_lighthouse: true}
listen: {host: 0.0.0.0, port: 4243}
tun: {dev: nebt0}
$fw
EOF
cat > "$P/peer.yml" <<EOF
pki: {ca: $P/ca.crt, cert: $P/peer.crt, key: $P/peer.key}
static_host_map: {"10.233.0.1": ["10.234.0.1:4243"]}
lighthouse: {am_lighthouse: false, hosts: ["10.233.0.1"]}
listen: {host: 0.0.0.0, port: 4244}
tun: {dev: nebp0}
$fw
EOF

ip netns add "$NS"
ip link add vnbt0 type veth peer name vnbt1
ip link set vnbt1 netns "$NS"
ip addr add 10.234.0.1/30 dev vnbt0; ip link set vnbt0 up
ip -n "$NS" addr add 10.234.0.2/30 dev vnbt1; ip -n "$NS" link set vnbt1 up; ip -n "$NS" link set lo up
modprobe tun
# detached, so it outlives the ssh session that runs this test
setsid ip netns exec "$NS" nebula -config "$P/peer.yml" > "$P/peer.log" 2>&1 < /dev/null &
sleep 2
pgrep -f "nebula -config $P/peer.yml" | head -n 1 > "$P/peer.pid"
[ -s "$P/peer.pid" ] && pass "peer node running (pid $(cat "$P/peer.pid"))" || fail "peer node did not start: $(tail -n 3 "$P/peer.log")"

zfw_open "-i vnbt0 -p udp --dport 4243" "-i nebt0 -p tcp --dport 7833"
# Negative control: without the host side the overlay must not answer.
if probe "$NS" 10.233.0.1 7833; then fail "answer before nebula ran on the host"; else pass "no answer before enable"; fi

if nebula-zima enable; then pass "nebula-zima enable"; else fail "nebula-zima enable"; fi
[ "$(systemctl is-active nebula.service)" = active ] && pass "nebula active" || fail "nebula: $(systemctl is-active nebula.service)"
[ "$(systemctl is-enabled nebula-watchdog.timer 2>/dev/null)" = enabled ] && pass "watchdog timer enabled" || fail "watchdog timer not enabled"
ok_tcp=0; for _ in 1 2 3; do probe "$NS" 10.233.0.1 7833 && { ok_tcp=1; break; }; sleep 1; done
[ "$ok_tcp" = 1 ] && pass "TCP through the overlay" || fail "no TCP through the overlay"
if systemctl reload nebula.service; then pass "reload (HUP)"; else fail "reload"; fi
sleep 1; [ "$(systemctl is-active nebula.service)" = active ] && pass "still active after reload" || fail "not active after reload"
echo "nebula host-test: $ok OK, $bad FAIL"
[ "$bad" -eq 0 ]
