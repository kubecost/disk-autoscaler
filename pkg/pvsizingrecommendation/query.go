package pvsizingrecommendation

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
	"k8s.io/apimachinery/pkg/api/resource"
)

const (
	oneGiBytes                 = 1024.0 * 1024.0 * 1024.0
	supportedStorageRegex      = "^([+-]?[0-9.]+)([eEinumkKMGTP]*[-+]?[0-9]*)$"
	equalityThreshold          = 0.00001
	cacheRefresh               = 10 * time.Minute
	overwriteTargetUtilization = 70
)

type KubecostService struct {
	clusterInfoApiPath    string
	recommendationApiPath string
	cache                 map[string][]byte
	mu                    sync.Mutex
}

func NewKubecostService(modelPath string) *KubecostService {
	modelPath = strings.TrimSuffix(modelPath, "/")
	cache := make(map[string][]byte)
	svc := &KubecostService{
		clusterInfoApiPath:    fmt.Sprintf("%s/%s", modelPath, path.Join("clusterInfo")),
		recommendationApiPath: fmt.Sprintf("%s/%s", modelPath, path.Join("savings", "persistentVolumeSizing")),
		cache:                 cache,
	}
	ticker := time.NewTicker(cacheRefresh)
	go func() {
		for {
			for t := time.Now(); ; t = <-ticker.C {
				log.Trace().Msgf("refreshing cache storing kubecost response at %s", t.String())
				svc.refreshCache()
			}
		}
	}()
	return svc
}

// refreshCache refreshes the cache storing kubecost pv savings recommendation
func (krs *KubecostService) refreshCache() {
	krs.mu.Lock()
	defer krs.mu.Unlock()
	krs.cache = make(map[string][]byte, 0)
}

// CheckAvailable returns nil if the service is available to handle requests.
func (krs *KubecostService) CheckAvailable(ctx context.Context) error {
	request, err := http.NewRequestWithContext(ctx, "GET", krs.recommendationApiPath, nil)
	if err != nil {
		return fmt.Errorf("failed to build request for API path '%s': %s", krs.recommendationApiPath, err)
	}
	resp, err := http.DefaultClient.Do(request)
	if err != nil {
		return fmt.Errorf("failed to execute request: %s", err)
	}
	defer resp.Body.Close()

	log.Debug().
		Int("status", resp.StatusCode).
		Str("endpoint", krs.recommendationApiPath).
		Msgf("Recommendation service IsAvailable() GET finished")

	// A 400 is actually acceptable because we aren't making a _valid_ query,
	// just making sure that the endpoint exists.
	if resp.StatusCode == 404 {
		return fmt.Errorf("unavailable because status (%d) is invalid", resp.StatusCode)
	}

	return nil
}

type RecResponse struct {
	Recommendation []*PVRecommendation `json:"recommendations"`
}

type PVRecommendation struct {
	VolumeName               string  `json:"volumeName"`
	AverageUsageBytes        float64 `json:"averageUsageBytes"`
	CurrentCapacityBytes     float64 `json:"currentCapacityBytes"`
	RecommendedCapacityBytes float64 `json:"recommendedCapacityBytes"`
	SavingsMonthly           float64 `json:"savingsMonthly"`
}

type RecommendationSizeWithSavings struct {
	RecommendedResourceSize resource.Quantity
	Savings                 float64
}

