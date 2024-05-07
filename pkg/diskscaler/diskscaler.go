package diskscaler

import (
	"bytes"
	"context"
	"fmt"
	"math/rand"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/kubecost/disk-autoscaler/pkg/pvsizingrecommendation"
	"github.com/rs/zerolog/log"
	v1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/client-go/util/retry"
	"k8s.io/kubectl/pkg/scheme"
)

var supportedSCProvisioner = []string{"ebs.csi.aws.com"}

const (
	kubecostDataMoverTransientPodName = "kubecost-data-mover-pod"
	volumeBindingWaitForFirstConsumer = "WaitForFirstConsumer"
	maxRetries                        = 3
	inactivityDuringDelay             = 10 * time.Second
	// Setting a timeout of 4 minutes on any creation or delete operation of disk scaler
	diskScalingOperationTimeout = 4 * time.Minute
)

var allowedCharactersForPVCName = []rune("abcdefghijklmnopqrstuvwxyz")

type DiskScaler struct {
	clientConfig     *rest.Config
	basicK8sClient   kubernetes.Interface
	dynamicK8sClient *dynamic.DynamicClient
	clusterID        string
	kubecostsvc      *pvsizingrecommendation.KubecostService
}

type pvcDetails struct {
	currentSize          resource.Quantity
	storageClass         string
	provisioner          string
	allowVolumeExpansion bool
	spec                 v1.PersistentVolumeClaimSpec
	resizeTo             resource.Quantity
	err                  error
	pvName               string
	resizedPVCName       string
	isSkippedForDeletion bool
}

func NewDiskScaler(clientConfig *rest.Config, basicK8sClient kubernetes.Interface, dynamicK8sClient *dynamic.DynamicClient, clusterID string, kubecostsvc *pvsizingrecommendation.KubecostService) (*DiskScaler, error) {
	if basicK8sClient == nil {
		return nil, fmt.Errorf("must have a Kubernetes client")
	}

	if dynamicK8sClient == nil {
		return nil, fmt.Errorf("disk scaler must have a dynamic client to modify custom resource")
	}

	return &DiskScaler{
		clientConfig:     clientConfig,
		basicK8sClient:   basicK8sClient,
		dynamicK8sClient: dynamicK8sClient,
		clusterID:        clusterID,
		kubecostsvc:      kubecostsvc,
	}, nil
}

