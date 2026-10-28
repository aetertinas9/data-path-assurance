# data-path-assurance

`pathctl` shows the physical data path of every GPU on a host and checks that
the path can run at full speed. One command, one screen per GPU:

```text
GPU 0000:1b:00.0  NVIDIA RTX PRO 6000 Blackwell Server Edition  [GPU0 GPU-1111...]
  link   Gen5 x16 63GB/s
  path   0000:1b:00.0 gpu  Gen5 x16 63GB/s
         0000:1a:04.0 downstream  Gen5 x16 63GB/s  ACS REDIRECT ReqRedir+CmplRedir
         0000:19:00.0 upstream  Gen5 x16 63GB/s  ACS none
         0000:16:01.0 root  NUMA 0  cpus 0-47,96-143
  nic    0000:1c:00.0 ConnectX-7  mlx5_0/1 400Gb/s ACTIVE  PIX  cap 50GB/s (port mlx5_0/1)  topo PIX
  dpu    0000:9c:00.0 BlueField-3  mlx5_2/1 400Gb/s ACTIVE  SYS  cap 50GB/s (port mlx5_2/1)  topo SYS
  peer   0000:1d:00.0 RTX PRO 6000 Blackwell Server Edition  PIX  p2p BLOCKED by ACS on 0000:1a:04.0, 0000:1a:05.0
  peer   0000:9b:00.0 RTX PRO 6000 Blackwell Server Edition  SYS
  checks
    gpu-link        PASS  Gen5 x16
    upstream-links  PASS  all at expected width and speed
    nic-locality    PASS  0000:1c:00.0 ConnectX-7 via PIX (target PXB or better)
    nic-port        PASS  mlx5_0/1 400 Gb/s ACTIVE
    numa-aligned    PASS  GPU and mlx5_0/1 both on NUMA 0
    p2p-acs         FAIL  ACS P2P redirect enabled on 0000:1a:04.0, 0000:1a:05.0
    nvlink          N/A   no NVLink on this GPU
    pcie-errors     PASS  no correctable errors counted
    pcie-payload    PASS  MaxPayload 256B on both; MaxReadReq GPU 512B, NIC 512B
```

It is built for Blackwell hosts (HGX B300 with on-board ConnectX-8, RTX PRO
6000 Blackwell servers with ConnectX-7) and BlueField DPUs, and it reads what
the kernel and the NVIDIA driver already expose: sysfs and `nvidia-smi`. It
is a single static Go binary with no dependencies outside the standard
library, it never writes to the host, and every value it cannot read is shown
as unknown rather than guessed.

## Usage

```sh
make build                              # bin/pathctl, CGO_ENABLED=0
sudo bin/pathctl path                   # every GPU on this host
sudo bin/pathctl path --gpu 0           # one GPU, by nvidia-smi index, PCI address or UUID
sudo bin/pathctl path --output json     # the same content for scripts
sudo bin/pathctl path --output mermaid  # a diagram to paste into a README or an issue
sudo bin/pathctl check                  # checks only; exit 3 when any check fails
sudo bin/pathctl capture --out hosts/gpu-node-1   # copy the read set into a fixture
bin/pathctl path --fixture hosts/gpu-node-1      # read the fixture anywhere, later
```

`sudo` is needed for one thing only: the PCI configuration space, which
carries ACS and the payload sizes. Without it those show as unknown and the
rest is still read. `nvidia-smi` is found on `PATH` or given with
`--nvidia-smi`; without it the GPUs are still listed from sysfs, with the UUID,
name, NVLink state and topology cross-check missing.

Two synthetic hosts are checked in under `testdata/fixtures/` and can be
viewed without hardware:

```sh
bin/pathctl path --fixture testdata/fixtures/two-socket-rtx    # 3x RTX PRO 6000, ConnectX-7, BlueField-3
bin/pathctl path --fixture testdata/fixtures/nvlink-pair       # 2x NVLink GPUs and a ConnectX-8 behind a Gen6 switch
```

## What is shown

For every GPU:

- **link**: negotiated width and speed, the bandwidth that gives, and whether it
  is below what both ends of the link support (`DEGRADED`) or below what the
  GPU alone supports (`device max`, a platform limit rather than a fault).
- **path**: the chain of PCIe ports from the GPU to its root port, each with its
  link state and, for switch ports, its ACS setting; the root port with the
  NUMA node and its CPU list.
