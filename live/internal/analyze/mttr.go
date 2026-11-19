// Package analyze 把原始事件流聚合成演练结果指标。
// 这些函数同时被真实模式和单元测试使用，保证口径一致。
package analyze

import "time"

// Event 是一条带时间戳的观测事件。
type Event struct {
	At       time.Time `json:"at"`
	Type     string    `json:"type"`     // info / warn / error / ok / step
	Message  string    `json:"message"`
	Phase    string    `json:"phase"`    // inject / detect / mitigate / recover
	Ready    int       `json:"ready"`    // 当时的就绪副本数
	Total    int       `json:"total"`
}

// Result 是聚合后的结果指标。
type Result struct {
	Detected     bool          `json:"detected"`
	DetectLatency time.Duration `json:"detectLatency"`
	MTTR         time.Duration `json:"mttr"`
	Recovered    bool          `json:"recovered"`
	ImpactPods   int           `json:"impactPods"`
	MinReady     int           `json:"minReady"`
	Events       int           `json:"events"`
}

// Analyze 从事件序列计算指标。
//
// 口径：
//   - 检测延迟 = 第一条 detect 事件时间 - 最后一条 inject 事件时间
//   - MTTR = 第一条 recover/ok(phase=recover) 事件时间 - 最后一条 inject 时间
//     取「最后一条」是因为基线采样也可能带 inject 语义，真正的注入点是故障落地的那一刻
//   - 影响副本 = inject 时的就绪数 - 序列中最小就绪数
//   - 恢复判定 = 最后一条事件的就绪数回到 inject 时的水平
func Analyze(events []Event) Result {
	r := Result{Events: len(events)}
	if len(events) == 0 {
		return r
	}

	var injectAt, detectAt, recoverAt time.Time
	minReady := events[0].Total + 1
	baseReady := events[0].Ready

	for _, e := range events {
		if e.Total > 0 && e.Ready < minReady {
			minReady = e.Ready
		}
		if e.Phase == "inject" {
			// 持续覆盖，最终保留最后一条 inject（故障真正落地的时刻）
			injectAt = e.At
			baseReady = e.Ready
		}
		if detectAt.IsZero() && e.Phase == "detect" {
			detectAt = e.At
			r.Detected = true
		}
		if recoverAt.IsZero() && e.Phase == "recover" {
			recoverAt = e.At
			r.Recovered = true
		}
	}

	if minReady > events[0].Total {
		minReady = events[0].Ready
	}
	r.MinReady = minReady
	if baseReady > minReady {
		r.ImpactPods = baseReady - minReady
	}
	if !injectAt.IsZero() {
		if !detectAt.IsZero() && detectAt.After(injectAt) {
			r.DetectLatency = detectAt.Sub(injectAt)
		}
		if !recoverAt.IsZero() && recoverAt.After(injectAt) {
			r.MTTR = recoverAt.Sub(injectAt)
		}
	}
	// 最终恢复态以最后一条事件为准
	if n := len(events); n > 0 {
		r.Recovered = r.Recovered || events[n-1].Ready >= baseReady
	}
	return r
}