// runDiskScalingWorkflow initiates a disk scaling workflow for a specific deployment in the given namespace.
func (ds *DiskScaler) runDiskScalingWorkflow(ctx context.Context, namespace, deployment string) error {
	volMap, err := ds.getPVCMap(ctx, namespace, deployment)
	if err != nil {
		return fmt.Errorf("disk scaling failed : %w", err)
	}

	log.Debug().Msgf("ctx: %s, volume map is: %+v", ctx.Value(diskScalerRunContextKey), volMap)

	originalScale, err := ds.retryscaleDeployment(ctx, deployment, namespace, 0)
	if err != nil {
		return fmt.Errorf("disk scaling failed: %w", err)
	}

	var didCopyFail bool
	// During the resize operation with multiple PVC attached to same deployment
	// we dont error out rather perform the partial operation and scale back up
	// notifying the user that errors occured.
	for name, pvcDetails := range volMap {
		if isEqualQuantity(pvcDetails.currentSize, pvcDetails.resizeTo) {
			log.Info().Msgf("ctx: %s, PVC has %s optimal storage at this time, so no action taken from disk auto scaler", ctx.Value(diskScalerRunContextKey), name)
			pvcDetails.isSkippedForDeletion = true
			continue
		}
		if pvcDetails.allowVolumeExpansion && isGreaterQuantity(pvcDetails.currentSize, pvcDetails.resizeTo) {
			log.Info().Msgf("ctx: %s, disk auto scaler is performing action to increase the volume size for pvc %s from %s to %s", ctx.Value(diskScalerRunContextKey), name, pvcDetails.currentSize.String(), pvcDetails.resizeTo.String())
			err := ds.patchPVCWithResize(ctx, namespace, name, pvcDetails.resizeTo)
			if err != nil {
				pvcDetails.err = err
			}
			pvcDetails.isSkippedForDeletion = true
		} else {
			log.Info().Msgf("ctx: %s, disk auto scaler is performing action to decrease the volume size for pvc %s from %s to %s", ctx.Value(diskScalerRunContextKey), name, pvcDetails.currentSize.String(), pvcDetails.resizeTo.String())
			// PVC name created with smaller pv is different from original pvc name
			didCopyFail = false
			newPVC, err := ds.createPVCFromASpec(ctx, namespace, name, pvcDetails.spec, pvcDetails.resizeTo, pvcDetails.resizedPVCName)
			if err != nil {
				pvcDetails.err = err
				continue
			}
			log.Debug().Msgf("ctx: %s, created pvc of name %s of size: %s", ctx.Value(diskScalerRunContextKey), newPVC.GetName(), pvcDetails.resizeTo.String())

			copierPodName := fmt.Sprintf("%s-%s", kubecostDataMoverTransientPodName, randStringRunes(5))
			err = ds.dataMoverTransientPod(ctx, namespace, copierPodName, name, pvcDetails.resizedPVCName)
			if err != nil {
				pvcDetails.err = err
				didCopyFail = true
			}

			// Always delete the transient copier pod before exiting  when copy operation failed we need to forcefully delete the copier
			err = ds.retryDeleteTransientPod(ctx, namespace, copierPodName, didCopyFail)
			if err != nil {
				pvcDetails.err = fmt.Errorf("ctx: %s, failed to delete transient pod after %d attempts, manual deletion needed err: %w", ctx.Value(diskScalerRunContextKey), maxRetries, err)
				continue
			}

			log.Debug().Msgf("ctx: %s, successfully moved data between PVC: %s to PVC: %s", ctx.Value(diskScalerRunContextKey), name, newPVC.GetName())

			// Only if copy is successful update the deployment with smaller PVC
			if !didCopyFail {
				err = ds.updateDeploymentWithSmallerSizePV(ctx, deployment, namespace, name, pvcDetails.resizedPVCName)
				if err != nil {
					pvcDetails.err = err
				}
			}
		}
	}

	err = ds.retryAnnotateDeployment(ctx, deployment, namespace)
	if err != nil {
		return fmt.Errorf("ctx: %s, disk scaling annotating deployment failed: %w", ctx.Value(diskScalerRunContextKey), err)
	}

	_, err = ds.retryscaleDeployment(ctx, deployment, namespace, originalScale)
	if err != nil {
		return fmt.Errorf("disk scaling failed: %w", err)
	}

	noOfErrors := 0
	failedPVCS := make([]string, 0)
	for pvcName, pvcDetails := range volMap {
		// Do not delete the extended volumes or volumes that don't have any action to be taken
		if pvcDetails.isSkippedForDeletion {
			continue
		}
		if pvcDetails.err != nil {
			noOfErrors += 1
			failedPVCS = append(failedPVCS, pvcName)
			log.Error().Msgf("ctx: %s, disk scaling of pvc with name: %s failed with err: %v", ctx.Value(diskScalerRunContextKey), pvcName, pvcDetails.err)
			err = ds.deletePVC(ctx, namespace, pvcDetails.resizedPVCName)
			if err != nil {
				log.Error().Msgf("ctx: %s, unable to delete PVC created in disk scaling operation: %s", ctx.Value(diskScalerRunContextKey), pvcDetails.resizedPVCName)
			}
			continue
		}
		err = ds.deletePVC(ctx, namespace, pvcName)
		if err != nil {
			log.Error().Msgf("ctx: %s, unable to delete PVC after the disk scaling operation: %s", ctx.Value(diskScalerRunContextKey), pvcName)
		}
	}

	if noOfErrors == 0 {
		return nil
	}

	if noOfErrors == len(volMap) {
		return &DiskScalingAllFailedError{namespace: namespace, deployment: deployment}
	}
	return &DiskScalingPartialFailedError{namespace: namespace, deployment: deployment, pvc: failedPVCS}
}

// retryAnnotateDeployment attempts to retry annotating the deployment in case of any infrastructure delay
func (ds *DiskScaler) retryAnnotateDeployment(ctx context.Context, deploymentName string, namespace string) error {
	var retryErr error
	for i := 0; i < maxRetries; i++ {
		retryErr = ds.annotateDeployment(ctx, deploymentName, namespace)
		if retryErr == nil {
			break
		}
		log.Debug().Msgf("ctx: %s, failed to annotate deployment with disk auto scaler annotations in %d attempt(s) with err %v", ctx.Value(diskScalerRunContextKey), i, retryErr)
		if i < maxRetries-1 {
			time.Sleep(inactivityDuringDelay)
		}
	}
	return retryErr
}

