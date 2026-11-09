# 리서치: GDS + 스토리지 data-path 를 Rust 로 구성할 때의 장점

작성일: 2026-11-09. 범위: NVIDIA GPUDirect Storage(GDS, cuFile)와 그 아래의
스토리지 data-path(NVMe / NVMe-oF / RDMA / io_uring)를 호스트 측에서 Rust 로
구현할 때 얻는 이점, 한계, 그리고 이 레포(`pathctl`, Go)에 주는 시사점.

> 조사 방법: 웹 검색 결과와 1차 문서 요약을 종합했다. 이 세션의 네트워크
> 정책상 `docs.nvidia.com`, `docs.rs`, `forums.developer.nvidia.com`,
> `db.in.tum.de`, `ratatoskr.run` 은 직접 열 수 없어 검색 결과에 인용된
> 문구에 의존했다. 수치는 모두 출처의 조건에서만 유효하다.

---

## 0. 한 줄 요약

GDS 데이터 경로에서 **느린 것은 언어가 아니라 토폴로지와 복사**다. Rust 가
주는 이득은 "C 보다 빠르다"가 아니라, **C/C++ 와 같은 속도를 내면서 DMA 버퍼의
수명(등록·핀·해제)과 비동기 완료(completion) 계약을 타입으로 강제**해, GC 없는
평탄한 tail latency 와 작은 `unsafe` 감사 범위를 함께 얻는다는 점이다.
반대로 cuFile / DOCA 의 Rust 바인딩은 아직 성숙도가 낮고, 커널 Rust NVMe
드라이버는 미병합 상태라 **"전부 Rust"는 아직 불가능**하다. 현실적 구성은
"C 라이브러리(cuFile, rdma-core)를 얇은 `unsafe` 레이어로 감싸고 그 위의
I/O 오케스트레이션·버퍼 풀·런타임을 Rust 로 쓰는 것"이다.

---

## 1. 배경: GDS 데이터 경로가 하는 일

GDS 는 스토리지(로컬 NVMe, NVMe-oF, RDMA 기반 분산 FS)와 GPU 메모리 사이에
**CPU 바운스 버퍼를 거치지 않는 DMA 경로**를 만든다. NVIDIA 문서는 GDS 없이는
"CPU 의 bounce buffer 를 거치는 추가 복사가 필요해 지연이 늘고 유효 대역폭이
떨어진다"고 설명하며, PCIe 스위치를 경유하는 직접 경로는 일부 시스템에서
"CPU 를 거치는 경로 대비 최소 2배의 피크 대역폭"을 낸다고 밝힌다.

호스트 측 API 는 `libcufile.so` 의 cuFile 이다. 핵심 계약은 다음과 같다.

| 항목 | 내용 |
|---|---|
| 버퍼 등록 | `cuFileBufRegister` 로 GPU 버퍼를 핀(pin)·등록. 여러 IO 에 재사용되는 스트리밍 버퍼용. 핀 메모리 부족 시 `CU_FILE_GPU_MEMORY_PINNING_FAILED` |
| 메모리 제약 | P2P 모드는 `cudaMallocManaged`·시스템 malloc 메모리 불가 |
| 파일 제약 | `O_DIRECT` 필수. 아니면 페이지 캐시를 거쳐 직접 경로가 무효화 |
| 비동기 | Batch API(Linux AIO 유사, 로컬 NVMe/NVMe-oF 파일시스템 위주), Stream API(CUDA 12.2+, 스트림 순서 보장) |
| 호환 모드 | cuFile 불가 시 POSIX 로 폴백. Spectrum Scale/WekaFS 에서는 batch/async 가 내부적으로 호환 모드 |
| RDMA | `libcufile_rdma.so` + MLNX_OFED 의존. NVMe-oF 지원 여부는 드라이버 스택에 따라 다름 |

즉 GDS 데이터 경로의 "코드"는 (1) 버퍼를 올바르게 등록·해제하고, (2) 정렬과
`O_DIRECT` 를 지키며, (3) 수많은 비동기 IO 를 큐잉하고 완료를 수거하고,
(4) 폴백·에러를 처리하는 **I/O 오케스트레이션 레이어**다. 이 레이어가 바로
Rust 가 들어갈 자리다.

---

## 2. "Rust 기반"이 실제로 뜻하는 범위

