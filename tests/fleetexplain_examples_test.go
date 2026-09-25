package tests_test

// Normative text examples of GFX §7 for S-WIDTH, copied verbatim from the
// specification. Each "…" stands for a computed value (GFO-084/086 or an S1
// output) and is matched as one or more printable non-space bytes.

const gfxExampleWidthGPUText = `gpu/gpu-node-1-gpu0 phase=Degraded qualification=Disqualified revision=7:2:… reason=Degraded mode=offline
PATH
  target uid=0b5f3c9a-6e2d-4f8b-a1c7-3d9e5f2a7b41 node=gpu-node-1 nodeUID=7c9e6679-7425-40de-944b-e07fc1f90ae7 eligibility=Ineligible assessment=… observed=2026-09-24T00:00:30Z evaluated=2026-09-24T00:00:30Z
  identity state=Bound reason=Ready vendor=NVIDIA uuid=GPU-5f0b1c2d-3e4f-4a5b-8c6d-7e8f9a0b1c2d serial=- nodeUID=7c9e6679-7425-40de-944b-e07fc1f90ae7 bootID=3f1c2a9e-8d4b-4e0a-9b1f-2c6d5e7a8b90 bdf=0000:03:00.0 function=PCIeFunction/pci-bdf:0000:03:00.0 sourceType=agent sourceName=path-agent/nvidia-smi evidence=nb:7:2:… observed=2026-09-24T00:00:30Z expires=2026-09-24T00:05:30Z
  segment PCIeFunction/pci-bdf:0000:03:00.0 LOCATED_IN PCIeSwitch/pci-bdf:0000:02:08.0 origin=Observed kind=Observed sourceType=agent sourceName=path-agent/sysfs-parent evidence=pe:7:2:… observed=2026-09-24T00:00:30Z expires=2026-09-24T00:05:30Z
  segment PCIeSwitch/pci-bdf:0000:02:08.0 LOCATED_IN PCIeSwitch/pci-bdf:0000:01:00.0 origin=Observed kind=Observed sourceType=agent sourceName=path-agent/sysfs-parent evidence=pe:7:2:… observed=2026-09-24T00:00:30Z expires=2026-09-24T00:05:30Z
  segment PCIeSwitch/pci-bdf:0000:01:00.0 LOCATED_IN PCIeRootPort/pci-bdf:0000:00:01.0 origin=Observed kind=Observed sourceType=agent sourceName=path-agent/sysfs-parent evidence=pe:7:2:… observed=2026-09-24T00:00:30Z expires=2026-09-24T00:05:30Z
  segment PCIeRootPort/pci-bdf:0000:00:01.0 LOCATED_IN KubernetesNode/kubernetes-node-uid:7c9e6679-7425-40de-944b-e07fc1f90ae7 origin=Observed kind=Observed sourceType=agent sourceName=path-agent/sysfs-parent evidence=pe:7:2:… observed=2026-09-24T00:00:30Z expires=2026-09-24T00:05:30Z
FINDINGS
  finding pcie-width-v1:5043496546756e6374696f6e2f7063692d6264663a303030303a30333a30302e30:50434965526f6f74506f72742f7063692d6264663a303030303a30303a30312e30:504349655377697463682f7063692d6264663a303030303a30323a30382e30 type=PCIE_LINK_WIDTH_DEGRADED severity=Warning state=Active confidence=Medium firstSeen=2026-09-24T00:00:00Z lastSeen=2026-09-24T00:00:30Z
    scope PCIeFunction/pci-bdf:0000:03:00.0
    scope PCIeRootPort/pci-bdf:0000:00:01.0
    scope PCIeSwitch/pci-bdf:0000:02:08.0
    evidence ob:7:0:… pcie.link.width.current=8 lanes
    evidence ob:7:0:… pcie.link.width.expected=16 lanes
    evidence ob:7:1:… pcie.link.width.current=8 lanes
    evidence ob:7:1:… pcie.link.width.expected=16 lanes
    evidence ob:7:2:… pcie.link.width.current=8 lanes
    evidence ob:7:2:… pcie.link.width.expected=16 lanes
    explanation PCIe link width degraded: subject="PCIeFunction/pci-bdf:0000:03:00.0"; peer="PCIeSwitch/pci-bdf:0000:02:08.0"; root="PCIeRootPort/pci-bdf:0000:00:01.0"; current=8 lanes; expected=16 lanes; samples=3; first=2026-09-24T00:00:00Z; last=2026-09-24T00:00:30Z; source_type="agent"; source_name="path-agent/sysfs-width"; provenance="operator_verified_wiring"; basis=adapter-supplied operator-verified wiring width; limitation=operator verification is asserted by the adapter and not revalidated by this rule.
    suggestedStep Recheck the verified wiring baseline and inspect both link endpoints in audit mode.
COVERAGE
  coverage nic-lldp pathKind=nic-lldp-remote required=false state=Unsupported reason=Unsupported observed=- latest=- expires=-
  coverage pcie-parent pathKind=gpu-pcie-parent required=true state=Normal reason=Normal observed=… latest=… expires=…
    evidence …
  coverage pcie-root pathKind=gpu-pcie-root required=true state=Normal reason=Normal observed=… latest=… expires=…
    evidence …
    evidence …
    evidence …
  coverage pcie-width pathKind=gpu-pcie-link-width-normal required=true state=Missing reason=CoverageMissing observed=… latest=… expires=…
    evidence …
    evidence …
    evidence …
    evidence …
    evidence …
ALLOCATION
  allocation state=Unknown reason=AllocationUnknown profile=- observed=- expires=-
  workload (none)
LIMITATIONS
  allocation_unavailable
  offline
  offline_trust_not_live offline:lab-a-width-replay
  traffic_path_unverified
`