// annotateDeployment annotates the given deployment with specific annotations required for the disk autoscaler.
func (ds *DiskScaler) annotateDeployment(ctx context.Context, deploymentName string, namespace string) error {
	deployment := ds.basicK8sClient.AppsV1().
		Deployments(namespace)

	dep, err := deployment.
		Get(ctx, deploymentName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get deployment with err: %w", err)
	}
	lastScalingTime := time.Now().Format(timeFormat)
	currAnnotation := dep.GetAnnotations()
	currAnnotation[AnnotationLastScaled] = lastScalingTime
	dep.SetAnnotations(currAnnotation)
	dep, err = deployment.Update(ctx, dep, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("failed to annotate the deployment with err: %w", err)
	}
	log.Debug().Msgf("ctx: %s, successfully updated deployment %s with last scaling timestamp: %s", ctx.Value(diskScalerRunContextKey), dep.Name, lastScalingTime)
	return nil
}

// patchPVCWithResize resizes the PersistentVolumeClaim (PVC) associated
// with a PersistentVolume (PV) using a patch operation, which is faster
// than using an edit operation. It updates the size of the PVC to the
// new size specified by resizeTo.
func (ds *DiskScaler) patchPVCWithResize(ctx context.Context, namespace, pvc string, resizeTo resource.Quantity) error {
	persVolC := ds.basicK8sClient.CoreV1().PersistentVolumeClaims(namespace)

	data := fmt.Sprintf(`{ "spec": { "resources": { "requests": { "storage": "%s" }}}}`, resizeTo.String())
	updatePVC, err := persVolC.Patch(ctx, pvc, types.MergePatchType, []byte(data), metav1.PatchOptions{})
	if err != nil {
		return fmt.Errorf("unable to patch pvc: %s err: %w", pvc, err)
	}
	// To-Do: add annotation extendBy kubecot_disk_auto_scaler for the pvc in the same patch
	log.Info().Msgf("ctx: %s, updated PVC %s with size: %s", ctx.Value(diskScalerRunContextKey), updatePVC.GetName(), resizeTo.String())
	return nil
}

// updateDeploymentWithSmallerSizePV updates the Deployment's spec to use a new PVC name
// for a smaller-sized PersistentVolumeClaim (PVC).
func (ds *DiskScaler) updateDeploymentWithSmallerSizePV(ctx context.Context, deploymentName string, namespace string, oldClaimName string, newClaim string) error {
	deployment := ds.basicK8sClient.AppsV1().
		Deployments(namespace)

	retryErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// Retrieve the latest version of Deployment before attempting update
		// RetryOnConflict uses exponential backoff to avoid exhausting the apiserver
		result, getErr := deployment.Get(ctx, deploymentName, metav1.GetOptions{})
		if getErr != nil {
			return fmt.Errorf("unable to get deployment: %s err: %w", deploymentName, getErr)
		}

		volumes := result.Spec.Template.Spec.Volumes
		// Get the volume with old claim name
		var volumeToReplaceClaimName *v1.Volume
		for _, volume := range volumes {
			if volume.PersistentVolumeClaim != nil && volume.PersistentVolumeClaim.ClaimName == oldClaimName {
				volumeToReplaceClaimName = &volume
			}
		}
		volumeToReplaceClaimName.PersistentVolumeClaim.ClaimName = newClaim
		_, updateErr := deployment.Update(ctx, result, metav1.UpdateOptions{})
		return updateErr
	})
	if retryErr != nil {
		return fmt.Errorf("update failed to the deployment: %s err: %w", deploymentName, retryErr)
	}

	log.Info().Msgf("ctx: %s,successfully updated deployment with new pvc: %s", ctx.Value(diskScalerRunContextKey), newClaim)
	return nil
}

// getKubecostRecommendationForPV is used to get the recommendation from
// kubecost service for a particular pvName.
func (ds *DiskScaler) getKubecostRecommendationForPV(ctx context.Context, pvName string, targetUtilization int, interval string) (resource.Quantity, error) {
	recommendedStorageQuantity, err := ds.kubecostsvc.GetRecommendation(ctx, pvName, targetUtilization, interval)
	if err != nil {
		return resource.Quantity{}, fmt.Errorf("failed to get recommendation from kubecost err: %w", err)
	}
	return recommendedStorageQuantity, nil
}

