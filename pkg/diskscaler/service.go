package diskscaler

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/go-multierror"
	"github.com/kubecost/disk-autoscaler/pkg/pvsizingrecommendation"
	"github.com/rs/zerolog/log"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

type contextKey string

func (c contextKey) String() string {
	return string(c)
}

const (
	defaultInterval                     = "7h"
	defaultTargetUtilization            = "70"
	diskScalerRunContextKey             = contextKey("ds_run")
	diskScalerServiceContextKey         = contextKey("dss_run")
	diskScalerServiceAnnotateContextKey = contextKey("dss_annotate")
	AnnotationEnabled                   = "request.autodiskscaling.kubecost.com/enabled"
	AnnotationExcluded                  = "request.autodiskscaling.kubecost.com/excluded"
	AnnotationLastScaled                = "request.autodiskscaling.kubecost.com/lastScaled"
	AnnotationInterval                  = "request.autodiskscaling.kubecost.com/interval"
	AnnotationTargetUtilization         = "request.autodiskscaling.kubecost.com/targetUtilization"
	PVCAnnotationExtendBy               = "request.autodiskscaling.kubecost.com/volumeExtendedBy"
	PVCAnnotationCreatedBy              = "request.autodiskscaling.kubecost.com/volumeCreatedBy"
	DiskAutoScaler                      = "kubecost_disk_auto_scaler"
	timeFormat                          = time.RFC3339
	diskScalingDefaultInterval          = "7h"
)

type RunStatus struct {
	NumEnabled  int
	NumEligible int
	SuccessRun  int
	FailedRun   int
}

type DiskScalerDeploymentWorkload struct {
	Namespace  string `json:"namespace"`
	Deployment string `json:"deployment"`
}

type DiskScalerService struct {
	basicK8sClient         kubernetes.Interface
	ds                     *DiskScaler
	resizeAll              bool
	excludedNamespaceRegex *regexp.Regexp
}

func NewDiskScalerService(clientConfig *rest.Config, k8sClient kubernetes.Interface, dynamicK8sClient *dynamic.DynamicClient, resizeAll bool, kubecostSvc *pvsizingrecommendation.KubecostService, excludedNamespaces []string) (*DiskScalerService, error) {
	// To-DO :fill it via kubecost API
	clusterID := "localCluster"
	ds, err := NewDiskScaler(clientConfig, k8sClient, dynamicK8sClient, clusterID, kubecostSvc)
	if err != nil {
		return nil, fmt.Errorf("unable to create NewDiskScaler: %w", err)
	}

	// Support regex we convert slice of string to the compile regular expression and store it to check if namespace is satisfying the regex
	excludeNamespaceStr := strings.Join(excludedNamespaces, "|")
	regex, err := regexp.Compile(excludeNamespaceStr)
	if err != nil {
		return nil, fmt.Errorf("unable to create NewDiskScaler: %w", err)
	}
	dss := &DiskScalerService{
		basicK8sClient:         k8sClient,
		ds:                     ds,
		resizeAll:              resizeAll,
		excludedNamespaceRegex: regex,
	}
	return dss, nil
}

func (dss *DiskScalerService) getDiskScalerDeploymentWorkload(ctx context.Context, currentRun string) (RunStatus, []DiskScalerDeploymentWorkload, error) {
	status := RunStatus{}
	deploymentWorkload := []DiskScalerDeploymentWorkload{}
	deployments, err := dss.basicK8sClient.
		AppsV1().
		Deployments(""). // Empty string lists all Deployments in all Namespaces
		List(ctx, metav1.ListOptions{})
	if err != nil {
		return status, deploymentWorkload, fmt.Errorf("listing all Deployments: %s", err)
	}

	enabled := 0
	eligible := 0

	for _, deployment := range deployments.Items {
		if deployment.Status.UnavailableReplicas > 0 {
			continue
		}
		if !dss.workloadIsEnabled(deployment.ObjectMeta) {
			continue
		}
		enabled += 1
		if !dss.workloadIsEligible(deployment.ObjectMeta, currentRun) {
			continue
		}
		eligible += 1
		deploymentWorkload = append(deploymentWorkload, DiskScalerDeploymentWorkload{
			Namespace:  deployment.Namespace,
			Deployment: deployment.Name,
		})
	}
	status.NumEnabled = enabled
	status.NumEligible = eligible
	return status, deploymentWorkload, nil
}

