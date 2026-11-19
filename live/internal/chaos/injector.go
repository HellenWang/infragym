// Package chaos 通过 chaos-mesh CRD 注入与清理真实故障。
// 每个实验都用 Kubernetes 原生资源表达，可审计、可 kubectl 查看。
package chaos

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/hellenwang/infragym/live/internal/kube"
)

const apiBase = "/apis/chaos-mesh.org/v1alpha1"

// Kind 是故障类型。
type Kind string

const (
	PodKill      Kind = "PodChaos/pod-kill"
	PodFailure   Kind = "PodChaos/container-kill"
	NetDelay     Kind = "NetworkChaos/delay"
	NetLoss      Kind = "NetworkChaos/loss"
)

// Spec 描述一次故障注入的意图。
type Spec struct {
	Kind        Kind
	Namespace   string
	LabelSelector map[string]string
	Duration    time.Duration
	Latency     string // NetDelay 用，如 "200ms"
	Loss        string // NetLoss 用，如 "50"
}

// objectMeta 是 CRD 通用元数据。
type objectMeta struct {
	Name      string            `json:"name"`
	Namespace string            `json:"namespace"`
	Labels    map[string]string `json:"labels,omitempty"`
}

// podChaosSpec 是 chaos-mesh PodChaos 的 spec 子集。
type podChaosSpec struct {
	Action     string            `json:"action"`
	Mode       string            `json:"mode"`
	Selector   selector          `json:"selector"`
	Duration   string            `json:"duration,omitempty"`
	GracePeriod int               `json:"gracePeriod,omitempty"`
}

type netChaosSpec struct {
	Action   string   `json:"action"`
	Mode     string   `json:"mode"`
	Selector selector `json:"selector"`
	Delay    string   `json:"delay,omitempty"`
	Loss     string   `json:"loss,omitempty"`
	Duration string   `json:"duration,omitempty"`
}

type selector struct {
	LabelSelectors map[string]string `json:"labelSelectors,omitempty"`
	Namespaces     []string          `json:"namespaces,omitempty"`
	Pods           map[string][]string `json:"pods,omitempty"`
}

// crd 是提交给 API server 的完整资源对象。
type crd struct {
	APIVersion string      `json:"apiVersion"`
	Kind       string      `json:"kind"`
	Metadata   objectMeta  `json:"metadata"`
	Spec       interface{} `json:"spec"`
}

// Injector 负责在集群里创建/删除 chaos 资源。
type Injector struct {
	client *kube.Client
}

func NewInjector(c *kube.Client) *Injector { return &Injector{client: c} }

// Inject 创建 chaos 资源并返回资源名，后续用该名字清理。
func (i *Injector) Inject(s Spec) (string, error) {
	if s.Namespace == "" {
		s.Namespace = i.client.Config().Namespace
	}
	if len(s.LabelSelector) == 0 {
		return "", fmt.Errorf("LabelSelector 不能为空： chaos 必须限定作用范围")
	}
	if s.Duration <= 0 {
		s.Duration = 30 * time.Second
	}

	name := fmt.Sprintf("ig-%s-%d", string(s.Kind), time.Now().UnixNano())
	dur := s.Duration.String()

	var resource crd
	switch s.Kind {
	case PodKill, PodFailure:
		action := "pod-kill"
		if s.Kind == PodFailure {
			action = "container-kill"
		}
		resource = crd{
			APIVersion: "chaos-mesh.org/v1alpha1",
			Kind:       "PodChaos",
			Metadata: objectMeta{
				Name: name, Namespace: s.Namespace,
				Labels: map[string]string{
					"app.kubernetes.io/managed-by": "infragym",
					"infragym.dev/scenario":        string(s.Kind),
				},
			},
			Spec: podChaosSpec{
				Action: action, Mode: "one",
				Selector: selector{
					LabelSelectors: s.LabelSelector,
					Namespaces:     []string{s.Namespace},
				},
				Duration: dur,
			},
		}
	case NetDelay, NetLoss:
		action := "delay"
		if s.Kind == NetLoss {
			action = "loss"
		}
		resource = crd{
			APIVersion: "chaos-mesh.org/v1alpha1",
			Kind:       "NetworkChaos",
			Metadata: objectMeta{
				Name: name, Namespace: s.Namespace,
				Labels: map[string]string{
					"app.kubernetes.io/managed-by": "infragym",
					"infragym.dev/scenario":        string(s.Kind),
				},
			},
			Spec: netChaosSpec{
				Action: action, Mode: "all",
				Selector: selector{
					LabelSelectors: s.LabelSelector,
					Namespaces:     []string{s.Namespace},
				},
				Delay: s.Latency, Loss: s.Loss, Duration: dur,
			},
		}
	default:
		return "", fmt.Errorf("不支持的故障类型 %q", s.Kind)
	}

	payload, err := json.Marshal(resource)
	if err != nil {
		return "", err
	}
	plural := "podchaos"
	if s.Kind == NetDelay || s.Kind == NetLoss {
		plural = "networkchaos"
	}
	path := fmt.Sprintf("%s/namespaces/%s/%s", apiBase, s.Namespace, plural)
	_, err = i.client.Do(http.MethodPost, path, bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("注入 %s 失败: %w", s.Kind, err)
	}
	return plural + "/" + name, nil
}

// Cleanup 删除指定的 chaos 资源。resourceID 形如 podchaos/ig-xxx。
func (i *Injector) Cleanup(namespace, resourceID string) error {
	if namespace == "" {
		namespace = i.client.Config().Namespace
	}
	path := fmt.Sprintf("%s/namespaces/%s/%s", apiBase, namespace, resourceID)
	_, err := i.client.Do(http.MethodDelete, path, nil)
	if err != nil {
		return fmt.Errorf("清理 %s 失败: %w", resourceID, err)
	}
	return nil
}

// ListActive 列出当前由 infragym 管理的活跃 chaos 资源。
func (i *Injector) ListActive(namespace string) ([]string, error) {
	if namespace == "" {
		namespace = i.client.Config().Namespace
	}
	var list struct {
		Items []struct {
			Kind     string `json:"kind"`
			Metadata struct {
				Name      string            `json:"name"`
				Namespace string            `json:"namespace"`
				Labels    map[string]string `json:"labels"`
			} `json:"metadata"`
		} `json:"items"`
	}
	path := fmt.Sprintf("%s/namespaces/%s/podchaos", apiBase, namespace)
	if err := i.client.GetJSON(path, &list); err != nil {
		return nil, err
	}
	var out []string
	for _, it := range list.Items {
		if it.Metadata.Labels["app.kubernetes.io/managed-by"] == "infragym" {
			out = append(out, "podchaos/"+it.Metadata.Name)
		}
	}
	return out, nil
}