// newPVCName generates a new unique name for a PersistentVolumeClaim (PVC) to ensure that
// each scaling operation (up or down) results in a distinct and bounded set of names.
func (ds *DiskScaler) newPVCName(ctx context.Context, namespace, pvcName string) (string, error) {
	persVolC, err := ds.basicK8sClient.CoreV1().PersistentVolumeClaims(namespace).Get(ctx, pvcName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("unable to get the persistent volume claim %s in namespace %s", pvcName, namespace)
	}
	pvcAnnotations := persVolC.GetAnnotations()
	createdBy := pvcAnnotations[PVCAnnotationCreatedBy]
	if createdBy == "" {
		return fmt.Sprintf("%s-%s", pvcName, randStringRunes(5)), nil
	}
	return fmt.Sprintf("%s-%s", pvcName[:len(pvcName)-6], randStringRunes(5)), nil
}

// getPVInfo retrieves the PersistentVolume (PV) kubernetes spec for a given PV name
func (ds *DiskScaler) getPVInfo(ctx context.Context, pvName string) (*v1.PersistentVolume, error) {
	k8sPVInfo, err := ds.basicK8sClient.CoreV1().PersistentVolumes().Get(ctx, pvName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to to get pv information for Persistent Volume %s", pvName)
	}
	if k8sPVInfo == nil {
		return nil, fmt.Errorf("not a valid Persistent Volume %s", pvName)
	}
	return k8sPVInfo, nil
}

// isPVValidForDiskScaling checks if there are any hostpath mount, currently DAS doesnt support hostpath mounts
func (ds *DiskScaler) isPVValidForDiskScaling(k8spv *v1.PersistentVolume) bool {
	return k8spv.Spec.HostPath == nil
}

