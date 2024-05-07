package diskscaler

import (
	"log"
	"testing"

	"k8s.io/apimachinery/pkg/api/resource"
)

func Test_isGreaterQuantity(t *testing.T) {
	cases := map[string]struct {
		originalQuantity string
		resizeToQuantity string
		expected         bool
	}{
		"when original is great than resize": {
			originalQuantity: "5Gi",
			resizeToQuantity: "2Gi",
			expected:         false,
		},
		"when original is less than resize": {
			originalQuantity: "2Gi",
			resizeToQuantity: "3Gi",
			expected:         true,
		},
		"when original is equal to  resize": {
			originalQuantity: "2Gi",
			resizeToQuantity: "2Gi",
			expected:         false,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			og := resource.MustParse(tc.originalQuantity)
			rt := resource.MustParse(tc.resizeToQuantity)
			returnBool := isGreaterQuantity(og, rt)
			if tc.expected != returnBool {
				log.Fatalf("for test case: `%s`, expected bool %t but received %t", name, tc.expected, returnBool)
			}
		})
	}

}
