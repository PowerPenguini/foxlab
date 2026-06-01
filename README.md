# Foxlab

Foxlab is a declarative local virtual lab builder for libvirt/KVM. Labs are `.lab` files with YAML content, and the same files are used by the CLI and the browser UI.

## Commands

```sh
make start-dev
go run . serve --workspace .
make package-topology package-vnc-viewer
go run . app inspect dist/apps/topology.foxapp
go run . app inspect dist/apps/vnc-viewer.foxapp
go run . app run dist/apps/topology.foxapp --workspace .
go run . app run dist/apps/vnc-viewer.foxapp -- --vnc-host 127.0.0.1 --vnc-port 5900 --vm-label vm1
```

The dev shell defaults to `qemu:///system` because topology apply creates managed libvirt networks/bridges. The current user must already have permission to use the selected libvirt URI; Foxlab does not run privileged sudo commands.

`make start-dev` builds `dist/apps/topology.foxapp` and `dist/apps/vnc-viewer.foxapp`, then starts only the shell API backend on `127.0.0.1:8088` and the shell Vite app on `127.0.0.1:5173`. It does not start topology. The shell discovers the packaged apps and starts topology only when the desktop icon is opened. Use `make dev-topology` to run the packaged topology app directly, or `make dev-all` when both shell and topology should run at once.

`foxlab serve` exposes the desktop shell at `http://127.0.0.1:8088`. The shell starts the topology Fox app on a free localhost port when the Topology Editor desktop icon is opened, then waits for the app to ask the local WM gRPC service to open a window with its host, port, and path.

The shell React/Vite source lives in `web/shell`. The topology app lives under `apps/topology` with its own Go command, manifest, and web app. The VNC viewer app lives under `apps/vnc-viewer` and starts from explicit `--vnc-host`, `--vnc-port`, and `--vm-label` runtime parameters. `make package-topology` and `make package-vnc-viewer` build gzip-compressed tar archives containing `foxapp.json`, the app binary, and `web/dist`.

## Fox Apps

A Fox app is installed by copying a `.foxapp` bundle into an app directory. There is no source-tree registration path for the shell.

The shell scans these directories in order:

- every directory in `FOXLAB_APP_DIRS`
- `$XDG_DATA_HOME/foxlab/apps`, or `~/.local/share/foxlab/apps` when `XDG_DATA_HOME` is unset
- `dist/apps`

The package owns its app id, display name, icon, and window title through `foxapp.json`.

## Lab Model

Topology config is fully declarative: the UI and API edit the complete lab document at `/api/labs/<id>`. There are no VM, switch, or disk mutation endpoints; `apply` is the only path that creates or updates disks, libvirt networks, and VMs.

- `vms` define CPU, memory, disk, optional ISO, VNC, and switch or external-interface attachments.
- `switches` use `bridge`, `nat`, or experimental `macnat-bridge` mode. `bridge` is passthrough/local bridge, `nat` is libvirt NAT with DHCP, and `macnat-bridge` creates a local libvirt bridge that is handed to the FoxLab MAC NAT driver.
- `externalLinks` describe existing host bridge/interface names such as `br0`; Foxlab references them but does not create or reconfigure host networking.
- `disks` are simple file-backed qcow2/raw disks created or imported under the lab workspace.
- `layout` stores UI canvas positions and diagram-only links for VMs, switches, and external links.

Managed libvirt names use the `foxlab-<lab-id>-<resource-id>` convention. Foxlab refuses to replace a conflicting resource unless it carries Foxlab metadata for the same lab.

## Experimental MAC NAT Driver

`mode: macnat-bridge` is implemented by an experimental kernel module at `drivers/macnat`. It exposes `/dev/macnat`, accepts multiple active switch/uplink sessions, rewrites outbound VM Ethernet source MACs to the host uplink MAC, learns VM TAP ports/IPs from ARP/DHCP/IPv4, and forwards matched inbound ARP/DHCP/IPv4 frames back to the learned VM port.

```sh
make macnat-module
sudo insmod build/macnat/foxlab_macnat.ko
```

When a lab uses `macnat-bridge`, `apply` creates the local libvirt switch, starts VMs, collects VM MAC addresses, and sends the session to `/dev/macnat`. If the module is not loaded, `apply` fails before replacing the running lab. Driver status is read back from `/dev/macnat` and includes packet counters for the topology switch detail.