func (dss *DiskScalerService) run(diskAutoScalerRun string) (RunStatus, error) {
	status := RunStatus{}
	serviceCtx := context.Background()
	getDeploymentContext, cancel := context.WithTimeout(context.WithValue(serviceCtx, diskScalerServiceContextKey, "getDeployment"), 60*time.Second)
	defer cancel()

	status, deploymentWorkload, err := dss.getDiskScalerDeploymentWorkload(getDeploymentContext, diskAutoScalerRun)
	if err != nil {
		return RunStatus{}, fmt.Errorf("failed to get deployment workload: %s", err)
	}

	status.NumEligible = len(deploymentWorkload)
	if len(deploymentWorkload) == 0 {
		return status, nil
	}

	log.Debug().Msgf("length of valid candidates for run at: %s are: %d", diskAutoScalerRun, len(deploymentWorkload))
	log.Debug().Msgf("deployment workload: %+v", deploymentWorkload)
	var result error

	var wg sync.WaitGroup
	for _, workload := range deploymentWorkload {
		wg.Add(1)
		ctx := context.Background()
		ctx = context.WithValue(ctx, diskScalerRunContextKey, fmt.Sprintf("%s:%s", workload.Namespace, workload.Deployment))

		go func(workload DiskScalerDeploymentWorkload) {
			defer wg.Done()
			err := dss.ds.runDiskScalingWorkflow(ctx, workload.Namespace, workload.Deployment)
			if err != nil {
				result = multierror.Append(result, err)
				status.FailedRun += 1
				return
			}
			status.SuccessRun += 1
		}(workload)

	}
	wg.Wait()
	log.Info().Msgf("disk autoscaling run at : %s had success: %d failed: %d", diskAutoScalerRun, status.SuccessRun, status.FailedRun)
	if result != nil {
		log.Error().Msgf("disk autoscaling run at : %s errors: %s", diskAutoScalerRun, result.Error())
	}
	return status, nil
}

func (dss *DiskScalerService) startAutomatedScaling() error {
	log.Info().Msgf("Starting automated disk scaling loop every hour")
	ticker := time.NewTicker(1 * time.Hour)
	lastRunFailed := false

	go func() {
		for t := time.Now(); ; t = <-ticker.C {
			diskAutoScalerRun := t.Format(timeFormat)
			status, err := dss.run(diskAutoScalerRun)
			if err != nil {
				if lastRunFailed {
					log.Error().
						Err(err).
						Msgf("Run loop attempt failed consecutively at time: %s", diskAutoScalerRun)
				} else {
					log.Error().
						Err(err).
						Msgf("Run loop attempt failed at time %s", diskAutoScalerRun)
				}
				lastRunFailed = true
				continue
			}
			lastRunFailed = false

			log.Debug().Msgf("status at %s :%+v, triggered the disk scaling", diskAutoScalerRun, status)
			if status.NumEnabled == 0 {
				log.Debug().Msgf("No workloads have autoscaling enabled at %s", diskAutoScalerRun)
			}
			if status.NumEligible == 0 {
				log.Debug().Msgf("No workload with autoscaling enabled can be resized again yet at %s", diskAutoScalerRun)
			}
		}
	}()
	return nil
}

// Using objectMeta keeps this generic regardless of the underlying workload
// type we're considering.
func (dss *DiskScalerService) workloadIsEnabled(meta metav1.ObjectMeta) bool {
	// For safety while this feature is early, avoid resizing kube-system
	// automatically.
	if meta.Namespace == "kube-system" {
		return false
	}

	if dss.excludedNamespaceRegex != nil && dss.excludedNamespaceRegex.MatchString(meta.Namespace) {
		return false
	}

	if dss.resizeAll {
		if val := meta.Annotations[AnnotationExcluded]; val != "true" {
			return true
		}
	}

	// if annotation excluded is set to true, it is excluded from disk autoscaling!
	if val := meta.Annotations[AnnotationExcluded]; val == "true" {
		return false
	}

	// if annotation enabled is set to true and is true
	// it is included in the disk scaling process!
	if val := meta.Annotations[AnnotationEnabled]; val == "true" {
		return true
	}

	return false
}

