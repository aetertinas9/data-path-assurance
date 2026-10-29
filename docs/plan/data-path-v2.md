# Data Path v2 계획: B300 / RTX PRO 6000 Blackwell / DPU

작성일: 2026-10-28. 대상 브랜치: `claude/kind-wright-tq8pfb`.

목표는 하나다. **GPU 한 장을 집으면 데이터가 실제로 흐르는 물리 경로가 한눈에
보이고, 그 경로가 최대 성능을 낼 수 있는 상태인지 PASS/FAIL/UNKNOWN 으로
판정되는 것.** 라이프사이클, 스케줄링 게이트, 컨트롤 플레인은 이 목표에
필요하지 않으므로 축소한다.

---

## 1. 현재 구현 이력 진단

### 1.1 커밋 이력 요약

| 시기 | 작업 | 평가 |
|---|---|---|
| 2026-08 | `pkg/model`, `identity`, `evidence`, `graph` | 순수 도메인 코어. 작고 견고. **유지** |
| 2026-09 초 | `domains/pcie` 링크 폭 저하 규칙, arch-check | 유일한 "데이터 경로" 규칙. **유지·확장** |
| 2026-09 중 | `fleet` 라이프사이클/자격/게이트 (30여 커밋) | 데이터 경로와 무관한 상태 머신. **동결 후 제거 후보** |
| 2026-09-24 | `nativepcie` C 라이브러리 + cgo 브리지 | 속도/NUMA/AER 파싱은 있으나 에이전트에 연결 안 됨. **Go 로 흡수 후 제거** |
| 2026-09-25 | `agent` 오프라인 sysfs 수집기 | 데이터 경로의 씨앗. **유지·확장** |
| 2026-09-26 | `pathctl explain` + `offline` 리더 | 출력 틀은 쓸 만하나 `fleet` 에 결합. **`pathctl path` 로 대체** |
| 2026-09-30 | k8s CRD 3종 + 상태 컨트롤러 | 2.9만 줄. 데이터 경로에 불필요. **동결 후 제거 후보** |
| 2026-10-03 | mTLS gRPC 라이브 인제스트 | 3.1만 줄. 동일. **동결 후 제거 후보** |

### 1.2 코드 분포 (소스 / 테스트, 줄 수)

