# Getting started

## Build and test

```sh
make all          # build, vet, arch-check (standard library only), test
make build        # bin/pathctl only
```

Go 1.26 or newer; no other dependency.

## Run on a host

```sh
sudo bin/pathctl path                 # all GPUs
sudo bin/pathctl path --gpu 0000:1b:00.0
sudo bin/pathctl check; echo $?       # 0 when no check fails, 3 otherwise
```

Root is only needed for the PCI configuration space (ACS and payload sizes);
run without it and those two checks say UNKNOWN with the reason. `nvidia-smi`
is looked up on `PATH`; pass `--nvidia-smi /usr/bin/nvidia-smi` when the
`sudo` environment hides it.

`--locality` sets the worst acceptable GPU-to-NIC locality for the
`nic-locality` check (`PIX`, `PXB`, `PHB`, `NODE`, `SYS`; default `PXB`).

## Capture a host as a fixture

```sh
sudo bin/pathctl capture --out hosts/gpu-node-1
bin/pathctl path --fixture hosts/gpu-node-1
```

The capture holds `sys/` with exactly the files the reader uses and `nvidia/`
with the three `nvidia-smi` outputs. It contains PCI IDs, GPU UUIDs and port
rates and nothing else about the host. Commit captures of real B300, RTX PRO
6000 and BlueField hosts under `testdata/fixtures/`: they are the regression
tests that matter.

## On a DPU

A BlueField DPU seen from the host is a NIC function with a locality to each
GPU, like any other NIC. The path inside the DPU (its own PCIe to its Arm
cores and its ports) is read by running `pathctl` on the DPU itself, which is
an arm64 Linux host with sysfs; build with `GOARCH=arm64`.

## Add PCI IDs

Unknown IDs appear as `vendor:device` with a role from the class code. Add
the ones `lspci -nn` shows on a real host either to
`internal/datapath/devices.go` or to a JSON file passed with `--devices`:

```json
[
  {"vendor":"0x10de","device":"0x2bb5","name":"NVIDIA RTX PRO 6000 Blackwell Server Edition","role":"gpu"},
  {"vendor":"0x15b3","device":"0x1023","name":"ConnectX-8","role":"nic","port_gbps":800}
]
```

`port_gbps` is the device's maximum port rate; the `nic-port` check warns
when a port runs below it.

## Change the expected output

The synthetic hosts live in `internal/datapath/fixture_test.go`. After a
deliberate change to the output, run `make update-golden`, review the diff
under `testdata/`, and commit it with the change.
