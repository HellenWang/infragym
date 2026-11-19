// Package metrics 查询 Prometheus / kube-state-metrics 获取真实可观测信号。
// 所有查询都是标准 PromQL，用户在自己的 Prometheus 里也能直接跑。
package metrics

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Queryer 是 Prometheus HTTP API 的查询接口。
type Queryer interface {
	Query(promQL string) (*Vector, error)
}

// Vector 是 instant query 的结果向量。
type Vector struct {
	ResultType string   `json:"resultType"`
	Result     []Sample `json:"result"`
}

// Sample 是单个时间序列的瞬时值。
type Sample struct {
	Metric map[string]string `json:"metric"`
	SampleValue [2]interface{} `json:"value"` // [timestamp, "value"]
}

// Value 返回样本的数值，解析失败返回 0。
func (s Sample) Value() float64 {
	if len(s.SampleValue) < 2 {
		return 0
	}
	switch v := s.SampleValue[1].(type) {
	case string:
		f, _ := strconv.ParseFloat(v, 64)
		return f
	case float64:
		return v
	}
	return 0
}

// Client 是 Prometheus HTTP API 客户端。
type Client struct {
	base   string
	http   *http.Client
}

func NewClient(base string) *Client {
	return &Client{
		base: stringsTrimRight(base, "/"),
		http: &http.Client{Timeout: 10 * time.Second},
	}
}

func stringsTrimRight(s, cut string) string {
	for len(s) > 0 && len(cut) > 0 && s[len(s)-1] == cut[0] {
		s = s[:len(s)-1]
	}
	return s
}

// Query 执行 instant query。
func (c *Client) Query(promQL string) (*Vector, error) {
	u := c.base + "/api/v1/query?query=" + url.QueryEscape(promQL)
	resp, err := c.http.Get(u)
	if err != nil {
		return nil, fmt.Errorf("查询 Prometheus 失败: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Prometheus 返回 %d: %s", resp.StatusCode, string(body))
	}
	var env struct {
		Status string  `json:"status"`
		Data   Vector  `json:"data"`
		Error  string  `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("解析 Prometheus 响应失败: %w", err)
	}
	if env.Status != "success" {
		return nil, fmt.Errorf("Prometheus 查询错误: %s", env.Error)
	}
	return &env.Data, nil
}

// Metrics 是一组关键可观测信号。
type Metrics struct {
	TotalPods     int
	ReadyPods     int
	NotReadyPods  int
	RestartRate   float64
	APILatencyP99 float64
}

// Collector 从 Prometheus 采集演练关心的一组指标。
// namespace/label selector 由调用方传入，避免硬编码。
type Collector struct {
	q         Queryer
	namespace string
	appLabel  string
}

func NewCollector(q Queryer, namespace, appLabel string) *Collector {
	return &Collector{q: q, namespace: namespace, appLabel: appLabel}
}

// Queryer 返回底层查询器（用于切换到 mock 阶段等高级用法）。
func (c *Collector) Queryer() Queryer { return c.q }

// Snapshot 抓取一次指标快照。
func (c *Collector) Snapshot() (*Metrics, error) {
	m := &Metrics{}

	// kube-state-metrics：期望副本数
	totalQ := fmt.Sprintf(`sum(kube_deployment_status_replicas{namespace="%s"})`, c.namespace)
	if v, err := c.q.Query(totalQ); err == nil && len(v.Result) > 0 {
		m.TotalPods = int(v.Result[0].Value())
	}

	// 就绪副本数（真实可用信号）
	readyQ := fmt.Sprintf(`sum(kube_deployment_status_replicas_ready{namespace="%s"})`, c.namespace)
	if v, err := c.q.Query(readyQ); err == nil && len(v.Result) > 0 {
		m.ReadyPods = int(v.Result[0].Value())
	}

	// 容器重启速率：故障期间会飙升
	restartQ := fmt.Sprintf(`sum(rate(kube_pod_container_status_restarts_total{namespace="%s"}[1m]))`, c.namespace)
	if v, err := c.q.Query(restartQ); err == nil && len(v.Result) > 0 {
		m.RestartRate = v.Result[0].Value()
	}

	// API server P99 延迟：控制面健康信号
	latQ := `histogram_quantile(0.99, sum(rate(apiserver_request_duration_seconds_bucket{verb=~"GET|LIST"}[5m])) by (le))`
	if v, err := c.q.Query(latQ); err == nil && len(v.Result) > 0 {
		m.APILatencyP99 = v.Result[0].Value()
	}

	m.NotReadyPods = m.TotalPods - m.ReadyPods
	if m.NotReadyPods < 0 {
		m.NotReadyPods = 0
	}
	return m, nil
}

// Availability 返回当前可用比例（0-100）。
func (m *Metrics) Availability() float64 {
	if m.TotalPods == 0 {
		return 100
	}
	return float64(m.ReadyPods) / float64(m.TotalPods) * 100
}
