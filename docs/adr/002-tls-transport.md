# ADR-002: TLS Transport

## 상태
Accepted

## 컨텍스트

drpc ↔ drps 간 제어 연결의 암호화 방식을 결정해야 한다.

초기 설계에서는 WireGuard를 검토했으나 다음 문제가 확인되었다:

- **K8s 배포 제약** — WireGuard는 LB 뒤에 놓을 수 없음 (피어 키 바인딩). hostNetwork + DaemonSet 필수
- **UDP 의존** — 일부 기업 네트워크에서 UDP 차단 시 사용 불가
- **서버 추가 시 마찰** — 서버마다 WG peer 설정 + drpc 재설정 필요
- **splice() 무의미** — drpc↔drps 데이터가 yamux 위에서 동작하므로 WireGuard의 splice() zero-copy 이점이 없음
- **인프라 결합** — 애플리케이션만으로 완결되지 않고 OS 레벨 WireGuard 설정 필요

반면 TLS는:
- Go 표준 라이브러리 (`crypto/tls`) 제공. 코드 2줄 변경
- LB 뒤에서 정상 동작 (TCP 기반)
- K8s 일반 Service + LoadBalancer로 배포 가능
- 인증서 관리는 Let's Encrypt / cert-manager로 자동화 가능

## 결정

**drpc ↔ drps 제어 연결은 TLS over TCP로 암호화한다. WireGuard는 사용하지 않는다.**

### 포트 바인딩

| 포트 | 용도 | 접근 |
|------|------|------|
| `:80` | HTTP 사용자 트래픽 | 공개 |
| `:443` | HTTPS 사용자 트래픽 (SNI 패스스루) | 공개 |
| `:7000` | drpc 제어 연결 (TLS) | 공개 (API Key 인증) |

모든 포트가 LB 뒤에 배치 가능:

```
         LB
  :80  :443  :7000
    |    |     |
  drps-A  B    C
```

### 보안 모델

```
Layer 1: 전송 암호화
├─ user ↔ drps :443 → SNI 패스스루 (end-to-end TLS)
└─ drpc ↔ drps :7000 → TLS (Go crypto/tls)

Layer 2: 인증 (Authentication)
└─ drpc → drps → API Key (Login 메시지)

Layer 3: 인가 (Authorization)
├─ API Key → 허용된 alias/hostname만 등록 가능
└─ 프록시별 IP whitelist (선택)
```

### 구현

Phase 1 (개발): 보안 없이 raw TCP

```go
conn, err := net.Dial("tcp", serverAddr)
ln, err := net.Listen("tcp", controlAddr)
```

Phase 2 (프로덕션): TLS 추가

```go
conn, err := tls.Dial("tcp", serverAddr, tlsConfig)
ln, err := tls.Listen("tcp", controlAddr, tlsConfig)
```

변경: **2줄.** Go 표준 라이브러리. 외부 의존성 없음.

### K8s 배포

```yaml
apiVersion: v1
kind: Service
metadata:
  name: drps
spec:
  type: LoadBalancer
  ports:
    - name: http
      port: 80
    - name: https
      port: 443
    - name: control
      port: 7000     # drpc TLS 제어 연결. LB 뒤에서 정상 동작.
```

## 결과

### 장점
- LB 뒤에 모든 포트 배치 가능 → K8s 네이티브 배포
- UDP 의존 없음 → 어떤 네트워크에서든 동작
- 서버 추가 시 drpc 재설정 불필요 (LB 주소 하나)
- Go 표준 라이브러리로 구현 → 외부 의존성 없음
- 인증서 자동화 (cert-manager, Let's Encrypt)

### 단점
- TLS 핸드셰이크 오버헤드 (제어 연결은 장기 유지이므로 무시 가능)
- 인증서 관리 필요 (자동화 가능)
- WireGuard 대비 네트워크 레벨 격리 없음 (API Key 인증으로 보완)

### 대안 (선택하지 않음)

| 대안 | 미선택 이유 |
|------|------------|
| WireGuard | LB 불가, UDP 의존, K8s hostNetwork 필수, 서버 추가 시 peer 재설정 |
| mTLS | 클라이언트 인증서 배포 부담. API Key로 충분 |
| SSH 터널 | 오버헤드, 멀티 연결 관리 복잡 |
| 암호화 없음 | 프로덕션 사용 불가 |

## 참고 자료
- [Go crypto/tls](https://pkg.go.dev/crypto/tls)
- [ADR-001](./001-minimalist-architecture.md)
