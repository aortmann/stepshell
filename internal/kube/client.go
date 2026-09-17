// Package kube builds impersonated Kubernetes clients. The server's own
// ServiceAccount holds only impersonate rights; every user action runs under
// rest.ImpersonationConfig so the cluster's RBAC — and its audit log — see the
// real person.
package kube

import (
	"fmt"

	"github.com/strikesecurity/stepshell/internal/config"
	"github.com/strikesecurity/stepshell/internal/session"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// Factory produces impersonated clients from a base rest.Config.
type Factory struct {
	base        *rest.Config
	userPrefix  string
	groupPrefix string
}

// NewFactory loads the base config (in-cluster when kubeconfig is empty) and
// returns a Factory.
func NewFactory(cfg *config.Config) (*Factory, error) {
	base, err := loadBaseConfig(cfg.Kubeconfig)
	if err != nil {
		return nil, err
	}
	// We add impersonation per request, so the base config must not carry any.
	base.Impersonate = rest.ImpersonationConfig{}
	return &Factory{
		base:        base,
		userPrefix:  cfg.Impersonate.UserPrefix,
		groupPrefix: cfg.Impersonate.GroupPrefix,
	}, nil
}

func loadBaseConfig(kubeconfig string) (*rest.Config, error) {
	if kubeconfig == "" {
		cfg, err := rest.InClusterConfig()
		if err != nil {
			return nil, fmt.Errorf("in-cluster config: %w", err)
		}
		return cfg, nil
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("kubeconfig %q: %w", kubeconfig, err)
	}
	return cfg, nil
}

// impersonatedConfig returns a copy of the base config carrying id's
// impersonation headers (with configured prefixes applied).
func (f *Factory) impersonatedConfig(id session.Identity) *rest.Config {
	cfg := rest.CopyConfig(f.base)
	cfg.Impersonate = rest.ImpersonationConfig{
		UserName: f.userPrefix + id.User,
		Groups:   f.prefixGroups(id.Groups),
	}
	return cfg
}

func (f *Factory) prefixGroups(groups []string) []string {
	if f.groupPrefix == "" || len(groups) == 0 {
		return groups
	}
	out := make([]string, len(groups))
	for i, g := range groups {
		out[i] = f.groupPrefix + g
	}
	return out
}

// Clients bundles the typed and dynamic clients for one impersonated identity,
// plus the rest.Config needed to build exec streams.
type Clients struct {
	Typed    kubernetes.Interface
	Dynamic  dynamic.Interface
	RESTConf *rest.Config
}

// For returns clients that act as id.
func (f *Factory) For(id session.Identity) (*Clients, error) {
	cfg := f.impersonatedConfig(id)
	typed, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("typed client: %w", err)
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("dynamic client: %w", err)
	}
	return &Clients{Typed: typed, Dynamic: dyn, RESTConf: cfg}, nil
}
