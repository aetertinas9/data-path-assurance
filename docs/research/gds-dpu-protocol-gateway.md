# 리서치: GDS / DPU 관련 프로토콜 게이트웨이를 만든다면 어디에 이득이 있나

작성일: 2026-11-09. 앞선 문서 `rust-gds-storage-data-path.md` 의 후속.
질문: "GDS 나 DPU 쪽에서 프로토콜 게이트웨이(GW)를 만들면 앞으로 이득이 있을
영역이 있는가?"

> 조사 방법: 웹 검색 결과와 1차 문서 인용에 의존했다. 이 세션에서
> `docs.nvidia.com`, `developer.nvidia.com` 은 직접 열 수 없었다. NVIDIA 의
> 성능 수치(5배 등)는 벤더 주장이다.

---

## 0. 결론 요약

**있다. 단, 이득이 나는 곳은 "데이터 플레인을 번역하는 GW"가 아니라 "컨트롤
플레인을 번역하고 데이터 플레인은 RDMA zero-copy 로 남기는 GW", 혹은 GW 자신이
데이터를 소유하는 "티어(tier) GW"다.** GDS 의 가치는 복사와 홉을 없애는 데
있으므로, 데이터가 GW 의 메모리를 거쳐 지나가기만 하는 전통적 프록시는 GDS 의
이점을 스스로 지운다.

2026년 하반기 기준으로 생태계가 **GW 모양의 이음새를 동시에 여러 개 만들고
있다**는 점이 기회다.

| 이음새 | 상태 (2026-11) | GW 기회 | 이득 크기 |
|---|---|---|---|
| S3 over RDMA (cuObject) | 2026-09-30 GA. Cloudian·Dell·HPE 가 서버 라이브러리 통합 | 기존 S3 스토어 앞단에 RDMA 데이터 플레인을 붙이는 **캐시형 GW** | 큼, 가까움 |
| 추론 KV 캐시 티어 (CMX / DOCA Memos vs LMCache / Mooncake / KVBM) | BlueField-4 기반 플랫폼은 H2 2026 예정, 2026-10 기준 출하 없음 | 설치 기반(H100/B200 + CX-7/BF-3)에 **"소프트웨어 CMX"** 와 백엔드 추상화 GW | 가장 큼, 창이 열려 있음 |
| GPU 개시 I/O (SCADA / Storage-Next, cuFile 오픈소스) | 2026-08 cuFile 오픈소스(xio-sig), SCADA Server SDK 공개, 40+ 벤더 | 기존 NVMe-oF/오브젝트 백엔드가 **GPU 발행 512B 요청**을 받게 하는 서버측 GW | 큼, 불확실, 선점 효과 |
| NVMe/TCP ↔ RDMA 전송 GW (DPU SNAP) | SNAP full-offload 는 NVMe-RDMA 타깃만. 기업은 NVMe/TCP 표준화 | 레거시 TCP 배열을 GDS 경로로 끌어오는 GW | 중간, DPU 위 TCP 종단은 성능 한계 |
| 파일 GW (virtio-fs → NFS) | DOCA SNAP virtio-fs 는 beta, libnfs 번역 alpha | NVIDIA 가 직접 하는 중 | 작음 |

반대로 **로컬 NVMe → 같은 PCIe 스위치 아래 GPU** 경로에는 어떤 GW 도 끼어들
이유가 없다. 거기서는 `pathctl` 이 보는 토폴로지(ACS, PIX, NUMA)가 전부다.

---

## 1. 왜 지금 "게이트웨이"가 의미를 갖게 됐나

2025~2026 에 NVIDIA 스토리지 스택이 세 방향으로 동시에 열렸다.

