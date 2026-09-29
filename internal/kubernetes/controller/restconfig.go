package controller

import (
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// configError is the only error LoadRESTConfig returns. Its text is fixed so a
// kubeconfig's content, credentials and file path never reach a log line; the
// cause is reachable through Unwrap only.
type configError struct{ cause error }

func (*configError) Error() string   { return "controller: cannot load REST config" }
func (e *configError) Unwrap() error { return e.cause }

// LoadRESTConfig returns the in-cluster config when kubeconfigPath is empty and
// the config of that kubeconfig file otherwise. QPS, Burst and Timeout that are
// zero become 50, 100 and 30s.
func LoadRESTConfig(kubeconfigPath string) (*rest.Config, error) {
	var (
		cfg *rest.Config
		err error
	)
	if kubeconfigPath == "" {
		cfg, err = rest.InClusterConfig()
	} else {
		cfg, err = clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	}
	if err != nil {
		return nil, &configError{cause: err}
	}
	applyRESTDefaults(cfg)
	return cfg, nil
}