// getPVCMap retrieves and maps the PersistentVolumeClaim (PVC) and its associated information
// before scaling the deployment, in order to perform the PersistentVolume (PV) scaling.
func (ds *DiskScaler) getPVCMap(ctx context.Context, namespace string, deploymentName string) (map[string]*pvcDetails, error) {
	deployment := ds.basicK8sClient.AppsV1().
		Deployments(namespace)

	volumeMap := map[string]*pvcDetails{}
	v1Dep, err := deployment.Get(ctx, deploymentName, metav1.GetOptions{})
	if err != nil {
		log.Error().Msgf("ctx: %s, unable to get deployment for the name %s err: %v", ctx.Value(diskScalerRunContextKey), deploymentName, err)
		return volumeMap, fmt.Errorf("unable to get deployment for the name %s err: %w", deploymentName, err)
	}

	currentAnnotation := v1Dep.GetAnnotations()
	targetUtilization := currentAnnotation[AnnotationTargetUtilization]
	if targetUtilization == "" {
		targetUtilization = defaultTargetUtilization
	}

	var intTargetUtilization int
	if intTargetUtilization, err = strconv.Atoi(targetUtilization); err != nil {
		log.Warn().Msgf("targetUtilization is invalid for deployment name %s, defaulting to %s", deploymentName, defaultTargetUtilization)
		intTargetUtilization, _ = strconv.Atoi(defaultTargetUtilization)
	}

	interval := currentAnnotation[AnnotationInterval]
	if interval == "" {
		interval = defaultInterval
	}
	if _, err = time.ParseDuration(interval); err != nil {
		log.Warn().Msgf("interval is invalid for deployment name %s, defaulting to %s", deploymentName, defaultInterval)
		interval = defaultInterval
	}

	volumes := v1Dep.Spec.Template.Spec.Volumes
	for _, vol := range volumes {
		if vol.PersistentVolumeClaim == nil {
			log.Error().Msgf("ctx: %s, deployment %s contains non PV claim volume source", ctx.Value(diskScalerRunContextKey), deploymentName)
			return volumeMap, fmt.Errorf("deployment %s contains non PV claim volume source", deploymentName)
		}
		pvcName := vol.PersistentVolumeClaim.ClaimName
		k8sPVCInfo, err := ds.getPVCInfo(ctx, namespace, pvcName)
		if err != nil {
			return map[string]*pvcDetails{}, fmt.Errorf("failed to get volume map for pvc: %s with err: %w", pvcName, err)
		}

		newPVCName, err := ds.newPVCName(ctx, namespace, pvcName)
		if err != nil {
			return map[string]*pvcDetails{}, fmt.Errorf("failed to create a new PVC Name: %w", err)
		}

		pvName := k8sPVCInfo.Spec.VolumeName
		log.Debug().Msgf("ctx: %s, backing volume name is: %s for pvc: %s", ctx.Value(diskScalerRunContextKey), pvName, pvcName)
		spec := k8sPVCInfo.Spec
		storageCapacity := k8sPVCInfo.Status.Capacity[v1.ResourceStorage]
		storageClassName := k8sPVCInfo.Spec.StorageClassName

		scClass, err := ds.getStorageClassInfo(ctx, k8sPVCInfo.GetName(), *storageClassName)
		if err != nil {
			return map[string]*pvcDetails{}, fmt.Errorf("failed to get storage class info: %w", err)
		}

		provisioner := scClass.Provisioner
		allowExpansion := *scClass.AllowVolumeExpansion
		volumeBindingMode := *scClass.VolumeBindingMode
		log.Debug().Msgf("ctx: %s, provisioner is: %s allowVolumeExpansion is: %t, volumeBindingMode is: %s", ctx.Value(diskScalerRunContextKey), provisioner, allowExpansion, volumeBindingMode)

		// Currently only support storage class with provisioner "ebs.csi.aws.com"
		if !slices.Contains(supportedSCProvisioner, provisioner) {
			log.Error().Msgf("ctx: %s, unsupported provisioner %s for storage class %s", ctx.Value(diskScalerRunContextKey), provisioner, *storageClassName)
			return map[string]*pvcDetails{}, fmt.Errorf("unsupported provisioner %s for storage class %s", provisioner, *storageClassName)
		}

		if volumeBindingMode != volumeBindingWaitForFirstConsumer {
			log.Error().Msgf("ctx: %s, unsupported volumeBindingMode %s for storage class %s", ctx.Value(diskScalerRunContextKey), volumeBindingMode, *storageClassName)
			return map[string]*pvcDetails{}, fmt.Errorf("cannot support volume binding mode %s for storage class %s", volumeBindingMode, *storageClassName)
		}

		// CHeck to see if pv is not hostpath mounted
		pvInfo, err := ds.getPVInfo(ctx, pvName)
		if err != nil {
			return map[string]*pvcDetails{}, fmt.Errorf("unable to get kubernetes pv information for pv %s with err:%w", pvName, err)
		}

		isValid := ds.isPVValidForDiskScaling(pvInfo)
		if !isValid {
			return map[string]*pvcDetails{}, fmt.Errorf("pv %s is invalid for disk autoscaling", pvName)
		}

		resizeTo, err := ds.getKubecostRecommendationForPV(ctx, pvName, intTargetUtilization, interval)
		if err != nil {
			return map[string]*pvcDetails{}, fmt.Errorf("unable to get recommendation from kubecost %w", err)
		}

		pvcDetails := &pvcDetails{
			currentSize:          storageCapacity,
			storageClass:         *storageClassName,
			provisioner:          provisioner,
			allowVolumeExpansion: allowExpansion,
			spec:                 spec,
			resizeTo:             resizeTo,
			pvName:               pvName,
			resizedPVCName:       newPVCName,
		}

		volumeMap[k8sPVCInfo.GetName()] = pvcDetails
	}

	return volumeMap, nil
}

// retryAnnotateDeployment attempts to retry annotating the deployment in case of any infrastructure delay
func (ds *DiskScaler) retryscaleDeployment(ctx context.Context, deploymentName string, namespace string, scaleTo int32) (int32, error) {
	var retryErr error
	var originalScale int32
	for i := 0; i < maxRetries; i++ {
		originalScale, retryErr = ds.scaleDeployment(ctx, deploymentName, namespace, scaleTo)
		if retryErr == nil {
			break
		}
		log.Debug().Msgf("ctx: %s, failed to scale deployment with disk auto scaler in %d attempt(s) with err %v", ctx.Value(diskScalerRunContextKey), i, retryErr)
		if i < maxRetries-1 {
			time.Sleep(inactivityDuringDelay)
		}
	}
	return originalScale, retryErr
}

