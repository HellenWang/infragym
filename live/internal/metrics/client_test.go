package metrics

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientQueryParsesVector(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("query"); got == "" {
			t.Error("query 参数为空")
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"success","data":{"resultType":"vector","result":[{"metric":{"pod":"n1"},"value":[1700000000,"6"]}]}}`)
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	v, err := c.Query(`sum(up)`)
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if len(v.Result) != 1 {
		t.Fatalf("结果数量 = %d, want 1", len(v.Result))
	}
	if v.Result[0].Value() != 6 {
		t.Errorf("值 = %v, want 6", v.Result[0].Value())
	}
}

func TestClientQueryHandlesErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"error","error":"bad promql"}`)
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	if _, err := c.Query(`invalid(`); err == nil {
		t.Error("Prometheus 返回 error 状态时应报错")
	}
}

func TestCollectorSnapshotUsesRealPromQL(t *testing.T) {
	var queries []string
	q := &recordingQueryer{out: &Vector{ResultType: "vector", Result: []Sample{
		{SampleValue: [2]interface{}{float64(1700000000), "4"}},
	}}}
	_ = queries
	c := NewCollector(q, "infragym-demo", "demo")
	m, err := c.Snapshot()
	if err != nil {
		t.Fatalf("采集失败: %v", err)
	}
	if m.ReadyPods != 4 || m.TotalPods != 4 {
		t.Errorf("ready/total = %d/%d", m.ReadyPods, m.TotalPods)
	}
	if m.Availability() != 100 {
		t.Errorf("可用性 = %v, want 100", m.Availability())
	}
	if len(q.got) < 4 {
		t.Errorf("应发起至少 4 条 PromQL 查询, got %d", len(q.got))
	}
	joined := ""
	for _, s := range q.got {
		joined += s
	}
	if !contains(joined, "kube_deployment_status_replicas_ready") {
		t.Error("缺少就绪副本查询")
	}
	if !contains(joined, "histogram_quantile") {
		t.Error("缺少 API 延迟 P99 查询")
	}
}

type recordingQueryer struct {
	got []string
	out *Vector
}

func (r *recordingQueryer) Query(promQL string) (*Vector, error) {
	r.got = append(r.got, promQL)
	return r.out, nil
}

func TestAvailabilityEdgeCases(t *testing.T) {
	m := &Metrics{TotalPods: 0, ReadyPods: 0}
	if m.Availability() != 100 {
		t.Error("零副本时可用性应为 100")
	}
	m = &Metrics{TotalPods: 10, ReadyPods: 7}
	if m.Availability() != 70 {
		t.Errorf("可用性 = %v, want 70", m.Availability())
	}
}