1. **오브젝트가 GDS 1급 대상이 됨 (cuObject).** 컨트롤 플레인은 평범한 S3
   HTTP 요청으로 두고, 데이터 전송만 `x-amz-rdma-token` / `x-amz-rdma-reply`
   헤더로 협상해 스토리지 노드 메모리 ↔ GPU 메모리 RDMA 로 옮긴다. 토큰이
   있는 요청은 와이어에 본문이 없다. 서버 라이브러리는 파트너 오브젝트
   서버에 통합되는 형태다.
2. **DPU 가 스토리지 노드가 됨 (BlueField-4 CMX, DOCA Memos).** KV 캐시 전용
   이더넷 부착 플래시 티어("G3.5")를 BlueField-4 가 운영하고, 컴퓨트 노드의
   BlueField-4 가 이니시에이터가 된다. 추론 프레임워크는 NIXL 로 접근하며,
   NIXL 의 `DOCA_MEMOS` 백엔드는 호스트에 **NVMe KV 네임스페이스**로 보이고
   128비트 hex 키(동적 모드에서는 캐시 키의 SHA-256 절단)를 쓴다.
3. **GPU 가 I/O 를 직접 발행함 (SCADA, Storage-Next).** BaM(ASPLOS 2023) 의
   제품화. GPU 가 스토리지 요청을 만들고 완료를 직접 처리하며 수십만 건을
   동시에 띄운다. Storage-Next 는 512바이트 랜덤 접근에 최적화한 드라이브와
   컨트롤러 표준화이고, SCADA Server SDK 는 스토리지 벤더가 GPU 발행 요청을
   RDMA 로 받는 서버를 만들게 한다. 2026-08-04 cuFile 과 스토리지 스택이
   오픈소스화되어 Google·Intel·Meta·NVIDIA 가 초기 메인테이너인 GitHub
   조직(XIO-SIG 로 보도)으로 들어갔다.

세 방향 모두 **"기존 스토리지가 새 프로토콜을 말하게 하는 어댑터"**를
필요로 한다. 그것이 GW 다.

---

## 2. 이득이 있는 GW 유형, 구체적으로

### 2.1 S3-over-RDMA 캐시 GW (가장 가깝고 명확)

**무엇.** MinIO 커뮤니티판, Ceph RGW, RustFS, 퍼블릭 클라우드 S3 등 cuObject
서버 라이브러리를 아직 통합하지 않은 스토어 앞에 서서, S3 컨트롤 플레인은
그대로 프록시하고 `x-amz-rdma-token` 이 붙은 GET/PUT 은 GW 의 로컬 NVMe 캐시
에서 GPU 메모리로 RDMA 로 직접 전송한다.

**왜 이득인가.**
- 파트너(Cloudian HyperStore, Dell ObjectScale, HPE Alletra MP X10000) 가
  아닌 스토어 사용자에게 GDS-to-object 를 제공한다. MinIO AIStor 도 같은
  프로토콜을 따른다고 보고되지만 AIStor 는 상용이다.
- **프로토콜 정규화 자체가 가치다.** 클라이언트 간 토큰 형식이 이미 갈린다.
  elbencho 는 토큰에 접미사를 붙이자 서버가 전부 거부해 "bare cuObject
  descriptor" 로 고쳤고, ROCm hipObject 는 3필드 형식을 쓴다. GW 가 둘 다
  받아주면 클라이언트 호환성 문제를 한 곳에서 흡수한다.
- 서버는 성공 시 `x-amz-rdma-bytes-transferred` 를 돌려주고 등록 버퍼를 응답
  본문처럼 노출해야 한다. 이 "등록 버퍼 풀 + 완료 수거" 가 앞 문서의 Rust
  데이터 플레인 설계와 정확히 같은 문제다.

**조건.** GW 는 반드시 **캐시/티어**여야 한다. 원격 S3 에서 받은 바이트를 GW
메모리에 내려놓고 다시 RDMA 로 보내는 순수 프록시는 바운스 버퍼를 GW 로 옮긴
것뿐이라 지연이 늘고 GDS 의 의미가 사라진다. 콜드 미스는 S3 HTTP 로 떨어지고
핫 데이터는 GW 의 NVMe 에서 RDMA 로 나가는 구조여야 한다.

