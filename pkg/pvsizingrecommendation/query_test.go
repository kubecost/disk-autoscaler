package pvsizingrecommendation

import (
	"testing"

	"k8s.io/apimachinery/pkg/api/resource"
)

func Test_almostEqual(t *testing.T) {
	type testCase struct {
		name             string
		val1             float64
		val2             float64
		expectedEquality bool
	}

	testCases := []testCase{
		{
			name:             "when two values are 0.0 and are equal",
			val1:             0.0,
			val2:             0.0,
			expectedEquality: true,
		},
		{
			name:             "when two values are equality threshold apart",
			val1:             1.0,
			val2:             1.0 + equalityThreshold,
			expectedEquality: false,
		},
		{
			name:             "when two values are having large difference",
			val1:             2.0,
			val2:             5.0,
			expectedEquality: false,
		},
		// Test to prove even smaller difference that equalitythreshold return true
		{
			name:             "when two values are having 1/10 difference of equality threshold",
			val1:             2.0,
			val2:             2.0 + (equalityThreshold / 10),
			expectedEquality: true,
		},
		// Test to prove even higher difference that equalitythreshold return false
		{
			name:             "when two values are having 10 times of equality threshold",
			val1:             2.0,
			val2:             2.0 + (equalityThreshold * 10),
			expectedEquality: false,
		},
	}

	for _, tc := range testCases {
		isEqual := almostEqual(tc.val1, tc.val2)
		if isEqual != tc.expectedEquality {
			t.Fatalf("test '%s': failed expected bool is %t but received %t", tc.name, tc.expectedEquality, isEqual)
		}
	}
}

func Test_convertKubecostBytesToStorageRecommendation(t *testing.T) {
	type testCase struct {
		name            string
		val             float64
		expectedQuanity resource.Quantity
	}

	testCases := []testCase{
		{
			name:            "when value is exactly 1Gi",
			val:             (1024 * 1024 * 1024 / oneGiBytes),
			expectedQuanity: resource.MustParse("1Gi"),
		},
		{
			name:            "when 1.7 Gi is rounded to 2 Gi",
			val:             (1825361100 / oneGiBytes),
			expectedQuanity: resource.MustParse("2Gi"),
		},
		{
			name:            "when value is 1.2 Gi it should round to 2 Gi",
			val:             (1288490188 / oneGiBytes),
			expectedQuanity: resource.MustParse("2Gi"),
		},
		{
			name:            "1024 Gi is rounded to 1 Ti",
			val:             (1024.0 * oneGiBytes / oneGiBytes),
			expectedQuanity: resource.MustParse("1Ti"),
		},
		{
			name:            "Make sure 1025 Gi is rounded to 2Ti not 1 Ti",
			val:             (1025.0 * oneGiBytes / oneGiBytes),
			expectedQuanity: resource.MustParse("2Ti"),
		},
	}

	for _, tc := range testCases {
		returnStorageQuantity, err := convertKubecostBytesToStorageRecommendation(tc.val)
		if err != nil {
			t.Fatalf("test case %s: received unexpected err: %s  ", tc.name, err)
		}
		if returnStorageQuantity.Cmp(tc.expectedQuanity) != 0 {
			t.Fatalf("test case %s: failed expected quantity %s but received %s", tc.name, tc.expectedQuanity.String(), returnStorageQuantity.String())
		}
	}
}