```
┌──────────────────────────────────────────────┐
│ 애플리케이션 / 추론 서빙 / KV 캐시 오프로드  │  Rust 가능 (NIXL rust bindings, cudarc)
├──────────────────────────────────────────────┤
│ I/O 오케스트레이션: 버퍼 풀, 큐 깊이, 배치, │  ★ Rust 의 핵심 영역
│ 완료 수거, 폴백, 백프레셔, 토폴로지 인지    │  (monoio/glommio/tokio-uring, libublk)
├──────────────────────────────────────────────┤
│ FFI 경계: cuFile, ibverbs(rdma-core), DOCA   │  얇은 unsafe 레이어 (cufile-sys, ibverbs, nixl-sys)
├──────────────────────────────────────────────┤
│ libcufile.so / nvidia-fs.ko / rdma-core      │  C (NVIDIA 제공, 교체 불가)
├──────────────────────────────────────────────┤
│ 커널 NVMe / NVMe-oF / 파일시스템             │  C (Rust NVMe 드라이버는 out-of-tree)
└──────────────────────────────────────────────┘
```

cuFile 과 `nvidia-fs` 커널 모듈은 NVIDIA 가 C 로 제공하며 대체할 수 없다.
따라서 "Rust 기반 GDS 데이터 경로"는 **FFI 경계 위의 모든 것을 Rust 로 두는
설계**를 의미한다.

---

## 3. 장점

### 3.1 소유권 모델이 DMA 버퍼 수명 계약과 정확히 맞는다

GDS/RDMA 데이터 경로에서 가장 흔한 버그는 "등록(pin)된 버퍼를 디바이스가 아직
쓰고 있는데 해제·재사용한다", "등록 해제를 빼먹어 핀 메모리가 샌다"이다.
Rust 생태계는 이를 타입으로 표현하는 패턴이 이미 자리 잡았다.

- **cudarc**: `CudaSlice` 가 `Arc<CudaContext>` 를 소유해 디바이스보다 먼저
  해제되지 않고, free 는 drop 시 한 번만 호출돼 double free 가 구조적으로
  불가능하다. `PinnedHostSlice` 는 드라이버가 페이지 락을 보장하는 핀 메모리를
  `Pin<Vec>` 과 구분해 표현한다.
- **NIXL 1.5 Rust 바인딩**: 디바이스 개시 전송용 `MemView` 가 Drop 에서
  자동 해제된다.
- **ibverbs 계열 크레이트**: jonhoo/ibverbs 는 QP 가 CQ·PD 를 참조 카운트로
  붙잡아 수명 파라미터 없이 해제 순서를 보장한다. `ibverbs-rs` 는 two-sided
  send/recv 를 **borrow checker 로 검사**해 NIC 가 쓰는 동안 버퍼가 빌려진
  상태로 남게 하고, one-sided(원격이 언제든 접근)는 `unsafe` 로 남겨 위험을
  명시한다. SafeVerbs 는 같은 MR 에 다중 reader 와 겹치지 않는 다중 writer 를
  허용하는 연구용 안전 레이어다.
- **NVIDIA cuda-rust 이슈 #1431**: `cuMemHostRegister` 를 감싸 drop 시
  등록 해제를 보장하는 RAII `PinnedHostMapping` 제안이 진행 중이다.

cuFile 의 `cuFileBufRegister` / `cuFileHandleRegister` / 배치 핸들도 정확히
같은 "획득-사용-해제" 모양이라 RAII 로 감싸기에 적합하다. 등록 버퍼 풀을
타입으로 만들면 "등록 안 된 버퍼를 cuFileRead 에 넘기는" 실수를 컴파일
단계에서 막을 수 있다.

### 3.2 GC 가 없다: 핀 메모리·tail latency·cgo 문제의 부재

- Go 처럼 GC 가 있는 런타임은 (a) 힙을 라이브 데이터의 약 2배까지 키우고
  (b) 할당 압력 아래서 p99/p999 스파이크가 생기며 (c) cgo 경계를 넘길 때
  포인터 규칙과 `runtime.LockOSThread` 같은 제약이 붙는다. DMA 가 수십 GB 의
  핀 버퍼를 오래 붙잡는 데이터 경로에서는 GC 힙과 핀 메모리가 서로 어긋난다.
- 상태 머신 복제(SMR) 연구(arXiv 2405.11182)는 p99 20 ms 목표에서 Rust 가
  Go 대비 1.4배 처리량(69.5K vs 49.8K ops/s)을 냈고, 메모리가 제한되면 Go 는
  목표 tail latency 를 달성하지 못했다고 보고한다.