### 2.2 KV 캐시 티어 GW / 백엔드 추상화 (가장 큰 이득, 시간 창 존재)

**무엇.** 추론 프레임워크(vLLM, SGLang, TensorRT-LLM, Dynamo) 가 NIXL 또는
LMCache 커넥터로 접근하는 **하나의 KV 인터페이스**를 제공하고, 뒤에서 로컬
NVMe / NVMe-oF / S3 / 나중에는 CMX 를 티어링하는 노드.

**왜 이득인가.**
- **하드웨어 결합 해소.** 공개 논의(modelplane 이슈 #72)가 지적하듯 백엔드
  선택이 배포가 아니라 하드웨어에 묶여 있다. Mooncake 는 RDMA 전용이고
  etcd + Master + 노드별 에이전트를 요구하며, KVBM 은 NVIDIA 결합이다.
  LMCache 는 CPU RAM / 로컬 / Redis / GDS / InfiniStore / Mooncake 를 다층으로
  쓴다. 운영자는 이들 사이를 옮겨 다닐 GW 를 원한다.
- **설치 기반을 위한 "소프트웨어 CMX".** CMX 는 BlueField-4 + Vera Rubin +
  Spectrum-X 전제다. 파트너 플랫폼은 H2 2026 예정이고 2026-10 기준 출하된
  것이 없다. H100/B200 + ConnectX-7 + BlueField-3 로 돌아가는 클러스터가 향후
  수년간 다수다. 이 설치 기반에 NVMe KV 네임스페이스 또는 NIXL 플러그인
  형태로 같은 의미론(128비트 키, 값 단위 디스크립터)을 제공하는 GW 는 CMX
  가 오기 전에 자리를 잡고, 오고 나서는 CMX 를 한 티어로 흡수한다.
- **측정 가능한 효과.** 연구와 벤더가 NVMe 외부 KV 캐시로 TTFT 와 세션 수
  개선을 보고한다(py-kvcache 특성화, MinIO 의 KV 캐시 벤치 글). NVIDIA 의
  "5배 토큰/초" 는 벤더 주장이다.

**조건.** 키 의미론을 DOCA Memos / NIXL 과 맞춰야 나중에 CMX 로 이행할 수
있다. 즉 GW 는 처음부터 NIXL 백엔드 플러그인으로 짜는 것이 유리하다.

### 2.3 SCADA / Storage-Next 서버측 GW (선점형)

**무엇.** 기존 NVMe-oF 타깃이나 오브젝트 백엔드가 GPU 발행 512B 요청을 RDMA
로 받도록 하는 서버. 수십만 개 동시 요청을 백엔드가 소화할 수 있는 크기로
합치고(coalescing) 완료를 GPU 큐에 돌려준다.

**왜 이득인가.** 표준이 막 만들어지고 있고(xio-sig, SCADA Server SDK, 40+
벤더), 참조 구현이 적다. 한 보도는 단일 서버 2.3억 512B IOPS 를 인용하지만
단일 출처라 미검증이다. 여기서 GW 는 "오늘 쓸 제품"보다 "표준에 들어가는
참조 구현"의 가치가 크다.

**조건.** cuFile 오픈소스 저장소와 라이선스, Server SDK 의 접근성을 확인해야
한다. 이번 조사에서 저장소 URL 과 라이선스는 확인하지 못했다.

### 2.4 NVMe/TCP ↔ RDMA 전송 GW (중간, 배치 위치가 중요)

**무엇.** 기업 다수가 무손실 패브릭 없이 NVMe/TCP 로 표준화했다(Marvell,
simplyblock 자료). GPU 쪽 GDS 는 RDMA 경로를 원한다. 둘을 잇는 GW.

**이득과 한계.**
- SNAP full-offload 는 NVMe-RDMA 타깃에만 적용되고 Arm 코어는 컨트롤 플레인만
  만진다. SNAP-direct zero-copy 역시 SPDK 백엔드가 NVMe-oF RDMA bdev 일
  때만이다. 즉 **TCP 백엔드를 붙이는 순간 DPU 의 zero-copy 가 깨진다.**
- arXiv 2509.13997 은 SmartNIC 의 RDMA 는 호스트와 대등하지만 SmartNIC 의
  TCP 는 현저히 낮다고 보고한다. Napatech 는 NVMe/TCP 를 DPU 로 완전 오프로드
  한다고 주장하지만 벤더 자료다.
- 따라서 TCP 종단은 BlueField Arm 이 아니라 **전용 x86 GW 노드**에 두고, 그
  노드가 GPU 쪽으로는 NVMe-oF RDMA 타깃이 되어 SNAP full-offload 를 받는
  구성이 맞다. 이러면 2.1 과 같은 "티어 노드" 로 수렴한다.

### 2.5 파일 GW (작음)

DOCA SNAP virtio-fs 는 beta 이고, 파일 연산을 libnfs 로 NFS 요청으로 번역하는
경로는 alpha 다. NVIDIA 가 직접 하고 있는 영역이라 독자 GW 의 차별점이
작다. 예외는 비 NVIDIA DPU(AMD Pensando, Intel IPU) 를 쓰는 환경이다.

---

## 3. 이득이 없거나 역효과인 경우

- **GPU 와 스토리지가 같은 PCIe 스위치 아래(PIX/PXB) 인 로컬 경로.** 어떤
  GW 도 홉을 추가한다. GDS 는 홉을 없애려고 있다.
- **데이터가 GW CPU 메모리를 통과만 하는 프록시.** 바운스 버퍼를 호스트에서
  GW 로 옮긴 것이다. DGX 측정에서도 cuFile 과 POSIX 가 대등한 경우가 있었던
  만큼, 복사 하나가 들어오면 이득이 사라진다.
- **NVIDIA 가 같은 하드웨어에서 같은 것을 출하하는 영역** (BF-4 위의 DOCA
  Memos, SNAP NVMe 에뮬레이션). 차별화는 비 BF-4 설치 기반, 비 NVIDIA DPU,
  멀티 백엔드 추상화에서만 나온다.

---

## 4. GW 가 가치를 내기 위한 설계 원칙 (조사에서 반복된 패턴)

1. **컨트롤 플레인은 번역하고 데이터 플레인은 RDMA 로 남긴다.** cuObject 가
   HTTP 헤더로 협상하고 본문을 비우는 것, SNAP 이 Arm 을 컨트롤에만 쓰는 것,
   CMX 가 NIXL 디스크립터로 값을 옮기는 것 모두 이 원칙이다.
2. **GW 는 데이터를 소유하는 티어다.** 캐시/KV 티어로서 자기 NVMe 에서 GPU
   로 쏜다. 프록시가 아니다.
3. **토폴로지를 알고 버퍼를 배치한다.** 등록 버퍼와 큐를 요청 GPU 와 같은
   NUMA·PCIe 스위치에 둬야 한다. 이것이 `pathctl` 이 읽는 정보다.
4. **키·토큰 의미론을 업스트림과 맞춘다.** NIXL 백엔드 플러그인,
   DOCA Memos 키 형식, cuObject 헤더 계약을 그대로 따른다. 이행 비용이
   낮아지고 클라이언트 호환 문제를 GW 가 흡수한다.
5. **데이터 플레인은 GC 없는 언어로.** 앞 문서의 결론과 같다. 등록 버퍼 풀,
   완료 수거, 수십만 동시 요청은 Rust 의 소유권 모델과 thread-per-core
   런타임이 맞는 자리다.

---

## 5. 이 레포(`pathctl`)와의 연결

GW 는 `pathctl` 의 소비자이자 확장 지점이 될 수 있다.

- **어드미션 체크.** GW 가 어떤 GPU 의 요청을 어떤 NIC/버퍼로 서빙할지
  결정할 때 `pathctl path --output json` 의 locality(PIX/PXB/NODE/SYS), ACS,
  NUMA 정보를 쓴다. `p2p-acs FAIL` 인 경로로 RDMA 를 태우면 성능이 아니라
  정합성 문제가 된다.
- **경로 보증을 서비스로.** GW 노드와 컴퓨트 노드 양쪽에서 `pathctl check` /
  `watch` 를 돌려 "이 경로가 선언된 속도를 낼 수 있는가" 를 GW 의 헬스
  체크에 포함한다. BlueField Arm 에서도 같은 바이너리를 돌리는 계획
  (`docs/plan/data-path-v2.md`) 과 맞물린다.
- **측정.** 앞 문서에서 제안한 Rust `pathbench` 가 cuObject / NIXL 백엔드를
  타깃으로 삼으면 GW 의 실제 GB/s·p99 를 경로 판정과 함께 보고할 수 있다.

---

## 6. 추천 순서

1. **KV 캐시 티어 GW 를 NIXL 백엔드 플러그인으로** (2.2). 가장 큰 수요, 창이
   열려 있음, 설치 기반이 넓음. 키 의미론은 DOCA Memos 를 따른다.
2. 같은 노드에 **S3-over-RDMA 캐시 프론트** (2.1) 를 얹는다. 데이터 플레인
   (등록 버퍼 풀, RDMA 완료 수거) 을 공유한다.
3. 여력이 되면 **SCADA Server SDK 참조 구현** (2.3) 으로 표준 논의에
   참여한다.
4. NVMe/TCP 브리지(2.4) 는 1·2 의 노드가 NVMe-oF RDMA 타깃 역할을 겸하는
   형태로만 한다. 파일 GW(2.5) 는 보류.

---

## 7. 참고 자료

S3 over RDMA / cuObject
- NVIDIA cuObject 문서: https://docs.nvidia.com/gpudirect-storage/cuobject/index.html
- NVIDIA 블로그, RDMA for S3-Compatible Storage: https://blogs.nvidia.com/blog/s3-compatible-ai-storage/
- Cloudian 발표: https://cloudian.com/blog/cloudian-deploys-new-nvidia-rdma-for-s3-compatible-storage-a-game-changer-for-ai-storage-performance/
- Dell ObjectScale: https://www.dell.com/en-us/infohub/articles/accelerating-ai-workloads-with-rdma-for-s3-compatible-storage-a-game-changer-with-dell-objectscale
- multi-storage-client 토큰 처리 PR: https://github.com/NVIDIA/multi-storage-client/pull/189
- elbencho `--cuobj` PR: https://github.com/breuner/elbencho/pull/112
- Everpure s5cmd RDMA PR: https://github.com/PureStorage-OpenConnect/Everpure-S5cmd-rdma/pull/1
- DDN simple-s3-rdma: https://github.com/DDNStorage/simple-s3-rdma
- ROCm S3-over-RDMA 백엔드 PR: https://github.com/ROCm/rocm-ernic/pull/154
- hipObject: https://github.com/potatogim/hipObject

CMX / BlueField-4 / DOCA Memos
- NVIDIA CMX 제품 페이지: https://www.nvidia.com/en-us/data-center/ai-storage/cmx/
- NVIDIA 개발자 블로그, CMX 소개: https://developer.nvidia.com/blog/introducing-nvidia-bluefield-4-powered-inference-context-memory-storage-platform-for-the-next-frontier-of-ai/
- NVIDIA BlueField-4 발표: https://blogs.nvidia.com/blog/bluefield-4-ai-factory/
- Vera BlueField-4 STX: https://nvidianews.nvidia.com/news/nvidia-vera-bluefield-4-stx-brings-agentic-ai-storage-processing-with-in-silicon-security
- NIXL DOCA_MEMOS 백엔드 PR: https://github.com/ai-dynamo/nixl/pull/1717
- Blocks & Files, KV cache offload to NVMe (2026-01): https://blocksandfiles.com/2026/01/06/nvidia-standardizes-gpu-cluster-kv-cache-offload-to-nvme-ssds/
- NAND Research 노트: https://nand-research.com/research-note-improving-inference-nvidias-inference-context-memory-storage-platform/
- Solidigm CMX 설명: https://www.solidigm.com/products/technology/what-is-cmx-context-memory-storage.html
- MinIO KV 캐시 벤치 글: https://www.min.io/blog/minio-doubles-the-sessions-your-gpus-hold-measured-the-way-youd-actually-run-it

KV 캐시 오프로드 스택
- Dynamo KV Cache Offloading 문서: https://docs.nvidia.com/dynamo/backends/v-llm/kv-cache-offloading
- LMCache NIXL 백엔드: https://docs.lmcache.ai/legacy/kv_cache/storage_backends/nixl.html
- modelplane KVOffloadTier 이슈: https://github.com/modelplaneai/modelplane/issues/72
- py-kvcache 특성화 (arXiv 2609.11744): https://arxiv.org/pdf/2609.11744
- ObjectCache (arXiv 2605.22850): https://arxiv.org/pdf/2605.22850
- Backend.AI, KV cache offloading 원리: https://www.backend.ai/blog/2026-04-how-to-save-gpu-memory-in-llm-serving-kv-cache-offloading

SCADA / Storage-Next / cuFile 오픈소스
- StorageReview, SCADA 와 cuFile 오픈소스: https://www.storagereview.com/news/nvidia-scada-puts-storage-control-on-the-gpu-as-cufile-goes-open-source
- Futurum, FMS 2026: https://futurumgroup.com/insights/nvidia-ai-storage-goes-open-at-fms-2026-is-open-source-the-new-moat/
- NAND Research, FMS 2026: https://nand-research.com/fms-2026-ai-moving-boundaries-between-memory-storage/
- SiliconANGLE (2026-08-04): https://siliconangle.com/2026/08/04/nvidia-open-sources-cufile-api-accelerating-gpu-read-write-capability-high-speed-storage/
- Forbes (2026-08-05): https://www.forbes.com/sites/jonmarkman/2026/08/05/nvidia-open-sourced-cufile-a-new-era-for-gpu-driven-storage/

DPU SNAP / 전송 GW
- DOCA SNAP Services: https://docs.nvidia.com/doca/sdk/DOCA-SNAP-Services/index.html
- DOCA SNAP-3 User Guide: https://docs.nvidia.com/doca/sdk/DOCA-SNAP-3-User-Guide/index.html
- DOCA SNAP Virtio-fs Service Guide: https://docs.nvidia.com/doca/sdk/nvidia-doca-snap-virtio-fs-service-guide.pdf
- BlueField-3 SNAP v4.5.0: https://docs.nvidia.com/networking/display/nvidia-bluefield-3-snap-for-nvme-and-virtio-blk-v4-5-0.0.pdf
- FlexBSO (arXiv 2409.02381): https://arxiv.org/pdf/2409.02381
- SmartNIC RDMA vs TCP (arXiv 2509.13997): https://web3.arxiv.org/pdf/2509.13997
- Napatech Link-Storage: https://www.napatech.com/products/link-storage-software/
- SNIA SDC 2024, Optimizing NVMe over TCP: https://snia.org/sites/default/files/2025-05/SNIA-SDC2024-Burla-Nalla-Optimizing-NVMe-Over-TCP.pdf
- GPU Operator, GPUDirect RDMA and GDS: https://docs.nvidia.com/datacenter/cloud-native/gpu-operator/26.7/gpu-operator-rdma.html