- **nic / dpu**: every NIC and DPU on the host, with its RDMA or network port,
  rate and state; the locality to this GPU in `nvidia-smi topo` vocabulary
  (`PIX` same switch, `PXB` several switches, `PHB` through the host bridge,
  `NODE` another host bridge on the same NUMA node, `SYS` across NUMA nodes);
  the capacity of the path, which is the slowest hop between the GPU and the
  port, and which hop that is. When `nvidia-smi topo -m` is available its
  verdict is shown next to the one derived from sysfs, and a disagreement is
  listed under limitations.
- **peer**: every other GPU with its locality, the number of NVLinks between
  them when there are any, and for PCIe peers whether ACS on the switch path
  redirects peer-to-peer traffic through the root complex.
- **checks**: the table below.

| check | PASS | FAIL / WARN |
|---|---|---|
| `gpu-link` | negotiated = min(GPU max, port max) | FAIL below that; WARN when the GPU could do more than its port |
| `upstream-links` | every switch link above the GPU at its expected width and speed | FAIL on any degraded link |
| `nic-locality` | best NIC within `--locality` (default `PXB`) | FAIL when farther |
| `nic-port` | best port ACTIVE/up | FAIL when down; WARN below the device's maximum rate |
| `numa-aligned` | GPU and best NIC on the same NUMA node | WARN otherwise |
| `p2p-acs` | no ACS P2P redirect on the switch path to a PCIe peer | FAIL when redirect is on; UNKNOWN without root |
| `nvlink` | all links active | FAIL when some are down; N/A without NVLink |
| `pcie-errors` | no correctable errors counted on the GPU link | WARN when any |
| `pcie-payload` | same MaxPayload on GPU and NIC | WARN when they differ |

`check` exits 3 on any FAIL and 0 otherwise. UNKNOWN never fails a host; its
reason is in the detail column.

## How it reads

- sysfs: `bus/pci/devices` links resolved lexically inside the root, then per
  function `vendor`, `device`, `class`, `revision`, `subsystem_*`,
  `current_link_*`, `max_link_*`, `numa_node`, `aer_dev_*`, `config`
  (4 KiB), the `driver` and `physfn` links; `class/infiniband/*/ports/*`
  (`rate`, `state`, `phys_state`, `link_layer`); `class/net/*` (`speed`,
  `operstate`, `carrier`); `devices/system/node/*/cpulist`. Every read is
  bounded and opened read-only. `capture` copies exactly this set.
- nvidia-smi: `--query-gpu=index,name,uuid,pci.bus_id,pcie.link.gen.current,
  pcie.link.gen.max,pcie.link.width.current,pcie.link.width.max`, `nvlink -s`,
  `topo -m`, each with a 5 s timeout and a 1 MiB output limit, no shell.
- Device names come from `nvidia-smi` for GPUs and from a small table of PCI
  IDs (`internal/datapath/devices.go`) for NICs, DPUs and GPUs without a
  driver. Unknown IDs are shown as `vendor:device` and keep their role from
  the class code. `--devices ids.json` adds IDs seen on a real host:

  ```json
  [{"vendor":"0x15b3","device":"0x1023","name":"ConnectX-8","role":"nic","port_gbps":800}]
  ```

## Layout

| path | what |
|---|---|
| `cmd/pathctl` | the command: `path`, `check`, `capture` |
| `internal/sysfs` | read-only bounded sysfs reader and `Capture` |
| `internal/nvidia` | bounded `nvidia-smi` runner and the three parsers |
| `internal/datapath` | device table, path model, locality, capacity, checks |
| `internal/render` | text, JSON and Mermaid output |
| `testdata/fixtures`, `testdata/golden` | synthetic hosts and their expected output |

`make all` builds, vets, checks that nothing outside the standard library is
imported and runs the tests. `make update-golden` rewrites the expected
output after a deliberate change.

## Status

The reader, parsers, checks and renderers are complete and tested against
synthetic hosts that mirror how the kernel lays out sysfs. They have not yet
run on a real B300, RTX PRO 6000 or BlueField-3 host. The first thing to do
on such a host is `pathctl capture`, and to commit the result under
`testdata/fixtures/` so that the real layout becomes the regression test;
PCI IDs that the table lacks are then added from `lspci -nn`.

The earlier control-plane work (Kubernetes resources, controller, mTLS
ingest, fleet lifecycle) is kept at the tag `legacy/v0-control-plane` and is
not part of this tree. See `docs/plan/data-path-v2.md` for the reasoning.

## License

[Apache License 2.0](LICENSE).
