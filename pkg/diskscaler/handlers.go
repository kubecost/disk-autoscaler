package diskscaler

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

func (dss *DiskScalerService) enableDiskAutoScaling(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	q := r.URL.Query()
	namespace := q.Get("namespace")
	deployment := q.Get("deployment")
	interval := q.Get("interval")
	targetUtilization := q.Get("targetUtilization")
	if namespace == "" {
		http.Error(w, "namespace is empty", http.StatusInternalServerError)
		return
	}

	if deployment == "" {
		http.Error(w, "deployment is empty", http.StatusInternalServerError)
		return
	}

	if interval == "" {
		interval = defaultInterval
	}

	_, err := time.ParseDuration(interval)
	if err != nil {
		http.Error(w, fmt.Sprintf("interval duration parsing failed with err: %v", err), http.StatusInternalServerError)
		return
	}

	if _, err := strconv.Atoi(targetUtilization); err != nil {
		http.Error(w, fmt.Sprintf("targetUtilization parsing failed with err: %v", err), http.StatusInternalServerError)
		return
	}

	ctx := context.Background()
	ctx = context.WithValue(ctx, diskScalerServiceAnnotateContextKey, fmt.Sprintf("%s:%s", namespace, deployment))

	err = dss.enableDeployment(ctx, namespace, deployment, interval, targetUtilization)
	if err != nil {
		http.Error(w, fmt.Sprintf("unable to annotate namespace: %s, deployment: %s with err: %v", namespace, deployment, err), http.StatusInternalServerError)
		return
	}
}

func (dss *DiskScalerService) excludeDiskAutoScaling(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	q := r.URL.Query()
	namespace := q.Get("namespace")
	deployment := q.Get("deployment")
	if namespace == "" {
		http.Error(w, "namespace is empty", http.StatusInternalServerError)
		return
	}

	if deployment == "" {
		http.Error(w, "deployment is empty", http.StatusInternalServerError)
		return
	}

	ctx := context.Background()
	ctx = context.WithValue(ctx, diskScalerServiceAnnotateContextKey, fmt.Sprintf("%s:%s", namespace, deployment))

	err := dss.excludeDeployment(ctx, namespace, deployment)
	if err != nil {
		http.Error(w, fmt.Sprintf("unable to annotate namespace: %s, deployment: %s with err: %v", namespace, deployment, err), http.StatusInternalServerError)
		return
	}
}