func (krs *KubecostService) GetRecommendation(ctx context.Context, pvName string, targetUtilization int, interval string) (RecommendationSizeWithSavings, error) {
	ohPercentage := computeOverHeadPercentForTargetUtilization(targetUtilization)
	recommendation := RecommendationSizeWithSavings{}
	respBody, err := krs.getFromCacheOrFetch(interval, ohPercentage)
	if err != nil {
		return recommendation, fmt.Errorf("failed to fetch pv recommendation from kubecost: %w", err)
	}

	var recResp RecResponse
	err = json.Unmarshal(respBody, &recResp)
	if err != nil {
		return recommendation, fmt.Errorf("unable to parse the response from kubecost")
	}
	var recommendedBytes float64
	var savingsMonthly float64
	for _, pvRecommendation := range recResp.Recommendation {
		// Make sure that the recommended bytes is 70% utilization of current average usage byte compute from kubecost
		if pvRecommendation.VolumeName == pvName {
			recommendedBytes = pvRecommendation.RecommendedCapacityBytes
			savingsMonthly = pvRecommendation.SavingsMonthly
		}
	}

	// Happens when the storage provisioned but kubecost hasnt recieved data
	if almostEqual(recommendedBytes, 0.0) {
		return recommendation, fmt.Errorf("unable to find accurate utilization from kubecost at this time")
	}

	// 1 Gi is the smallest storage size in AWS EBS
	// ref : https://kubernetes.io/docs/tasks/administer-cluster/limit-storage-consumption/#limitrange-to-limit-requests-for-storage
	if recommendedBytes < oneGiBytes {
		log.Debug().Msgf("recommendedBytes from kubecost %f[in Bytes] is less than 1 Gi, defaulting to 1 Gi, since minimal storage provision is 1Gi", recommendedBytes)
		recommendation.RecommendedResourceSize = resource.MustParse("1Gi")
		recommendation.Savings = savingsMonthly
		return recommendation, nil
	}

	recommendedSize, err := convertKubecostBytesToStorageRecommendation(recommendedBytes / oneGiBytes)
	if err != nil {
		return recommendation, fmt.Errorf("failed to convert kubecost bytes recommendation to storage request: %w", err)
	}
	recommendation.RecommendedResourceSize = recommendedSize
	recommendation.Savings = savingsMonthly

	return recommendation, nil
}

// getFromCacheOrFetch fetches from cache instead of repeated calling kubecost pvsizing recommendation end point
func (krs *KubecostService) getFromCacheOrFetch(window string, overheadPercent string) ([]byte, error) {
	krs.mu.Lock()
	defer krs.mu.Unlock()

	cacheKey := window
	if content, ok := krs.cache[window]; ok {
		return content, nil
	}

	queryParams := map[string]string{
		"window":          window,
		"overheadPercent": overheadPercent,
	}

	req, err := http.NewRequest("GET", krs.recommendationApiPath, nil)
	if err != nil {
		return []byte{}, fmt.Errorf("making request: %s", err)
	}
	q := req.URL.Query()
	for k, v := range queryParams {
		q.Add(k, v)
	}
	req.URL.RawQuery = q.Encode()
	log.Debug().
		Str("url", req.URL.String()).
		Msgf("Request recommendation")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return []byte{}, fmt.Errorf("executing query: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return []byte{}, fmt.Errorf("reading response body: %s", err)
	}

	if resp.StatusCode != http.StatusOK {
		return []byte{}, fmt.Errorf("non-OK response status (%d), body: %s", resp.StatusCode, string(respBody))
	}

	krs.cache[cacheKey] = respBody
	return respBody, nil
}

// convertKubecostBytesToStorageRecommendation converts the byte recommendation
// from Kubecost to a storage resource.Quantity. The function rounds to the
// nearest whole number greater than the storage recommendation when there
// is a fractional recommendation. A fractional recommendation can lead to an error
// when a PVC is described, even though Kubernetes is intelligent enough to round
// it to the nearest whole number, as this function does.
func convertKubecostBytesToStorageRecommendation(bf float64) (resource.Quantity, error) {
	recommendedStorageStr := func(bf float64) string {
		for _, unit := range []string{"Gi", "Ti", "Pi", "Ei", "Zi"} {
			if math.Abs(bf) < 1024.0 {
				return fmt.Sprintf("%3.1f%s", math.Ceil(bf), unit)
			}
			bf /= 1024.0
		}
		return fmt.Sprintf("%.1fYi", math.Round(bf))
	}(bf)
	// Make sure the string is a valid storage string
	_, err := regexp.MatchString(supportedStorageRegex, recommendedStorageStr)
	if err != nil {
		return resource.Quantity{}, fmt.Errorf("recommendation is not valid storage resource string")
	}
	return resource.MustParse(recommendedStorageStr), nil
}

// Kubecost PV right sizing API takes overhead percentage, the 70% utilization i.e 70 in 100 is
// 100 in 100+[30/70] equivalent. This function perform the conversion to parameter taken by kubecost.
func computeOverHeadPercentForTargetUtilization(tu int) string {
	// If user provides 0 in target utilization overwrite to 70% target utilization
	if tu == 0 {
		tu = overwriteTargetUtilization
	}
	overheadPercent := (float64(100-tu) / float64(tu) * 100)
	return fmt.Sprintf("%.2f", overheadPercent)
}

func almostEqual(val1, val2 float64) bool {
	return math.Abs(val1-val2) <= equalityThreshold
}