| 영역 | 소스 | 테스트 | 데이터 경로 필요 여부 |
|---|---|---|---|
| `pkg/model`, `identity`, `evidence`, `graph` | 4,336 | (tests/*.go 일부) | 필수 |
| `domains/pcie` | 323 | 일부 | 필수 |
| `agent` (sysfs, nvidia-smi) | 4,172 | 일부 | 필수 |
| `offline`, `app`, `cli` | 8,192 | 일부 | 부분 (fleet 의존 제거 필요) |
| `fleet` | 2,835 | 다수 | 불필요 |
| `nativepcie` (+C) | 1,236 | 2,560 | 불필요 (Go 로 흡수) |
| `kubernetes`, `ingest`, `transport`, `path-controller` | 9,554 | 37,571 | 불필요 |
| 빈 스텁 (`telemetry`, `policy`, `impact`, `correlation`, `sources/*`, `domains/fabric`, `testdata/*`, `profiles/*`, `deploy/helm`) | 0 | 0 | 삭제 |

### 1.3 지금 "데이터 경로"로 보이는 것

- GPU/NIC 기능 → PCIe 브리지들 → 루트 포트 → 노드의 `LOCATED_IN` 체인.
- 링크 폭 current/expected 관측과 지속 저하 판정 한 가지.

보이지 않는 것: 링크 속도(Gen), NUMA/CPU 소속, 스위치 포트의 ACS, NVLink,
NIC/RDMA 포트 상태와 속도, DPU 식별, GPU↔NIC 근접성(PIX/PXB/NODE/SYS),
경로 용량(병목). 그리고 **실제 하드웨어 픽스처가 하나도 없다.**

---

## 2. 목표 산출물: 데이터 경로 뷰

`pathctl path gpu <bdf|uuid>` 한 번으로 아래가 나와야 한다. (예시는 RTX PRO
6000 호스트. 값은 설명용.)

```text
GPU  0000:1b:00.0  NVIDIA RTX PRO 6000 Blackwell SE   Gen5 x16 / max Gen5 x16   OK
 └ dsp 0000:1a:04.0  PEX switch downstream   Gen5 x16   ACS SrcValid+ReqRedir   P2P-BLOCKED
   └ usp 0000:19:00.0  PEX switch upstream     Gen5 x16
     └ rp  0000:16:01.0  root port              Gen5 x16   NUMA 0   cpus 0-47,96-143

NIC  0000:1c:00.0  ConnectX-7  Gen5 x16  mlx5_0/port1  400 Gb/s  ACTIVE   locality PIX   OK
DPU  0000:3b:00.0  BlueField-3 Gen5 x16  mlx5_2/port1  400 Gb/s  ACTIVE   locality SYS   WARN

PEERS
 GPU 0000:1d:00.0  PIX  via PCIe P2P   ACS on 0000:1a:04.0, 0000:1a:05.0   P2P-BLOCKED
 GPU 0000:9b:00.0  SYS  cross-NUMA

CAPACITY  GPU→NIC(mlx5_0): limited by nic port 400 Gb/s (50 GB/s); pcie hops 64 GB/s
CHECKS    pcie.gen=5/5 OK  pcie.lanes=16/16 OK  gpu-nic.locality=PIX(>=PXB) OK
          p2p.acs=off FAIL (0000:1a:04.0)  numa.aligned OK  nvlink n/a
```

B300(HGX) 호스트에서는 `PEERS` 가 NVLink 링크 수/상태로 채워지고, NIC 가
온보드 ConnectX-8(800 Gb/s, Gen6) 이 된다. DPU 는 호스트에서 보면 PCIe 기능
몇 개(vendor `0x15b3`)이고, DPU 내부 경로는 **DPU Arm 에서 같은 에이전트를
또 한 번 돌려** 별도 노드로 본다.

같은 내용을 `--output json`, `--output mermaid` 로도 낸다. mermaid 는 README
와 이슈에 그대로 붙여 넣는 용도다.

---

## 3. 축소 (Cut)

### Tier A: 즉시 삭제 (1 커밋)

`internal/telemetry`, `internal/policy`, `internal/impact`,
`internal/correlation`, `internal/sources/{prometheus,gnmi}`,
`internal/domains/fabric`, `testdata/{prometheus,gnmi,pcap}`,
`profiles/{metrics,policies}`, `deploy/helm`. Makefile `DOMAIN_CORE` 에서 해당
이름 제거.

### Tier B: 동결 후 제거 (권장, 결정 필요)

`internal/kubernetes`, `internal/ingest`, `internal/transport`,
`cmd/path-controller`, `internal/app/liveingest`, `internal/app/framecore`,
`internal/fleet`, `api/proto`, `deploy/crds`, `tests/kubeapi`,
`tests/liveingest`.

- 근거: 데이터 경로 기능이 전혀 의존하지 않고, 코드·테스트의 약 55% 를
  차지하며, 하드웨어 검증이 한 번도 없었고, envtest 다운로드가 필요해
  `make test` 를 무겁게 한다.
- 방법: `git tag legacy/control-plane-v0 db7900f` 로 이력을 박제한 뒤 트리에서
  제거. go.mod 에서 k8s, grpc, protobuf 의존성이 모두 빠지고 `CGO_ENABLED=0 go
  build ./...` 가 표준 라이브러리만으로 끝난다.
- 최소안(제거가 부담스러우면): 트리에 두되 `make test` 기본 대상에서 빼고
  README 에 "동결" 표기. 단, `fleet` 은 `app/offline` 이 물고 있어 Phase 2 에서
  어차피 분리해야 한다.

### Tier C: 유지

`pkg/model`, `internal/identity`, `internal/evidence`, `internal/graph`,
`internal/domains/pcie`, `internal/agent`, `internal/offline`(스냅샷 리더만),
`internal/cli`, `cmd/path-agent`, `cmd/pathctl`, arch-check, 결정성·순수성
원칙 전부.

`internal/nativepcie` 는 Phase 1 에서 Go 수집기가 같은 sysfs 속성을 읽게 되면
제거한다(C 테스트 하네스, Makefile native 규칙, cgo 스텁 포함).

---

## 4. 모델 확장 (`pkg/model`)

작게 간다. 새 종류(kind)는 하나, 나머지는 관측(signal)과 차원(dimension)으로
표현한다.

| 항목 | 추가 | 비고 |
|---|---|---|
| AssetKind | `NUMANode` | 루트 포트가 `LOCATED_IN` NUMANode, NUMANode 가 `LOCATED_IN` KubernetesNode |
| 식별 네임스페이스 | `nvidia-gpu-uuid`, `rdma-device`, `netdev` | GPU 기능의 별칭으로 붙인다 |
| EdgeRelation | 기존 `CONNECTED_TO` 재사용 | NVLink 피어는 GPU↔GPU `CONNECTED_TO` + 차원 `link=nvlink`, `links_active`, `links_total` |
| FindingType | `PCIeLinkSpeedDegraded`, `PCIeLinkBelowProfile`, `GPUPeerP2PBlocked`, `GPUNICLocalityBelowTarget`, `NVLinkLinksDown`, `NICPortDegraded`, `SourceDisagreement` | 각 finding 은 기존 규칙대로 증거 링크 필수 |

신호(signal) 목록. 단위는 모두 정수.

| 신호 | 주체 | 단위 | 출처 |
|---|---|---|---|
| `pcie.link.width.current` / `.expected` (기존) / `.max` | PCIeFunction, 브리지 | lanes | sysfs |
| `pcie.link.speed.current` / `.max` | 〃 | GT/s ×10 (예: 320 = Gen5) | sysfs |
| `pcie.numa_node` | 〃 | 번호 | sysfs (읽기 전용 open) |
| `pcie.acs.ctrl` | 브리지(스위치 다운스트림 포트) | 비트마스크 | sysfs `config` 확장 영역 (root 필요) |
| `pcie.devctl.max_payload` / `.max_read_request` | PCIeFunction | bytes | sysfs `config` |
| `pcie.aer.correctable.total` 등 | PCIeFunction | count | sysfs `aer_dev_*` (evidence.Rate 로 리셋 안전 비율) |
| `nvlink.links.active` / `.total` | GPU 기능 | count | nvidia-smi |
| `rdma.port.rate` / `rdma.port.state` | NICPort | Gb/s, enum | `/sys/class/infiniband` |
| `net.speed` / `net.operstate` | NICPort | Mb/s, enum | `/sys/class/net` |

**장치 프로파일** `profiles/devices.json` (vendor:device → 모델명, 역할
gpu/nic/dpu/switch, 기대 Gen, 기대 lanes, nvlink 여부, 포트 최대 속도).
항목은 B300, RTX PRO 6000 Blackwell(Server/Workstation), ConnectX-7,
ConnectX-8, BlueField-3(NIC PF 와 관리 기능), Broadcom PEX 스위치. **PCI
device ID 는 기억에서 적지 않고 Phase 1 캡처의 `lspci -nn` 에서 가져온다.**
모르는 ID 는 `UNKNOWN_DEVICE` 로 보이되 경로 자체는 그린다.

---

## 5. 수집기 확장 (`internal/agent`)

원칙 유지: 주입된 `*os.Root`, 읽기 전용, 크기 상한, 심볼릭 링크는 루트
안에서만, 오프라인 모드는 벽시계·환경·프로세스 없음.

### 5.1 읽기 집합 추가

- 모든 PCI 기능과 브리지: `device`, `subsystem_vendor`, `subsystem_device`,
  `revision`, `max_link_width`, `current_link_speed`, `max_link_speed`,
  `numa_node`, `aer_dev_correctable|nonfatal|fatal`, `config`(최대 4 KiB).
  `numa_node` 는 쓰기 가능 속성이라 지금은 열지 않는데, O_RDONLY 로만 연다는
  규칙으로 완화한다. `config` 가 64 바이트까지만 읽히면(비 root) ACS/DevCtl
  은 `unknown` 으로 두고 프레임은 PARTIAL 로 만들지 않는다.
- RDMA/넷: `/sys/class/infiniband/<dev>/device` 링크로 BDF 매핑,
  `ports/<n>/{rate,state,phys_state,link_layer}`; `/sys/class/net/<if>/device`,
  `speed`, `operstate`, `carrier`.
- NUMA: `/sys/devices/system/node/node<N>/cpulist`.
- NVIDIA(바운디드 exec 유지, CGO 없음): 기존 `--query-gpu=uuid,pci.bus_id` 에
  `name,pcie.link.gen.current,pcie.link.gen.max,pcie.link.width.current` 를
  추가하고, `nvidia-smi topo -m` 과 `nvidia-smi nvlink -s` 를 각각 고정
  인자·상한으로 실행한다. 픽스처에서는 `frames/<i>/nvidia-topo.txt`,
  `nvidia-nvlink.txt` 캔 파일.

### 5.2 캡처 모드 (가장 먼저 실제 장비에서 돌릴 것)

`path-agent capture --sysfs-root /sys --out fixture/` 는 **수집기의 읽기
집합과 정확히 같은 파일만** 픽스처 디렉터리로 복사하고 `manifest.json` 을
생성한다. 이것이 B300, RTX PRO 6000, BlueField-3 호스트를 재현 가능한
회귀 테스트로 바꾸는 유일한 경로다. 산출 픽스처는 UUID/시리얼만 마스킹해
`testdata/fixtures/{hgx-b300,rtx-pro-6000,bf3-host,bf3-dpu}/` 에 커밋한다.

### 5.3 픽스처 스키마

`dpa.offline-fixture/v1` → `v2`. 새 파일은 전부 선택 사항이라 v1 픽스처도
그대로 읽히며, 없는 속성은 `unknown` 으로 간다.

---

## 6. 평가 규칙 (`internal/domains`)

전부 순수 함수, 입력은 `graph.Snapshot` + `evidence.Window` + 프로파일, 출력은
finding 과 뷰 값. 기존 `pcie` 폭 규칙의 "3 샘플·30 초 지속" 패턴을 따른다.

| 패키지 | 규칙 | 대상 |
|---|---|---|
| `pcie` | 속도 저하 (current gen < max gen 지속), 프로파일 미달 (예: RTX PRO 6000 이 Gen4 로 협상), AER correctable 비율 상한 | 셋 모두 |
| `locality` (신규) | `LOCATED_IN` 체인에서 GPU↔X 쌍을 PIX / PXB / PHB / NODE / SYS 로 분류 (nvidia-smi topo 범례와 동일 의미), GPU 별 최적 NIC/DPU 선택, 목표 미달 finding, nvidia-smi topo 와 불일치 시 `SourceDisagreement` | 셋 모두 |
| `p2p` (신규) | 같은 스위치 아래 GPU 쌍의 경로상 다운스트림 포트에 ACS SrcValid/ReqRedir 가 켜져 있으면 `GPUPeerP2PBlocked`; NVLink 피어는 `links_active < links_total` 이면 `NVLinkLinksDown` | RTX PRO 6000(P2P), B300(NVLink) |
| `nic` (신규) | 포트 state ≠ ACTIVE, rate < 프로파일 최대 | NIC, DPU |
| `capacity` (신규) | GPU→NIC 경로의 각 홉 대역폭 표 (Gen×lanes, 포트 rate) 중 최솟값과 그 홉 | 셋 모두 |

홉 대역폭 표(방향당, 원시):

| 링크 | GB/s |
|---|---|
| PCIe Gen4 x16 | 32 |
| PCIe Gen5 x16 | 64 |
| PCIe Gen6 x16 | 128 |
| 400 Gb/s 포트 | 50 |
| 800 Gb/s 포트 | 100 |
| NVLink 5 (GPU 당 합계) | 900 (양방향 1,800) |

---

## 7. 출력 (`pathctl`)

- `pathctl path gpu <bdf|uuid> --artifact snapshot.json [--profile profiles/max-perf.json]`
- `pathctl path node --artifact ...` : 노드의 모든 GPU 를 한 표로.
- `pathctl check --artifact ... --profile ...` : CHECKS 만 내고 FAIL 이 하나라도
  있으면 종료 코드 3. 번인/CI 용.
- `--output text|json|mermaid`.

`profiles/max-perf.json` 이 기존 `fleet.json` 을 대체한다. 내용은 목표 네
줄이다: GPU↔NIC 근접성 하한(기본 PXB), 링크 Gen/lanes 는 장치 프로파일
최대, P2P 피어 경로 ACS off, NVLink 전 링크 active, NIC 포트 최대 속도.

기존 `explain` 의 좋은 점(모든 홉의 출처·증거 ID·관측 시각, unknown 에는 반드시
이유, 제한 사항 섹션)은 `path` 가 그대로 물려받는다.

---

## 8. 단계별 실행

각 단계는 독립적으로 머지 가능하고, 끝날 때 `make build vet arch-check test` 가
초록이어야 한다.

| 단계 | 내용 | 완료 기준 |
|---|---|---|
| 0. 축소 | Tier A 삭제, Tier B 태그 후 제거(또는 동결), nativepcie 제거 준비, Makefile 정리 | go.mod 에 k8s/grpc 없음, `make test` 가 네트워크 없이 수 분 내 완료 |
| 1. 캡처와 읽기 집합 | `capture` 서브커맨드, 5.1 속성 추가, 픽스처 v2, nativepcie 의 파서를 Go 로 이전 후 삭제 | 실제 B300·RTX PRO 6000·BF3 호스트 픽스처 3~4 개 커밋, 같은 픽스처는 같은 바이트 |
| 2. PCIe 경로 뷰 | NUMANode kind, 장치 프로파일, `locality` 규칙, `pathctl path` text/json (PCIe 만) | RTX PRO 6000 픽스처에서 2 절의 GPU/스위치/루트/NUMA 블록이 나옴 |
| 3. NIC/DPU | RDMA·넷 수집, `nic`·`capacity` 규칙, DPU 식별, DPU Arm 에서 에이전트 실행(aarch64 빌드) | BF3 호스트+DPU 픽스처로 GPU→DPU 경로와 병목 홉 표시 |
| 4. GPU 피어 | nvidia-smi 필드 확장, topo/nvlink 캔 파일, `p2p` 규칙, 출처 불일치 규칙 | B300 픽스처에서 NVLink 피어, RTX PRO 6000 픽스처에서 ACS P2P 판정 |
| 5. 판정과 문서 | `pathctl check`, `max-perf.json`, mermaid 출력, README·architecture 재작성, 구 `explain`/`fleet` 제거 | 세 픽스처 각각에 대해 CHECKS 기대값 골든 테스트 |

단계 1 이 끝나기 전에는 규칙을 더 만들지 않는다. 실제 픽스처 없이 만든 규칙은
지금까지의 이력이 보여주듯 하드웨어에서 한 번도 검증되지 않은 채 쌓인다.

---

## 9. 지키는 원칙 (기존 코드에서 가져옴)

- 도메인 코어는 표준 라이브러리만. arch-check 유지, 금지 import 목록은
  k8s/grpc 제거 후에도 그대로 둔다(재유입 방지).
- 추론 금지: 역방향 간선, 없는 끝점, 의도와 관측의 자동 병합을 만들지 않는다.
  NVLink 피어는 nvidia-smi 가 말한 것만 간선이 된다.
- `unknown` 은 실패가 아니다. 읽지 못한 속성은 이유와 함께 `UNKNOWN` 으로
  보이고 PASS 로 바뀌지 않는다.
- 모든 finding 은 관측 ID 를 가리킨다. 모든 홉은 출처와 시각을 가진다.
- 오프라인 모드는 결정적이다. 같은 픽스처, 같은 바이트.

---

## 10. 결정이 필요한 것과 위험

1. **Tier B 제거 여부.** 권장은 태그 후 제거. 최소안은 동결.
2. **쓰기 가능 sysfs 속성의 읽기.** `numa_node`, `config` 를 O_RDONLY 로만
   연다는 완화. 문서에 명시.
3. **PCI device ID.** 프로파일은 캡처에서 채운다. B300(HGX) 은 GPU 가 온보드
   ConnectX-8 의 내장 PCIe Gen6 스위치 아래에 붙을 수 있어, 경로 그래프가
   "GPU → CX8 스위치 → 루트 포트" 로 보일 수 있다. 모델은 이미 임의 깊이의
   `LOCATED_IN` 을 받으므로 코드 변경 없이 픽스처로 확인한다.
4. **NVML 대신 nvidia-smi.** go-nvml 은 cgo 를 다시 끌어온다. 바운디드
   nvidia-smi 를 유지하고, 출력 포맷 변화는 픽스처 캔 파일로 고정한다.
5. **DPU 모드 판별.** 호스트 sysfs 로는 DPU 모드/NIC 모드를 알 수 없다. DPU 를
   별도 노드로 취급하는 설계로 우회하고, 모드는 프로파일의 운영자 입력으로
   둔다.
6. **ACS 읽기 권한.** 확장 config 는 root 가 필요하다. 에이전트는 root 로
   돌되 여전히 아무것도 쓰지 않는다는 것을 테스트(`rt_` 류의 읽기 집합
   검증)로 고정한다.

---

## 11. 실행 결과 (2026-10-28)

계획의 0~5단계 중 하드웨어가 없어도 되는 부분을 한 번에 구현했다. 기존
트리는 `legacy/v0-control-plane` 태그로 보존하고 모두 제거했으며, 새 트리는
표준 라이브러리만 쓰는 단일 바이너리 `pathctl` 이다.

| 계획 항목 | 결과 |
|---|---|
| Tier A/B 축소, nativepcie 제거 | 완료. 약 11.7만 줄 → 약 4천 줄(테스트 포함) |
| 읽기 집합 확장 (속도, max 폭, NUMA, device ID, config/ACS, AER, RDMA, net, cpulist) | 완료, `internal/sysfs` |
| 캡처 모드 | 완료, `pathctl capture` |
| 장치 프로파일 | 완료, `internal/datapath/devices.go` + `--devices` JSON |
| locality / capacity / p2p-ACS / NVLink / NIC / NUMA 규칙 | 완료, `internal/datapath` |
| `pathctl path` text/json/mermaid, `pathctl check` 종료 코드 | 완료 |
| 실제 B300 / RTX PRO 6000 / BF3 픽스처 | **미완**. 합성 픽스처 2개로 대체. 실제 장비에서 `pathctl capture` 후 커밋 필요 |

계획과 달라진 점: 기존 도메인 코어(model, evidence, graph)도 유지하지 않았다.
스냅샷 한 장을 읽어 판정하는 도구에는 스트리밍 증거 창과 리듀서가 필요
없었고, 유지하면 쓰이지 않는 코드와 2만 줄의 테스트가 남았기 때문이다.
"3 샘플·30 초 지속" 규칙은 빠졌다. 반복 실행은 `pathctl check` 의 종료 코드로
번인 루프에서 처리한다.
