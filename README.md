# Nebula for ZimaOS

**Version 1.1.0** · ZimaOS 1.8.0 (amd64) · sysext module `nebula`

[Nebula](https://github.com/slackhq/nebula), the overlay network by Slack, as a native ZimaOS module — no container, the node runs on the host with its own TUN interface.

- `nebula` and `nebula-cert` **v1.11.2**, the static binaries from the upstream release, checked against a pinned sha256;
- `nebula.service` — the upstream example unit, reading `/DATA/AppData/nebula/config.yml`;
- `nebula-zima` — checks the config and starts Nebula now and at every boot;
- the **Nebula** tile on the dashboard (`nebula-web`).

## The tile

**New network:** creates the network's CA on this host — valid 10 years; `nebula-cert`'s default is one year, after which every certificate of the network ends — gives this host the first address and makes it the lighthouse. **Add a device:** signs a certificate (next free address or a chosen one) and hands out one `config.yml` with the CA, certificate and key inline, pointing at this lighthouse — the device's key is not kept on the host. **Join:** paste `ca.crt`, this host's certificate and key from another network's CA owner. Turn on/off (on = also at every boot); **Reset** moves every file, CA key included, to `/DATA/AppData/nebula/.reset/`. With ZFW installed the tile shows whether the listen port is open there and opens it with one click.

Measured on ZimaOS 1.8.0 (ZimaBoard 2), 2026-10-23: set up in Chrome as a lighthouse (certificates valid until 2036), turned on (`nebula1` up); a device config signed by the tile, started in a network namespace, reached the host over the overlay (TCP answer, handshake in the device log).

**ZFW and the Nebula interface:** ZFW 1.0.29 trusts `wg+`, `tun0`, `zt+` and `tailscale0`, not `nebula1`; traffic to services on this host over the overlay is dropped until ZFW includes `nebula+` (prepared on ZFW's branch `nebula-bypass`) or a rule allows it.

## Use

```sh
sudo mkdir -p /DATA/AppData/nebula && cd /DATA/AppData/nebula
# certificates: on the machine that keeps your CA key (not necessarily this one)
#   nebula-cert ca -name "My network"
#   nebula-cert sign -name zima -networks 10.42.0.10/24
# then put ca.crt, zima.crt, zima.key and config.yml here (example config:
# https://github.com/slackhq/nebula/blob/v1.11.2/examples/config.yml), with the pki paths pointing to this folder
sudo nebula-zima enable      # nebula -test on the config, start now and at every boot
nebula-zima status
sudo systemctl reload nebula # re-read the config (SIGHUP)
sudo nebula-zima disable     # stop, no longer at boot
```

### Why `enable` and not `systemctl enable`

A unit inside a sysext is not started at boot on ZimaOS: `multi-user.target` is resolved before the extension is merged into `/usr`. `nebula-zima enable` writes a small timer to `/etc/systemd/system` (persistent) that starts `nebula.service` 15 seconds after boot if it is not running — lintux-modkit's `sysext/unit-watchdog.sh`. Run `sudo nebula-zima disable` before removing the module.

### Firewall (ZFW)

ZFW drops incoming traffic it does not know, and it knows neither Nebula's UDP port (`listen.port`, 4242 in the example config) nor its interface (`tun.dev`, `nebula1` in the example config). On a host with ZFW, open the UDP port for a lighthouse or a node others connect to, and allow the services you want to reach over the overlay on the Nebula interface. Nebula's own `firewall:` section in the config applies in addition.

## Install (on the ZimaOS host)

```sh
cd /tmp
curl -fsSLO https://github.com/chicohaager/zimaos-nebula-sysext/releases/latest/download/nebula-amd64.raw
curl -fsSLO https://github.com/chicohaager/zimaos-nebula-sysext/releases/latest/download/nebula-amd64.raw.sha256
sha256sum -c nebula-amd64.raw.sha256
mv nebula-amd64.raw nebula.raw              # zpkg takes the module name from the file name
sudo zpkg install /tmp/nebula.raw
sudo systemctl daemon-reload
```

## Build and test

```sh
sh build.sh amd64              # downloads the release tarball and checks its pinned sha256; needs Go, node, ../lintux-modkit; the backend tests run the downloaded nebula-cert
sudo sh tools/host-test.sh     # on the host after installing; `cleanup` removes what it created
```

The host test makes the host a lighthouse and runs a second node in its own network namespace behind a veth pair; a TCP connection over the overlay must fail before `enable` and get through after it. Measured on ZimaOS 1.8.0 (ZimaBoard 2) with ZFW active, 2026-10-23: 8 OK, 0 FAIL. The test opens the paths it needs in ZFW and removes exactly those again (`cleanup`). The test CA key is deleted right after signing.

MIT License for this repository, see [LICENSE](LICENSE). Nebula is MIT licensed too, its license is in the image (`/usr/share/doc/nebula/LICENSE`).
