package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kubecost/disk-autoscaler/pkg/diskscaler"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	envPrefix    = "das"
	logLevelConf = "log_level"
)

func initK8sRest() (*rest.Config, error) {
	// Try to acquire an in-cluster config first
	if config, err := rest.InClusterConfig(); err == nil {
		log.Info().
			Msgf("Determined to be running in a cluster. Using in-cluster K8s config.")
		return config, nil
	} else {
		log.Info().
			Err(err).
			Msgf("Attempting in-cluster K8s config failed, falling back to local config.")
		var config *rest.Config
		envKubeconfig := viper.GetString("kubeconfig")
		if envKubeconfig != "" {
			log.Info().Msg("kubeconfig set via environment variable attempting to create k8s config")
			config, err = clientcmd.BuildConfigFromFlags("", envKubeconfig)
			if err != nil {
				return nil, fmt.Errorf("building K8s rest config for kubeconfig at path '%s': %s", envKubeconfig, err)
			}
			log.Info().
				Msgf("Built K8s config from env variable: %s", envKubeconfig)
		} else {

			homeDir, err := os.UserHomeDir()
			if err != nil {
				return nil, fmt.Errorf("getting home dir: %s", err)
			}
			kubeconfigPath := filepath.Join(homeDir, ".kube", "config")
			log.Debug().
				Str("path", kubeconfigPath).
				Msgf("Attempting local kubeconfig lookup")

			config, err = clientcmd.BuildConfigFromFlags("", kubeconfigPath)
			if err != nil {
				return nil, fmt.Errorf("building K8s rest config for kubeconfig at path '%s': %s", kubeconfigPath, err)
			}
			log.Info().
				Str("path", kubeconfigPath).
				Msgf("Built K8s config from local config")
		}

		return config, nil
	}
}

func main() {
	zerolog.TimeFieldFormat = time.RFC3339
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.RFC3339})

	viper.SetEnvPrefix(envPrefix)
	viper.SetEnvKeyReplacer(strings.NewReplacer("-", "_"))
	viper.AutomaticEnv()

	pflag.CommandLine.AddGoFlagSet(flag.CommandLine)
	pflag.Parse()
	viper.BindPFlags(pflag.CommandLine)

	pflag.CommandLine.AddGoFlagSet(flag.CommandLine)
	pflag.Parse()
	viper.BindPFlags(pflag.CommandLine)

	zerolog.SetGlobalLevel(zerolog.InfoLevel)
	if logLevelStr := viper.GetString(logLevelConf); len(logLevelStr) > 0 {
		logLevel, err := zerolog.ParseLevel(logLevelStr)
		if err != nil {
			zerolog.SetGlobalLevel(zerolog.InfoLevel)
			log.Warn().Msgf("Error parsing log level, setting level to 'info'. Err: %s", err)
		} else {
			zerolog.SetGlobalLevel(logLevel)
		}
	}

	k8sRest, err := initK8sRest()
	if err != nil {
		log.Fatal().Err(err).Msgf("Failed to set up K8s REST config")
	}

	baseK8sClient, err := kubernetes.NewForConfig(k8sRest)
	if err != nil {
		log.Fatal().Err(err).Msgf("Failed to build K8s client")
	}

	dynamicK8sClient, err := dynamic.NewForConfig(k8sRest)
	if err != nil {
		log.Fatal().Err(err).Msgf("Failed to build dynamic K8s client for custom resource modifications")
	}

	mux := http.NewServeMux()

	err = diskscaler.Setup(mux, k8sRest, baseK8sClient, dynamicK8sClient)
	if err != nil {
		log.Error().Err(err).Msgf("Kubescaler setup failed")
	}
	log.Fatal().Msgf("Disk Auto Scaler ListenAndServe: %s", http.ListenAndServe(":9730", mux))
}
