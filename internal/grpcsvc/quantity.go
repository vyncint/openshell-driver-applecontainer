package grpcsvc

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// ParseCPUQuantity converts a Kubernetes-style CPU quantity ("2", "500m",
// "1.5") into whole vCPUs for VM sizing, rounding up. Empty input returns 0
// (caller applies its default).
func ParseCPUQuantity(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	var cores float64
	if strings.HasSuffix(s, "m") {
		milli, err := strconv.ParseFloat(strings.TrimSuffix(s, "m"), 64)
		if err != nil {
			return 0, fmt.Errorf("invalid cpu quantity %q", s)
		}
		cores = milli / 1000
	} else {
		var err error
		cores, err = strconv.ParseFloat(s, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid cpu quantity %q", s)
		}
	}
	if cores < 0 || math.IsNaN(cores) || math.IsInf(cores, 0) {
		return 0, fmt.Errorf("invalid cpu quantity %q", s)
	}
	if cores == 0 {
		return 0, nil
	}
	n := int64(math.Ceil(cores))
	if n < 1 {
		n = 1
	}
	return n, nil
}

var memorySuffixes = []struct {
	suffix string
	factor float64
}{
	// Longest suffixes first so "Gi" wins over "G".
	{"Ki", 1 << 10}, {"Mi", 1 << 20}, {"Gi", 1 << 30}, {"Ti", 1 << 40},
	{"K", 1e3}, {"M", 1e6}, {"G", 1e9}, {"T", 1e12},
}

// ParseMemoryQuantityMB converts a Kubernetes-style memory quantity
// ("512Mi", "4Gi", "1G", plain bytes) into MiB for VM sizing, rounding up.
// Empty input returns 0 (caller applies its default).
func ParseMemoryQuantityMB(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	factor := 1.0
	num := s
	for _, m := range memorySuffixes {
		if strings.HasSuffix(s, m.suffix) {
			factor = m.factor
			num = strings.TrimSuffix(s, m.suffix)
			break
		}
	}
	v, err := strconv.ParseFloat(num, 64)
	if err != nil || v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, fmt.Errorf("invalid memory quantity %q", s)
	}
	bytes := v * factor
	if bytes == 0 {
		return 0, nil
	}
	mb := int64(math.Ceil(bytes / (1 << 20)))
	if mb < 1 {
		mb = 1
	}
	return mb, nil
}