const gfxExampleWidthNodeText = `node/gpu-node-1 eligibility=Ineligible qualification=Disqualified revision=7:2:… eligibilityReason=Degraded mode=offline
PATH
  target uid=7c9e6679-7425-40de-944b-e07fc1f90ae7 assessment=… observed=2026-09-24T00:00:30Z evaluated=2026-09-24T00:00:30Z
  device gpu-node-1-gpu0 uid=0b5f3c9a-6e2d-4f8b-a1c7-3d9e5f2a7b41 desired=InService generation=1 phase=Degraded qualification=Disqualified reason=Degraded
  segment PCIeFunction/pci-bdf:0000:03:00.0 LOCATED_IN PCIeSwitch/pci-bdf:0000:02:08.0 origin=Observed kind=Observed sourceType=agent sourceName=path-agent/sysfs-parent evidence=pe:7:2:… observed=2026-09-24T00:00:30Z expires=2026-09-24T00:05:30Z
  segment PCIeSwitch/pci-bdf:0000:02:08.0 LOCATED_IN PCIeSwitch/pci-bdf:0000:01:00.0 origin=Observed kind=Observed sourceType=agent sourceName=path-agent/sysfs-parent evidence=pe:7:2:… observed=2026-09-24T00:00:30Z expires=2026-09-24T00:05:30Z
  segment PCIeSwitch/pci-bdf:0000:01:00.0 LOCATED_IN PCIeRootPort/pci-bdf:0000:00:01.0 origin=Observed kind=Observed sourceType=agent sourceName=path-agent/sysfs-parent evidence=pe:7:2:… observed=2026-09-24T00:00:30Z expires=2026-09-24T00:05:30Z
  segment PCIeRootPort/pci-bdf:0000:00:01.0 LOCATED_IN KubernetesNode/kubernetes-node-uid:7c9e6679-7425-40de-944b-e07fc1f90ae7 origin=Observed kind=Observed sourceType=agent sourceName=path-agent/sysfs-parent evidence=pe:7:2:… observed=2026-09-24T00:00:30Z expires=2026-09-24T00:05:30Z
FINDINGS
  finding pcie-width-v1:5043496546756e6374696f6e2f7063692d6264663a303030303a30333a30302e30:50434965526f6f74506f72742f7063692d6264663a303030303a30303a30312e30:504349655377697463682f7063692d6264663a303030303a30323a30382e30 type=PCIE_LINK_WIDTH_DEGRADED severity=Warning state=Active confidence=Medium firstSeen=2026-09-24T00:00:00Z lastSeen=2026-09-24T00:00:30Z
    scope PCIeFunction/pci-bdf:0000:03:00.0
    scope PCIeRootPort/pci-bdf:0000:00:01.0
    scope PCIeSwitch/pci-bdf:0000:02:08.0
    evidence ob:7:0:… pcie.link.width.current=8 lanes
    evidence ob:7:0:… pcie.link.width.expected=16 lanes
    evidence ob:7:1:… pcie.link.width.current=8 lanes
    evidence ob:7:1:… pcie.link.width.expected=16 lanes
    evidence ob:7:2:… pcie.link.width.current=8 lanes
    evidence ob:7:2:… pcie.link.width.expected=16 lanes
    explanation PCIe link width degraded: subject="PCIeFunction/pci-bdf:0000:03:00.0"; peer="PCIeSwitch/pci-bdf:0000:02:08.0"; root="PCIeRootPort/pci-bdf:0000:00:01.0"; current=8 lanes; expected=16 lanes; samples=3; first=2026-09-24T00:00:00Z; last=2026-09-24T00:00:30Z; source_type="agent"; source_name="path-agent/sysfs-width"; provenance="operator_verified_wiring"; basis=adapter-supplied operator-verified wiring width; limitation=operator verification is asserted by the adapter and not revalidated by this rule.
    suggestedStep Recheck the verified wiring baseline and inspect both link endpoints in audit mode.
COVERAGE
  coverage pcie-width device=gpu-node-1-gpu0 pathKind=gpu-pcie-link-width-normal required=true state=Missing reason=CoverageMissing observed=… latest=… expires=…
    evidence …
    evidence …
    evidence …
    evidence …
    evidence …
ALLOCATION
  workload (none)
LIMITATIONS
  allocation_unavailable
  offline
  offline_trust_not_live offline:lab-a-width-replay
  traffic_path_unverified
`
