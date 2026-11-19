// Package kube 提供最小化的 Kubernetes REST 客户端。
// 刻意不依赖 client-go：这里只需要 pods / events / chaos CRD 的读写，
// 直接用 net/http 反而让数据链路一目了然，适合作为教学与演练用途。
package kube

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config 是从 kubeconfig 解析出来的连接参数。
type Config struct {
	Server     string
	CAData     []byte
	CertData   []byte
	KeyData    []byte
	Token      string
	Namespace  string
}

// kubeconfig 的 YAML 结构（只取需要的字段）。
type kubeconfig struct {
	CurrentContext string `yaml:"current-context"`
	Clusters []struct {
		Name    string `yaml:"name"`
		Cluster struct {
			Server                   string `yaml:"server"`
			CertificateAuthorityData string `yaml:"certificate-authority-data"`
			InsecureSkipTLSVerify    bool   `yaml:"insecure-skip-tls-verify"`
		} `yaml:"cluster"`
	} `yaml:"clusters"`
	Users []struct {
		Name string `yaml:"name"`
		User struct {
			Token               string `yaml:"token"`
			ClientCertificateData string `yaml:"client-certificate-data"`
			ClientKeyData         string `yaml:"client-key-data"`
		} `yaml:"user"`
	} `yaml:"users"`
	Contexts []struct {
		Name    string `yaml:"name"`
		Context struct {
			Cluster   string `yaml:"cluster"`
			User      string `yaml:"user"`
			Namespace string `yaml:"namespace"`
		} `yaml:"context"`
	} `yaml:"contexts"`
}

// LoadConfig 从路径加载 kubeconfig。path 为空时依次尝试
// $KUBECONFIG 和 ~/.kube/config。
func LoadConfig(path string) (*Config, error) {
	if path == "" {
		path = os.Getenv("KUBECONFIG")
	}
	if path == "" {
		home, _ := os.UserHomeDir()
		path = home + "/.kube/config"
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取 kubeconfig %q 失败: %w", path, err)
	}
	return ParseConfig(raw)
}

// ParseConfig 解析 kubeconfig 内容，提取 current-context 对应的凭据。
func ParseConfig(raw []byte) (*Config, error) {
	var kc kubeconfig
	var err error
	if err = yaml.Unmarshal(raw, &kc); err != nil {
		return nil, fmt.Errorf("解析 kubeconfig 失败: %w", err)
	}
	if kc.CurrentContext == "" {
		return nil, fmt.Errorf("kubeconfig 缺少 current-context")
	}

	var clusterName, userName, namespace string
	for _, c := range kc.Contexts {
		if c.Name == kc.CurrentContext {
			clusterName = c.Context.Cluster
			userName = c.Context.User
			namespace = c.Context.Namespace
			break
		}
	}
	if clusterName == "" {
		return nil, fmt.Errorf("current-context %q 没有对应 cluster", kc.CurrentContext)
	}

	cfg := &Config{Namespace: namespace}
	if cfg.Namespace == "" {
		cfg.Namespace = "default"
	}
	for _, c := range kc.Clusters {
		if c.Name == clusterName {
			cfg.Server = strings.TrimRight(c.Cluster.Server, "/")
			if c.Cluster.CertificateAuthorityData != "" {
				cfg.CAData, err = base64.StdEncoding.DecodeString(c.Cluster.CertificateAuthorityData)
				if err != nil {
					return nil, fmt.Errorf("解码 CA 证书失败: %w", err)
				}
			}
			break
		}
	}
	if cfg.Server == "" {
		return nil, fmt.Errorf("找不到 cluster %q 的 server 地址", clusterName)
	}
	for _, u := range kc.Users {
		if u.Name == userName {
			cfg.Token = u.User.Token
			if u.User.ClientCertificateData != "" {
				cfg.CertData, err = base64.StdEncoding.DecodeString(u.User.ClientCertificateData)
				if err != nil {
					return nil, fmt.Errorf("解码客户端证书失败: %w", err)
				}
			}
			if u.User.ClientKeyData != "" {
				cfg.KeyData, err = base64.StdEncoding.DecodeString(u.User.ClientKeyData)
				if err != nil {
					return nil, fmt.Errorf("解码客户端私钥失败: %w", err)
				}
			}
			break
		}
	}
	if cfg.Token == "" && (len(cfg.CertData) == 0 || len(cfg.KeyData) == 0) {
		return nil, fmt.Errorf("user %q 既没有 token 也没有客户端证书", userName)
	}
	return cfg, nil
}

// Client 是带 TLS 凭据的 Kubernetes REST 客户端。
type Client struct {
	cfg    *Config
	http   *http.Client
}

// NewClient 根据配置构造客户端。
func NewClient(cfg *Config) (*Client, error) {
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if len(cfg.CAData) > 0 {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(cfg.CAData) {
			return nil, fmt.Errorf("CA 证书不是有效 PEM")
		}
		tlsCfg.RootCAs = pool
	} else {
		// kind 的 kubeconfig 通常带 CA；不带时只能跳过校验（本地演练可接受）
		tlsCfg.InsecureSkipVerify = true
	}
	if len(cfg.CertData) > 0 && len(cfg.KeyData) > 0 {
		cert, err := tls.X509KeyPair(cfg.CertData, cfg.KeyData)
		if err != nil {
			return nil, fmt.Errorf("加载客户端证书失败: %w", err)
		}
		tlsCfg.Certificates = []tls.Certificate{cert}
	}
	return &Client{
		cfg: cfg,
		http: &http.Client{
			Timeout:   15 * time.Second,
			Transport: &http.Transport{TLSClientConfig: tlsCfg},
		},
	}, nil
}

// Config 返回底层配置（用于暴露 namespace 等）。
func (c *Client) Config() *Config { return c.cfg }

// Do 发起带认证的 API 请求。path 是相对 API server 的路径，如 /api/v1/pods。
func (c *Client) Do(method, path string, body io.Reader) ([]byte, error) {
	url := c.cfg.Server + path
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, err
	}
	if c.cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 %s %s 失败: %w", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("API %s %s 返回 %d: %s", method, path, resp.StatusCode, string(data))
	}
	return data, nil
}

// GetJSON 发起 GET 并把响应反序列化到 out。
func (c *Client) GetJSON(path string, out interface{}) error {
	data, err := c.Do(http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

// Version 探测集群版本，用于连通性检查。
func (c *Client) Version() (string, error) {
	var v struct {
		GitVersion string `json:"gitVersion"`
	}
	if err := c.GetJSON("/version", &v); err != nil {
		return "", err
	}
	if v.GitVersion == "" {
		return "", fmt.Errorf("集群未返回 gitVersion")
	}
	return v.GitVersion, nil
}