// scaleDeployment scales the specified Deployment to the number of replicas specified in scaleTo.
func (ds *DiskScaler) scaleDeployment(ctx context.Context, deploymentName string, namespace string, scaleTo int32) (int32, error) {
	deployment := ds.basicK8sClient.AppsV1().
		Deployments(namespace)

	s, err := deployment.
		GetScale(ctx, deploymentName, metav1.GetOptions{})
	if err != nil {
		return 0, fmt.Errorf("failed to get deployment to scale with err: %w", err)
	}

	sc := *s
	originalScale := sc.Spec.Replicas
	sc.Spec.Replicas = scaleTo

	if originalScale == scaleTo {
		log.Info().Msgf("scaling the deployment skipped as the existing deployment scale is the same: %d", originalScale)
	}

	_, err = deployment.
		UpdateScale(ctx,
			deploymentName, &sc, metav1.UpdateOptions{})
	if err != nil {
		return 0, fmt.Errorf("unable to scale the deployment: %w", err)
	}

	log.Info().Msgf("ctx: %s, successfully scaled deployment: %s from %d to %d", ctx.Value(diskScalerRunContextKey), deploymentName, originalScale, scaleTo)
	return originalScale, nil
}

// getPVCInfo retrieves information about the PersistentVolumeClaim (PVC) that is to be shrunk.
func (ds *DiskScaler) getPVCInfo(ctx context.Context, namespace, pvc string) (*v1.PersistentVolumeClaim, error) {
	k8sPVCInfo, err := ds.basicK8sClient.CoreV1().PersistentVolumeClaims(namespace).Get(ctx, pvc, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to to get pvc information for PersistentVolumeClaim %s", pvc)
	}
	if k8sPVCInfo == nil {
		return nil, fmt.Errorf("not a valid PersistentVolumeClaim %s", pvc)
	}
	return k8sPVCInfo, nil
}

// getStorageClassInfo gives the storage kubernetes object information of the PVCName.
func (ds *DiskScaler) getStorageClassInfo(ctx context.Context, pvcName string, scName string) (*storagev1.StorageClass, error) {
	scObject, err := ds.basicK8sClient.StorageV1().StorageClasses().Get(ctx, scName, metav1.GetOptions{})
	if err != nil {
		log.Error().Msgf("ctx: %s, unable to get storage class information for name %s for pv claim: %s with err: %v", ctx.Value(diskScalerRunContextKey), scName, pvcName, err)
		return nil, fmt.Errorf("unable to get storage class information for name %s for pv claim: %s with err: %w", scName, pvcName, err)
	}

	if scObject == nil {
		log.Error().Msgf("ctx: %s, empty storage class for pvc: %s", ctx.Value(diskScalerRunContextKey), pvcName)
		return nil, fmt.Errorf("empty storage class")
	}
	return scObject, nil
}

// deletePVC deletes the pvcName in the given namespace.
func (ds *DiskScaler) deletePVC(ctx context.Context, namespace string, pvcName string) error {

	v1PV, err := ds.basicK8sClient.CoreV1().PersistentVolumeClaims(namespace).Get(ctx, pvcName, metav1.GetOptions{})
	if err != nil && strings.Contains(err.Error(), "not found") {
		log.Debug().Msgf("ctx: %s, no pv claim: %s found to delete", ctx.Value(diskScalerRunContextKey), pvcName)
		return nil
	}

	if err != nil {
		return fmt.Errorf("unable to get persistent volume claim: %s err: %w", pvcName, err)
	}

	err = ds.basicK8sClient.CoreV1().PersistentVolumeClaims(namespace).Delete(ctx, pvcName, metav1.DeleteOptions{})
	if err != nil {
		return fmt.Errorf("unable to delete persisent volume claim: %s: %w", pvcName, err)
	}

	var w watch.Interface
	if w, err = ds.basicK8sClient.CoreV1().PersistentVolumeClaims(namespace).Watch(ctx, metav1.ListOptions{
		Watch:           true,
		ResourceVersion: v1PV.ResourceVersion,
	}); err != nil {
		return err
	}

	func() {
		for {
			select {
			case events, ok := <-w.ResultChan():
				if !ok {
					return
				}
				if events.Type == watch.Deleted {
					return
				}

			case <-time.After(diskScalingOperationTimeout):
				log.Error().Msgf("timeout to wait for existing pvc deletion")
				w.Stop()
			}
		}
	}()
	log.Debug().Msgf("ctx: %s, successfully deleted pv claim: %s ", ctx.Value(diskScalerRunContextKey), pvcName)
	return nil
}

