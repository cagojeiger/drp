# 00. 전체 구조

## 한 문장

> NAT 뒤 서비스를 N대 서버 중 **아무 데나** 요청해도 도달하게 한다.

## 구성 요소

| 컴포넌트 | 역할 | POC 파일 |
|----------|------|----------|
| **drps** (server) | HTTP 수신, Host 라우팅, mesh 통신 | `drps.py` |
| **drpc** (client) | NAT 뒤에서 서버에 outbound 연결, work conn 제공 | `drpc.py` |
| **mesh** | 서버 간 peer 연결, broadcast, relay | `mesh.py` |
| **protocol** | TLV+JSON 프레임, 메시지 생성 | `protocol.py` |

## 물리 구성

```mermaid
graph TD
    subgraph "Public Internet"
        User["User<br/>curl -H Host: myapp.example.com"]
    end

    subgraph "Server Cluster"
        A["drps-A<br/>:8001 / :9001<br/>local_map: myapp ✓"]
        B["drps-B<br/>:8002 / :9002<br/>local_map: (empty)"]
        C["drps-C<br/>:8003 / :9003<br/>local_map: (empty)"]
        A --- B
        B --- C
    end

    subgraph "NAT"
        drpc["drpc<br/>myapp.example.com"]
        local["localhost:15000"]
        drpc --> local
    end

    User -->|"HTTP 요청"| C
    drpc -->|"outbound TCP :9001"| A

    linkStyle 0,1 stroke:#4a9eff,stroke-width:2px,stroke-dasharray:5
```

### 노드 상세

| 노드 | 위치 | HTTP 포트 | 제어 포트 | local_map | 역할 |
|------|------|----------|----------|-----------|------|
| drps-A | Public | :8001 | :9001 | myapp ✓ | drpc 연결됨, 서비스 보유 |
| drps-B | Public | :8002 | :9002 | (empty) | 중간 relay hop |
| drps-C | Public | :8003 | :9003 | (empty) | User 요청 수신 |
| drpc | NAT | - | - | - | outbound로 drps-A에 연결 |
| local | NAT | - | :15000 | - | 실제 서비스 (http.server) |

### 연결 관계

| 연결 | 방향 | 프로토콜 | 용도 |
|------|------|---------|------|
| User → drps-C | inbound | HTTP | 서비스 요청 |
| drpc → drps-A | outbound | TLV+JSON | 로그인, 서비스 등록, work conn |
| drps-A — drps-B | 양방향 | TLV+JSON (mesh) | WhoHas/IHave, relay |
| drps-B — drps-C | 양방향 | TLV+JSON (mesh) | WhoHas/IHave, relay |
| drpc → localhost | outbound | TCP | 로컬 서비스 프록시 |

## 요청 처리 3가지 경로

```mermaid
flowchart TD
    User["User<br/>curl -H Host: myapp"]

    subgraph "drps 서버"
        Check{"local_map에<br/>hostname 있나?"}
        Local["로컬 히트<br/>→ work conn 요청"]
        Broadcast["mesh broadcast<br/>WhoHas 전파"]
    end

    Found{"IHave<br/>응답 수신?"}
    Relay["relay 연결"]
    Fail["502 Bad Gateway"]
    OK["200 OK"]

    User --> Check
    Check -->|"있음"| Local
    Check -->|"없음"| Broadcast
    Local --> OK
    Broadcast --> Found
    Found -->|"있음"| Relay
    Found -->|"없음 (3초 timeout)"| Fail
    Relay --> OK
```

| 경로 | 조건 | 테스트 |
|------|------|--------|
| 로컬 히트 | drpc가 이 서버에 연결됨 | H1 |
| relay | 다른 서버에 연결됨 → mesh로 찾아서 relay | H2, H3 |
| 실패 | 어디에도 없음 → broadcast timeout | F1 |

## 포트 구조

각 drps는 2개 포트를 사용한다:

| 포트 | 용도 | 수신 대상 | 라우팅 기준 |
|------|------|----------|------------|
| HTTP (:8001) | User HTTP 요청 수신 | 외부 User | Host 헤더 |
| Control (:9001) | drpc 로그인, mesh peer, work conn, relay | drpc / 다른 drps | 첫 TLV 메시지 타입 |

## 파일 의존성

```mermaid
graph BT
    protocol["protocol.py<br/>stdlib only"]
    mesh["mesh.py"]
    drpc["drpc.py"]
    drps["drps.py"]

    mesh --> protocol
    drpc --> protocol
    drps --> protocol
    drps --> mesh
```
