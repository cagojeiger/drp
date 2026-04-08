package server

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/kangheeyong/drp/internal/pool"
)

type Snapshot struct {
	Timestamp   time.Time           `json:"timestamp"`
	ReqWorkConn ReqWorkConnSnapshot `json:"req_work_conn"`
	Pool        pool.AggregateStats `json:"pool"`
}

func MetricsHandler(reqStats *ReqWorkConnStats, poolAgg func() pool.AggregateStats) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		s := Snapshot{
			Timestamp: time.Now().UTC(),
		}
		if reqStats != nil {
			s.ReqWorkConn = reqStats.Snapshot()
		}
		if poolAgg != nil {
			s.Pool = poolAgg()
		}
		_ = json.NewEncoder(w).Encode(s)
	}
}
