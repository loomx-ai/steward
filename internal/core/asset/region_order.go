package asset

import "strings"

// RegionIDLess orders regions by geography, then naturally by region ID:
// Asia Pacific - China, Asia Pacific - Other, Europe and Americas,
// Middle East, and Other.
func RegionIDLess(left, right string) bool {
	leftGroup := regionGeographyGroup(left)
	rightGroup := regionGeographyGroup(right)
	if leftGroup != rightGroup {
		return leftGroup < rightGroup
	}
	return naturalRegionIDLess(left, right)
}

func regionGeographyGroup(regionID string) int {
	regionID = strings.ToLower(strings.TrimSpace(regionID))
	switch {
	case strings.HasPrefix(regionID, "cn-"):
		return 0
	case strings.HasPrefix(regionID, "ap-"):
		return 1
	case hasRegionPrefix(regionID, "eu-", "eusc-", "us-", "ca-", "na-", "sa-", "mx-", "br-"):
		return 2
	case hasRegionPrefix(regionID, "me-", "il-", "ae-"):
		return 3
	default:
		return 4
	}
}

func hasRegionPrefix(regionID string, prefixes ...string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(regionID, prefix) {
			return true
		}
	}
	return false
}

func naturalRegionIDLess(left, right string) bool {
	normalizedLeft := strings.ToLower(strings.TrimSpace(left))
	normalizedRight := strings.ToLower(strings.TrimSpace(right))
	for leftIndex, rightIndex := 0, 0; leftIndex < len(normalizedLeft) && rightIndex < len(normalizedRight); {
		leftDigit := normalizedLeft[leftIndex] >= '0' && normalizedLeft[leftIndex] <= '9'
		rightDigit := normalizedRight[rightIndex] >= '0' && normalizedRight[rightIndex] <= '9'
		if leftDigit && rightDigit {
			leftEnd := leftIndex
			for leftEnd < len(normalizedLeft) && normalizedLeft[leftEnd] >= '0' && normalizedLeft[leftEnd] <= '9' {
				leftEnd++
			}
			rightEnd := rightIndex
			for rightEnd < len(normalizedRight) && normalizedRight[rightEnd] >= '0' && normalizedRight[rightEnd] <= '9' {
				rightEnd++
			}
			leftSignificant := leftIndex
			for leftSignificant < leftEnd && normalizedLeft[leftSignificant] == '0' {
				leftSignificant++
			}
			rightSignificant := rightIndex
			for rightSignificant < rightEnd && normalizedRight[rightSignificant] == '0' {
				rightSignificant++
			}
			leftDigits := normalizedLeft[leftSignificant:leftEnd]
			rightDigits := normalizedRight[rightSignificant:rightEnd]
			if len(leftDigits) != len(rightDigits) {
				return len(leftDigits) < len(rightDigits)
			}
			if leftDigits != rightDigits {
				return leftDigits < rightDigits
			}
			if leftEnd-leftIndex != rightEnd-rightIndex {
				return leftEnd-leftIndex < rightEnd-rightIndex
			}
			leftIndex = leftEnd
			rightIndex = rightEnd
			continue
		}
		if normalizedLeft[leftIndex] != normalizedRight[rightIndex] {
			return normalizedLeft[leftIndex] < normalizedRight[rightIndex]
		}
		leftIndex++
		rightIndex++
	}
	if len(normalizedLeft) != len(normalizedRight) {
		return len(normalizedLeft) < len(normalizedRight)
	}
	return left < right
}
