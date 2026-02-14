# ADR-004: Redis Distributed State

## 상태
Accepted

## 컨텍스트

frp는 모든 상태(라우팅 테이블, 연결 풀, 인증 정보)를 in-memory로 관리한다:
- `ControlManager` — 제어 연결 관리
- `proxy.Manager` — 프록시 등록
- `workConnCh` — work connection 채널

이 구조에서는 서버를 여러 대로 수평 확장할 수 없다. 한 서버에 등록된 프록시 정보를 다른 서버가 알 방법이 없다.

drp는 분산 우선 설계를 목표로 하므로, 모든 노드가 공유할 수 있는 외부 상태 저장소가 필요하다.

## 결정

**Redis를 공유 상태 저장소로 사용한다.**

### 저장 데이터

```
# 라우트 등록
HSET route:myapp.example.com
     alias   "myapp"
     node    "drps-A"
     run_id  "run-a1b2c3"

# 노드 heartbeat
SET node:drps-A "10.0.0.10:9000" EX 30

# API Key + ACL
HSET apikey:sk-abc123
     aliases   "myapp,myapi"
     hostnames "*.myteam.example.com"
     created_at "2025-01-01T00:00:00Z"
```

### 라우팅 흐름

1. 사용자 요청 → drps-B가 Host/SNI에서 hostname 추출
2. Redis에서 `route:{hostname}` 조회 → 등록된 노드 확인
3. 로컬 노드면 로컬 work conn pool에서 획득
4. 원격 노드면 :9000 relay로 해당 노드에 work conn 요청

```go
func (s *Server) getWorkConn(route *RouteInfo) (net.Conn, error) {
    if route.Node == s.nodeID {
        return s.localPool.Get(route.Alias)  // 로컬
    }
    return s.cluster.RequestWorkConn(route.Node, route.Alias)  // 원격
}
```

### 노드 관리

```
추가: WG peer 추가 → drp server 시작 → Redis 자동 등록 → LB에 추가 → 즉시 가동
제거: LB에서 제거 → 프로세스 종료 → TTL 만료로 자동 정리 → drpc 재연결
```

### 장애 처리

| 장애 | 대응 |
|------|------|
| drps 노드 장애 | TTL 만료 → route 자동 정리, drpc 다른 노드로 재연결, LB health check |
| Redis 장애 | Sentinel/Cluster HA, 기존 연결 유지, 신규 등록만 불가 |
| drpc 장애 | heartbeat timeout → route 정리, 재시작 시 재연결 + 재등록 |

## 결과

### 장점
- 모든 노드가 동일한 라우팅 테이블을 공유
- 노드 추가/제거가 자동화 (TTL 기반)
- 운영 경험 풍부한 기술 스택
- 충분한 성능 (라우팅 조회는 단순 HGET)
- Sentinel/Cluster로 HA 구성 가능

### 단점
- Redis 인프라 의존 (별도 운영 필요)
- Redis 장애 시 신규 프록시 등록 불가
- 네트워크 지연 추가 (로컬 조회 대비)

### 대안 (선택하지 않음)

| 대안 | 미선택 이유 |
|------|------------|
| etcd | 분산 합의가 필요 없는 간단한 K/V 사용에 과한 복잡도 |
| Consul | Service mesh 기능이 과함. Redis로 충분 |
| In-memory + Gossip | 구현 복잡도 높음. 일관성 보장 어려움 |
| PostgreSQL | 라우팅 조회에 RDBMS는 과함. TTL 기반 정리에 부적합 |

## 참고 자료
- [Redis](https://redis.io/)
- drp CONTEXT.md §7
- [ADR-001](./001-minimalist-architecture.md)