// workloadIsEligible classifies the workload as either eligible or
// non eligible based on the timestamp request.autodiskscaling.kubecost.com/lastScaling
// being over 7 hrs ago. Reason for choosing 7 hours is that volume
// expansion in AWS is not allowed for a span of 6 hours!
func (dss *DiskScalerService) workloadIsEligible(meta metav1.ObjectMeta, currentRun string) bool {
	// For safety while this feature is early, avoid resizing kube-system
	// automatically.
	if meta.Namespace == "kube-system" {
		return false
	}

	currentTime, err := time.Parse(time.RFC3339, currentRun)
	if err != nil {
		return false
	}

	// seen for the first time
	if val := meta.Annotations[AnnotationLastScaled]; val == "" {
		return true
	}

	// check last scaled and compare it with current time and addition of the interval time!
	if val := meta.Annotations[AnnotationLastScaled]; val != "" {
		lastScaledTime, err := time.Parse(time.RFC3339, val)
		if err != nil {
			return false
		}
		interval := meta.Annotations[AnnotationInterval]
		if interval == "" {
			interval = diskScalingDefaultInterval
		}
		intervalDuration, err := time.ParseDuration(interval)
		if err != nil {
			return false
		}
		if lastScaledTime.Before(currentTime.Add(-intervalDuration)) {
			return true
		}
	}

	return false
}

func (dss *DiskScalerService) enableDeployment(ctx context.Context, namespace string, deployment string, interval string, targetUtilization string) error {
	// For safety while this feature is early, avoid resizing kube-system

	if namespace == "kube-system" {
		return fmt.Errorf("namespace %s is not eligible for disk auto scaling", "kube-system")
	}

	if dss.excludedNamespaceRegex != nil && dss.excludedNamespaceRegex.MatchString(namespace) {
		return fmt.Errorf("namespace %s is not eligible for disk auto scaling", namespace)
	}

	k8sDep, err := dss.basicK8sClient.
		AppsV1().
		Deployments(namespace).
		Get(ctx, deployment, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("namespace %s, deployment: %s failed to get deployment object: %v", namespace, deployment, err)
	}

	currAnnotation := k8sDep.GetAnnotations()
	currAnnotation[AnnotationEnabled] = "true"
	currAnnotation[AnnotationInterval] = interval
	currAnnotation[AnnotationTargetUtilization] = targetUtilization
	k8sDep.SetAnnotations(currAnnotation)
	_, err = dss.basicK8sClient.AppsV1().Deployments(namespace).Update(ctx, k8sDep, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("annotating deployment with disk auto scaler annotation failed with err: %w", err)
	}

	log.Info().Msgf("successfully annotated deployment %s", deployment)
	return nil
}

func (dss *DiskScalerService) excludeDeployment(ctx context.Context, namespace string, deployment string) error {
	// For safety while this feature is early, avoid resizing kube-system

	if namespace == "kube-system" {
		return fmt.Errorf("namespace %s is not eligible for disk auto scaling", "kube-system")
	}

	if dss.excludedNamespaceRegex != nil && dss.excludedNamespaceRegex.MatchString(namespace) {
		return fmt.Errorf("namespace %s is not eligible for disk auto scaling", namespace)
	}

	k8sDep, err := dss.basicK8sClient.
		AppsV1().
		Deployments(namespace).
		Get(ctx, deployment, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("namespace %s, deployment: %s failed to get deployment object: %v", namespace, deployment, err)
	}

	currAnnotation := k8sDep.GetAnnotations()
	currAnnotation[AnnotationExcluded] = "true"
	k8sDep.SetAnnotations(currAnnotation)
	_, err = dss.basicK8sClient.AppsV1().Deployments(namespace).Update(ctx, k8sDep, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("annotating deployment with disk auto scaler annotation failed with err: %w", err)
	}

	log.Info().Msgf("successfully annotated deployment %s", deployment)
	return nil
}
