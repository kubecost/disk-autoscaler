package diskscaler

import (
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/kubecost/disk-autoscaler/pkg/pvsizingrecommendation"
	"github.com/rs/zerolog/log"
	"github.com/spf13/viper"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

const (
	KubecostNamespace = "kubecost"
)

func Setup(mux *http.ServeMux, clientConfig *rest.Config, k8sClient kubernetes.Interface, dynamicK8sClient *dynamic.DynamicClient) error {
	costModelPath, err := getDiskScalerCostModelPath()
	if len(costModelPath) == 0 {
		return err
	}

	resizeAll := viper.GetBool("resize-all")
	if resizeAll {
		log.Warn().Msg("disk auto scaler is experimental and at this time resize-all will be overridden to false")
		resizeAll = false
	}

	// If the kubecost install is in any other namespace than namespace "kubecost"
	// must be explicitly set as an excluded namespace with the env variable DAS_EXCLUDED_NAMESPACE
	excludeNamespacesStr := viper.GetString("exclude-namespaces")
	excludedNamespaces := []string{}
	if excludeNamespacesStr != "" {
		excludedNamespaces = strings.Split(excludeNamespacesStr, ",")
	}

	log.Debug().Msgf("excludedNamespaces are: %+v", excludedNamespaces)

	if !slices.Contains(excludedNamespaces, KubecostNamespace) {
		excludedNamespaces = append(excludedNamespaces, KubecostNamespace)
	}

	recommendationSvc := pvsizingrecommendation.NewKubecostService(costModelPath)
	dss, err := NewDiskScalerService(clientConfig, k8sClient, dynamicK8sClient, resizeAll, recommendationSvc, excludedNamespaces)
	if err != nil {
		return fmt.Errorf("failed to create disk scaler service: %w", err)
	}

	err = dss.startAutomatedScaling()
	if err != nil {
		return fmt.Errorf("unable to start disk scaler service loop: %w", err)
	}
	mux.HandleFunc("/diskAutoScaler/enable", dss.enableDiskAutoScaling)
	mux.HandleFunc("/diskAutoScaler/exclude", dss.excludeDiskAutoScaling)
	return nil
}

func getDiskScalerCostModelPath() (string, error) {
	costModelPath := viper.GetString("cost-model-path")
	if len(costModelPath) == 0 {
		return "", fmt.Errorf(`a cost-model HTTP base path is required. Set with DAS_COST_MODEL_PATH Example: DAS_COST_MODEL_PATH=http://localhost:9090/model`)
	}
	return costModelPath, nil
}
