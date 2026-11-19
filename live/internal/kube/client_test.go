package kube

import (
	"encoding/base64"
	"testing"
)

func TestParseConfigWithToken(t *testing.T) {
	ca := base64.StdEncoding.EncodeToString([]byte("fake-ca"))
	yaml := `apiVersion: v1
kind: Config
current-context: kind-infragym
clusters:
- name: kind-infragym
  cluster:
    server: https://127.0.0.1:6443
    certificate-authority-data: ` + ca + `
users:
- name: kind-infragym-admin
  user:
    token: abc123token
contexts:
- name: kind-infragym
  context:
    cluster: kind-infragym
    user: kind-infragym-admin
    namespace: infragym-demo
`
	cfg, err := ParseConfig([]byte(yaml))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if cfg.Server != "https://127.0.0.1:6443" {
		t.Errorf("server = %q", cfg.Server)
	}
	if cfg.Token != "abc123token" {
		t.Errorf("token = %q", cfg.Token)
	}
	if cfg.Namespace != "infragym-demo" {
		t.Errorf("namespace = %q", cfg.Namespace)
	}
	if string(cfg.CAData) != "fake-ca" {
		t.Errorf("CA = %q", cfg.CAData)
	}
}

func TestParseConfigWithClientCert(t *testing.T) {
	cert := base64.StdEncoding.EncodeToString([]byte("CERTDATA"))
	key := base64.StdEncoding.EncodeToString([]byte("KEYDATA"))
	yaml := `apiVersion: v1
current-context: ctx
clusters:
- name: c1
  cluster:
    server: https://example.com:6443
users:
- name: u1
  user:
    client-certificate-data: ` + cert + `
    client-key-data: ` + key + `
contexts:
- name: ctx
  context:
    cluster: c1
    user: u1
`
	cfg, err := ParseConfig([]byte(yaml))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if string(cfg.CertData) != "CERTDATA" || string(cfg.KeyData) != "KEYDATA" {
		t.Errorf("证书数据解析错误: cert=%q key=%q", cfg.CertData, cfg.KeyData)
	}
	if cfg.Namespace != "default" {
		t.Errorf("默认 namespace 应为 default, got %q", cfg.Namespace)
	}
}

func TestParseConfigMissingContext(t *testing.T) {
	yaml := `apiVersion: v1
current-context: nope
clusters: []
users: []
contexts: []
`
	if _, err := ParseConfig([]byte(yaml)); err == nil {
		t.Error("缺少对应 context 应报错")
	}
}