- Discord 의 Read States 서비스 Go→Rust 이전은 GC tail latency 제거가 동기였다.

데이터 경로는 "평균"이 아니라 "가장 느린 IO" 가 GPU 를 기다리게 만들므로,
결정적(deterministic) 메모리 관리가 곧 GPU 활용률이다.

### 3.3 C 와 같은 속도: 측정된 사례

- **vroom** (TUM 석사 논문, 2024): Rust 로 쓴 사용자 공간 폴링 NVMe 드라이버가
  **SPDK 와 IOPS 1% 이내**였고, psync/io_uring 의 시스템 콜 오버헤드(~10 µs)와
  비교해 SPDK 와 같은 등급의 지연을 보였다.
- **Linux Rust NVMe 드라이버** (Hindborg, LPC'22): Rust 드라이버가 C 드라이버와
  대등한 성능을 낸다고 보고했다. (단, 아래 5절의 미병합 상태 참고.)
- **libublk-rs** (ublk 사용자 공간 블록 디바이스): 8 큐 NVMe 위에서 queue
  depth 128 로 약 3M 4K IOPS. 2026-10 PR 에서 IO 당 사용자 사이클을 추가로
  12%(null) / 8%(loop) 줄였고 zero-copy 경로는 노이즈 수준이다.

결론: Rust 를 고른다고 C/C++ 대비 성능을 잃지 않는다. 얻는 것은 3.1·3.2 다.

### 3.4 completion 기반 I/O 와 thread-per-core 런타임 생태계

GDS 의 Batch API, io_uring, RDMA CQ 는 모두 **completion 기반**이다. Rust 는
이 모델을 1급으로 지원하는 런타임이 여럿 있다.

| 런타임 | 특징 |
|---|---|
| monoio (ByteDance) | thread-per-core, io_uring 네이티브, 자체 벤치에서 최고 성능 주장 |
| glommio | thread-per-core, 디스크 I/O 중심 설계 |
| tokio-uring | tokio 생태계 호환, io_uring fd 를 epoll 로 감시하는 간접층 |
| compio | 크로스 플랫폼(IOCP/io_uring) |

thread-per-core + io_uring 조합은 echo 벤치에서 Tokio 멀티스레드 대비
1.5~2배(일부 벤치 5배 이상) 처리량을 보고한다. GPU/NIC/NVMe 큐를 NUMA 노드별
코어에 고정하는 데이터 경로 설계와 자연스럽게 맞는다.

또한 **ioutgt** (2026-07, linux-block 공지): Rust 로 쓴 범용 사용자 공간
스토리지 타깃이 io_uring 위에서 커널 nvme-tcp / nvme-rdma 이니시에이터에
대해 xfstests quick 그룹을 통과했다. Rust 로 NVMe-oF 타깃 측 data-path 를
쓰는 것이 이미 실현 가능함을 보여준다.

### 3.5 `unsafe` 경계가 명시적이어서 감사 범위가 작다

cuFile, ibverbs, DOCA 는 어차피 C FFI 다. C/C++ 로 쓰면 코드 전체가
"잠재적 UB" 영역이지만, Rust 에서는 `-sys` 크레이트와 수십 줄의 `unsafe`
블록만 감사하면 된다. Sideway(RDMA-Rust) 프로젝트가 "ibv_reg_mr 과 post 는
unsafe 로 남기고 그 위에 안전한 메모리 풀을 쌓는다"는 설계를 택한 것이 이
패턴의 전형이다.

### 3.6 추론 서빙 스택에서 Rust 가 이미 1급 시민이다

- **NIXL** (NVIDIA Inference Xfer Library): GDS / GDS_MT / POSIX / OBJ /
  HF3FS / Mooncake / UCX / GPUNETIO 백엔드를 가진 전송 라이브러리로,
  TensorRT-LLM·vLLM·SGLang·Dynamo 가 쓴다. **공식 Rust 바인딩 `nixl-sys`
  (1.5.0)** 가 있고 GDS 백엔드가 빌드 옵션으로 포함된다. 즉 KV 캐시
  오프로드·분산 추론 경로를 Rust 로 쓰면서 GDS 를 그대로 쓸 수 있다.
- **cudarc** 가 CUDA 11.4~13.4 를 지원해 GPU 메모리 관리를 Rust 에서 한다.
- cuFile 직접 바인딩: `baracuda-cufile`(안전 래퍼 + 동적 로더, alpha),
  `singe-cufile`(batch/buffer/driver/file/stream 모듈), `cufile`/`cufile-sys`
  (일부 API 만, 정체), `gds-rs-example`(hello-world 포팅).

### 3.7 정적 바이너리·크로스 컴파일: 호스트와 DPU Arm 에서 같은 코드

Rust 는 Go 처럼 의존성 없는 정적 바이너리를 만들고 `aarch64` 크로스 컴파일이
쉽다. BlueField-3 Arm 에서 호스트와 동일한 data-path 프로브를 돌리는 구성
(이 레포 `docs/plan/data-path-v2.md` 가 이미 상정)에 유리하다. 다만 DOCA 는
공식 Rust SDK 가 없어 C FFI 셈(shim)이 필요하다(5절).

---

## 4. 장점이 "안" 나오는 경우: GDS 이득은 토폴로지가 결정한다

- NVIDIA DGX 플랫폼 엔지니어 슬라이드(2022)는 DGX-2/A100 로컬 스토리지에서
  **cuFile 과 POSIX 읽기가 대체로 대등**(작은 IO 에서 cuFile 이 약간 불리)
  하다고 보고했다. 한 서드파티 보고서는 단일 스레드 POSIX 가 커널 readahead
  덕에 GDS 를 이기는 경우도 적었다.
- GDS 가 크게 이기는 조건은 (1) 큰 IO, (2) CPU 가 병목, (3) 스토리지와 GPU 가
  **같은 PCIe 스위치 아래**(PIX/PXB)에 있고 ACS 가 P2P 를 막지 않으며,
  (4) NUMA 가 정렬된 경우다.
- 즉 언어 선택보다 `pathctl` 이 검사하는 `p2p-acs`, `nic-locality`,
  `numa-aligned`, `pcie-payload` 가 실제 대역폭을 더 크게 좌우한다. Rust 는
  "올바른 토폴로지 위에서 소프트웨어가 병목이 되지 않게" 하는 도구다.

---

## 5. 한계와 리스크 (정직하게)

| 리스크 | 현황 | 완화 |
|---|---|---|
| cuFile Rust 바인딩 성숙도 | `baracuda-cufile` alpha, `cufile-sys` 는 driver open/close·handle register 만 지원, 갱신 정체 | `bindgen` 으로 자체 `-sys` 생성 + 수십 줄 RAII 래퍼. API 표면이 작아 부담 적음 |
| DOCA 공식 Rust SDK 부재 | RustFS 가 NVIDIA 포럼에 문의. 답은 "C DOCA API 를 bindgen/C shim 으로" | DPU 측은 SNAP(NVMe-RDMA full offload) 설정 중심으로 두고 Rust 코드는 호스트 측에 집중 |
| 커널 Rust NVMe 드라이버 | out-of-tree. 2026-02 LSF/MM 에서 병합 제안, 3월 "중복 드라이버" 반대. `rnull` 만 진행 | 커널 경로는 C 를 쓴다고 전제. 사용자 공간(SPDK/vroom/io_uring)만 Rust |
| DMA 메모리와 Rust 앨리어싱 규칙 | 디바이스가 쓰는 중인 메모리를 `&[u8]`/`&mut` 로 들면 UB. 커널 Rust `CoherentAllocation` 도 `read_volatile`/`write_volatile` 로만 접근하고 "원자성은 보장 못 함" 명시. Tock 은 "완료 후에만 슬라이스 재획득" | 등록 버퍼는 raw pointer / `MaybeUninit` 로 보관, 완료 수거 후에만 안전 참조 발급. `DMA_BUF_IOCTL_SYNC` 호출 래핑 |
| io_uring vs SPDK | 연구들: io_uring 은 SPDK 급 성능에 2배 코어 필요. 2026 블로그: passthrough+fixed buffer+polling 으로 9~16% 이내(미검증) | 코어 예산 있으면 io_uring, 없으면 SPDK FFI 또는 vroom 류 |
| 워크로드 역전 | Conviva: Rust 쿼리 엔진에서 mmap→io_uring 전환 후 13.6s→21.8s 로 느려짐 | 데이터 경로는 반드시 실측(gdsio 유사 벤치)으로 검증 |
| 팀 역량·생태계 | Go 대비 개발 속도 저하, 채용 풀 작음 | 데이터 플레인만 Rust, 컨트롤 플레인·CLI 는 Go 유지(6절) |

---

## 6. 이 레포(`pathctl`)에 주는 시사점

`pathctl` 은 sysfs/`nvidia-smi` 를 읽어 경로와 PASS/FAIL 을 판정하는
**관측·판정 도구**다. 이 역할에는 Go 의 정적 바이너리·표준 라이브러리만으로
충분하고 GC 도 문제가 되지 않는다. 바꿀 이유가 없다.

Rust 가 가치를 내는 곳은 README 의 `watch`(burn-in) 다음 단계, 즉 **"경로가
실제로 그 속도를 내는지 데이터를 흘려 측정하는 부분"**이다. 제안:

1. **`pathbench` (Rust, 별도 바이너리)**: 지정 GPU 와 NVMe/NVMe-oF 대상 사이에
   cuFile Batch API 로 IO 를 흘려 GB/s·p99·CPU 사용률을 측정. 등록 버퍼 풀을
   RAII 로 관리하고 thread-per-core 런타임으로 NUMA 노드에 고정. 호환 모드
   폴백이 발생했는지(`cuFile` 이 POSIX 로 떨어졌는지)를 FAIL 로 보고.
2. `pathctl check` 에 `gds-bandwidth` 류 체크를 추가하고, `pathbench` 가
   있으면 호출해 결과를 JSON 으로 받아 합친다. 없으면 `N/A`.
3. `pathbench` 는 `aarch64` 로 크로스 빌드해 BlueField-3 Arm 에서도 동일하게
   실행(DPU 내부 경로 측정). DOCA 의존은 두지 않는다.
4. 이미 `docs/plan/data-path-v2.md` 가 `nativepcie` C+cgo 를 "Go 로 흡수 후
   제거"로 판단했듯, Go 바이너리에 cgo 로 cuFile 을 붙이는 방식은 피한다.
   GC·cgo 포인터 규칙·`LockOSThread` 가 핀 버퍼 수명과 충돌한다.

### 권장 Rust 스택

| 계층 | 선택 | 비고 |
|---|---|---|
| CUDA 메모리 | `cudarc` | `CudaSlice`/`PinnedHostSlice`, CUDA 11.4~13.4 |
| cuFile | 자체 `bindgen` `-sys` + RAII 래퍼 (또는 `baracuda-cufile` 추이 관찰) | 표면 작음: driver, handle, bufregister, batch, stream |
| 전송 추상화(선택) | `nixl-sys` | GDS/POSIX/OBJ 백엔드 전환, 추론 서빙과 공유 |
| RDMA(선택) | `ibverbs`(jonhoo) 또는 `ibverbs-rs` | two-sided 는 borrow check, one-sided 는 unsafe |
| 비동기 런타임 | `monoio` 또는 `glommio` | thread-per-core, io_uring 네이티브 |
| 블록 디바이스 노출(선택) | `libublk` | 사용자 공간 타깃 실험용 |

---

## 7. 참고 자료

GDS / cuFile
- NVIDIA GPUDirect Storage Design Guide v1.19.1 (2026-09): https://docs.nvidia.com/gpudirect-storage/pdf/design-guide.pdf
- GDS cuFile API Reference: https://docs.nvidia.com/gpudirect-storage/api-reference-guide/index.html
- GDS Overview Guide: https://docs.nvidia.com/gpudirect-storage/overview-guide/
- GDS Installation and Troubleshooting Guide: https://docs.nvidia.com/gpudirect-storage/troubleshooting-guide/index.html
- cuda-python cufile.buf_register: https://nvidia.github.io/cuda-python/cuda-bindings/latest/module/generated/cuda.bindings.cufile.buf_register.html
- GDS with IBM Spectrum Scale (DGX 측정 슬라이드, 2022): https://stg-a6b951f9.gpfsug.org/wp-content/uploads/2022/02/episode-18-NVIDIA-GPU-Direct-Storage-with-IBM-Spectrum-Scale.pdf
- 서드파티 GDS 측정 보고서: https://gds-report.pages.dev/theory/
- KvikIO: https://github.com/rapidsai/kvikio

Rust 바인딩 / 크레이트
- baracuda-cufile: https://docs.rs/baracuda-cufile/latest/baracuda_cufile/
- singe-cufile: https://docs.rs/singe-cufile/latest/singe_cufile/
- cufile / cufile-sys: https://docs.rs/cufile/latest/cufile/ , https://crates.io/crates/cufile-sys/0.1.1
- gds-rs-example: https://github.com/n4o847/gds-rs-example
- cudarc: https://docs.rs/cudarc , https://docs.rs/cudarc/latest/cudarc/driver/safe/index.html
- NVIDIA cuda-rust PinnedHostMapping 제안: https://github.com/NVIDIA/cuda-rust/issues/1431
- NIXL 및 nixl-sys: https://github.com/ai-dynamo/nixl , https://docs.rs/crate/nixl-sys/latest , https://developer.nvidia.com/blog/enhancing-distributed-inference-performance-with-the-nvidia-inference-transfer-library
- ibverbs (jonhoo): https://github.com/jonhoo/rust-ibverbs , https://docs.rs/ibverbs
- ibverbs-rs: https://github.com/Tikitikitikidesuka/ibverbs-rs
- SafeVerbs: https://github.com/crazyboycjr/safeverbs
- RDMA-Rust "Why another RDMA wrapper" (2025-11): https://rdma-rust.github.io/2025/11/16/why-another-rdma-wrapper/
- libublk-rs: https://crates.io/crates/libublk , https://github.com/ublk-org/libublk-rs/pull/69
- dma-buf crate: https://docs.rs/dma-buf

Rust 스토리지 성능·사례
- vroom, "Writing an NVMe Driver in Rust" (TUM 2024): https://db.in.tum.de/~ellmann/theses/finished/24/pirhonen_writing_an_nvme_driver_in_rust.pdf
- Linux PCI NVMe driver in Rust (LPC'22): https://lpc.events/event/16/contributions/1180/attachments/1017/1961/deck.pdf
- Rust for Linux NVMe driver 페이지: https://rust-for-linux.com/nvme-driver
- LSF/MM/BPF 2026 Rust in block subsystem: https://ratatoskr.run/lkml/2026/02/3416523 , https://lkml.iu.edu/hypermail/linux/kernel/2603.2/02697.html
- rnull 블록 드라이버: https://www.linuxnews.net/articles/the-rnull-rust-block-driver
- ioutgt 공지 (2026-07): https://ratatoskr.run/linux-block/2026/07/17216611
- TiKV: https://github.com/tikv/tikv
- Ceph Crimson: https://docs.ceph.com/en/latest/crimson/crimson/
- RustFS DOCA Rust SDK 문의: https://forums.developer.nvidia.com/t/inception-member-rustfs-26k-stars-seeking-guidance-on-rust-sdk-for-doca-ec-rdma-offloading/367421
- DOCA SNAP-4 Service Guide: https://docs.nvidia.com/doca/sdk/doca-snap-4-service-guide.pdf

io_uring / SPDK / 런타임
- "What Modern NVMe Storage Can Do" (VLDB 2023): https://vldb.org/pvldb/vol16/p2090-haas.pdf
- libaio/SPDK/io_uring 비교 (SYSTOR 2022): https://atlarge-research.com/pdfs/2022-systor-apis.pdf
- "High-Performance DBMSs with io_uring": https://arxiv.org/pdf/2512.04859
- xNVMe (CIDR 2026): https://www.vldb.org/cidrdb/papers/2026/p6-houlborg.pdf
- io_uring, SPDK, and the Kernel Bypass Wars (블로그, 미검증): https://muratkarslioglu.com/blog/io-uring-spdk-kernel-bypass/
- monoio: https://github.com/bytedance/monoio
- compio: https://github.com/bdbai/compio
- Thread-per-core async in Rust: https://www.include.gr/writing/rust-thread-per-core-async.html
- Conviva, mmap→io_uring 역전 사례: https://www.conviva.ai/resource/we-replaced-mmap-with-io_uring-in-our-rust-query-engine-it-got-slower/

GC / tail latency
- "The Cost of Garbage Collection for State Machine Replication": https://arxiv.org/pdf/2405.11182

DMA 안전성
- Tock DMA slice / DmaFence: https://docs.tockos.org/src/kernel/utilities/dma_slice.rs , https://docs.tockos.org/kernel/platform/dma_fence/trait.dmafence
- Linux Rust dma_buf 바인딩 패치: https://lkml.iu.edu/2508.3/09123.html
- Linux Rust dma volatile 안전성 주석 패치 (2026-01): https://patchew.org/linux/20260130-dma-doc-update-v1-1-bac46939cfea@kernel.org
