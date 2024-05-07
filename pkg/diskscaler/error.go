// All the available custom error types of disk auto scaler
package diskscaler

import (
	"fmt"
	"strings"
)

// Custom error to return to the service calling the
// disk autoscaler workflow all PVC scaling failed
type DiskScalingAllFailedError struct {
	namespace  string
	deployment string
}

func (e *DiskScalingAllFailedError) Error() string {
	return fmt.Sprintf("failed to scale all the persistent volume claims in deployment %s belonging to namespace %s", e.namespace, e.deployment)
}

// Custom error to return to the service calling the
// disk autoscaler workflow when some of PVC in scaling failed
type DiskScalingPartialFailedError struct {
	namespace  string
	deployment string
	pvc        []string
}

func (e *DiskScalingPartialFailedError) Error() string {
	return fmt.Sprintf("failed to scale persistent volume claims %s in deployment %s belonging to namespace %s", strings.Join(e.pvc, ","), e.namespace, e.deployment)
}