// createPVCFromASpec is used to keep the spec between original PVC and new PVC same except the size
func (ds *DiskScaler) createPVCFromASpec(ctx context.Context, namespace string, pvc string, spec v1.PersistentVolumeClaimSpec, newSize resource.Quantity, newPVCName string) (*v1.PersistentVolumeClaim, error) {
	spec.Resources.Requests[v1.ResourceStorage] = newSize
	// volumename should be set to empty otherwise there will be resource creation failure
	spec.VolumeName = ""

	pvcObj := &v1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      newPVCName,
			Namespace: namespace,
			Annotations: map[string]string{
				PVCAnnotationCreatedBy: DiskAutoScaler,
			},
		},
		Spec: spec,
	}

	smallerPvc, err := ds.basicK8sClient.CoreV1().PersistentVolumeClaims(namespace).Create(ctx, pvcObj, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("unable to create pvc of size %s for original pvc %s: %v", newSize.String(), pvc, err)
	}
	return smallerPvc, nil
}

// dataMoverTransientPod create a transient pod to move data between original PV claim volume source to new PV Claim volume source
func (ds *DiskScaler) dataMoverTransientPod(ctx context.Context, namespace string, copierPodName string, originalPVC string, newPVC string) error {
	cpCommand := "if [ -z \"$(ls -A /oldData)\" ]; then echo \"directory is empty no need to copy\"; else  cp -r /oldData/* /newData/; fi"
	req := &v1.Pod{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Pod",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: copierPodName,
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name:    "temp-container",
					Image:   "ubuntu",
					Command: []string{"/bin/bash", "-c", "sleep infinity"},
					VolumeMounts: []v1.VolumeMount{
						v1.VolumeMount{
							Name:      "orig-vol-mount",
							MountPath: "/oldData",
						},
						v1.VolumeMount{
							Name:      "backup-vol-mount",
							MountPath: "/newData",
						},
					},
				},
			},
			Volumes: []v1.Volume{
				v1.Volume{
					Name: "orig-vol-mount",
					VolumeSource: v1.VolumeSource{
						PersistentVolumeClaim: &v1.PersistentVolumeClaimVolumeSource{
							ClaimName: originalPVC,
						},
					},
				},
				v1.Volume{
					Name: "backup-vol-mount",
					VolumeSource: v1.VolumeSource{
						PersistentVolumeClaim: &v1.PersistentVolumeClaimVolumeSource{
							ClaimName: newPVC,
						},
					},
				},
			},
		},
	}

	resp, err := ds.basicK8sClient.CoreV1().Pods(namespace).Create(ctx, req, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("failed to create copier pod %s in namespace %s with err: %w ", copierPodName, namespace, err)
	}

	status := resp.Status

	var w watch.Interface
	if w, err = ds.basicK8sClient.CoreV1().Pods(namespace).Watch(ctx, metav1.ListOptions{
		Watch:           true,
		ResourceVersion: resp.ResourceVersion,
	}); err != nil {
		return fmt.Errorf("failed to create watcher for creation of copier pod %s in namespace %s with err: %w", copierPodName, namespace, err)
	}

	func() {
		for {
			select {
			case events, ok := <-w.ResultChan():
				if !ok {
					return
				}
				switch events.Object.(type) {
				case *v1.Pod:
					resp = events.Object.(*v1.Pod)
					status = resp.Status
					// Check for if pod is in running state before the diskScalingOperationTimeout
					if resp.Status.Phase == v1.PodRunning {
						w.Stop()
					}
				default:
					// intermittent typecasting to *metav1.Status... In this case we continue
					// and look for if pod is running set diskScalingOperationTimeout
				}

			case <-time.After(diskScalingOperationTimeout):
				log.Error().Msgf("ctx: %s, timeout to wait for pod %s in namespace %s to be in running state", ctx.Value(diskScalerRunContextKey), copierPodName, namespace)
				w.Stop()
			}
		}
	}()

	// The check ensure after diskScalingOperationTimeout, did the data mover pod moved to running state.
	if status.Phase != v1.PodRunning {
		return fmt.Errorf("timeout to wait for pod %s in namespace %s to be in running state", copierPodName, namespace)
	}

	log.Debug().Msgf("ctx: %s, successfully created transient pod: %s in namespace: %s", ctx.Value(diskScalerRunContextKey), copierPodName, namespace)

	buf := &bytes.Buffer{}
	errBuf := &bytes.Buffer{}
	request := ds.basicK8sClient.CoreV1().RESTClient().
		Post().
		Namespace(namespace).
		Resource("pods").
		Name(copierPodName).
		SubResource("exec").
		VersionedParams(&v1.PodExecOptions{
			Command: []string{"/bin/sh", "-c", cpCommand},
			Stdin:   false,
			Stdout:  true,
			Stderr:  true,
			TTY:     true,
		}, scheme.ParameterCodec)
	exec, err := remotecommand.NewSPDYExecutor(ds.clientConfig, "POST", request.URL())
	if err != nil {
		return fmt.Errorf("failed to create remote command executor object on namespace:%s pod:%s err: %w", namespace, copierPodName, err)
	}
	err = exec.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdout: buf,
		Stderr: errBuf,
	})
	if err != nil {
		return fmt.Errorf("failed to perform copy operation on namespace:%s pod:%s with err: %w", namespace, copierPodName, err)
	}

	log.Debug().Msgf("ctx: %s, successfully executed command on pod: %s", ctx.Value(diskScalerRunContextKey), copierPodName)

	return nil
}

// retryDeleteTransientPod attempts to delete the transient pod whenever there are any intermittent failure
func (ds *DiskScaler) retryDeleteTransientPod(ctx context.Context, namespace string, copierPodName string, isForceful bool) error {
	var retryErr error
	for i := 0; i < maxRetries; i++ {
		retryErr = ds.deleteTransientPod(ctx, namespace, copierPodName, isForceful)
		if retryErr == nil {
			break
		}
		log.Debug().Msgf("ctx: %s, deletion of transient pod failed in attempt %d with err %v", ctx.Value(diskScalerRunContextKey), i, retryErr)
		if i < maxRetries-1 {
			time.Sleep(inactivityDuringDelay)
		}
	}
	return retryErr
}

// deleteTransientPod deletes the transient copier pod used for the copy operation between pvs
func (ds *DiskScaler) deleteTransientPod(ctx context.Context, namespace string, copierPodName string, isForceful bool) error {
	var err error
	var w watch.Interface

	v1Pod, _ := ds.basicK8sClient.CoreV1().Pods(namespace).Get(ctx, copierPodName, metav1.GetOptions{})
	if v1Pod == nil || v1Pod.Name == "" {
		log.Debug().Msgf("pod name : %s doesn't exist in namespace: %s", copierPodName, namespace)
		return nil
	}

	var deletionOption metav1.DeleteOptions
	deletePolicy := metav1.DeletePropagationForeground
	deletionOption.PropagationPolicy = &deletePolicy
	if isForceful {
		force := int64(0)
		deletionOption.GracePeriodSeconds = &force
	}
	err = ds.basicK8sClient.CoreV1().Pods(namespace).Delete(ctx, copierPodName, metav1.DeleteOptions{
		PropagationPolicy: &deletePolicy,
	})
	if err != nil {
		return fmt.Errorf("failed to delete transient pod: %s: %w", copierPodName, err)
	}

	v1Pod, err = ds.basicK8sClient.CoreV1().Pods(namespace).Get(ctx, copierPodName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("unable to get transient pod: %s: %w", copierPodName, err)
	}

	if w, err = ds.basicK8sClient.CoreV1().Pods(namespace).Watch(ctx, metav1.ListOptions{
		Watch:           true,
		ResourceVersion: v1Pod.ResourceVersion,
	}); err != nil {
		return err
	}

	func() {
		for {
			select {
			case events, ok := <-w.ResultChan():
				if !ok {
					return
				}
				if events.Type == watch.Deleted {
					return
				}

			case <-time.After(120 * time.Second):
				log.Error().Msgf("ctx: %s, timeout to wait for pod deletion %s in namespace %s", ctx.Value(diskScalerRunContextKey), copierPodName, namespace)
				w.Stop()
			}
		}
	}()

	log.Debug().Msgf("ctx: %s, successfully delete transient pod: %s in namespace: %s", ctx.Value(diskScalerRunContextKey), copierPodName, namespace)
	return nil
}

// isGreaterQuantity returns true if resizeTo is greater than original size
func isGreaterQuantity(originalSize resource.Quantity, resizeTo resource.Quantity) bool {
	return resizeTo.Cmp(originalSize) == 1
}

// isEqualQuantity returns true if resizeTo is equal to original size
func isEqualQuantity(originalSize resource.Quantity, resizeTo resource.Quantity) bool {
	return resizeTo.Cmp(originalSize) == 0
}

// generates a random string of n characters to create a new PVC claim or pod
func randStringRunes(n int) string {
	b := make([]rune, n)
	for i := range b {
		b[i] = allowedCharactersForPVCName[rand.Intn(len(allowedCharactersForPVCName))]
	}
	return string(b)
}
