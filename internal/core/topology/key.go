package topology

import (
	"encoding/base64"
	"fmt"
	"strings"
)

const accountGlobalKey = "account-global"

func AccountGlobalFocusKey() string {
	return accountGlobalKey
}

func RegionFocusKey(regionID string) string {
	return "region:" + encodeKeySegment(regionID)
}

func RegionPublicFocusKey(regionID string) string {
	return "region-public:" + encodeKeySegment(regionID)
}

func VPCFocusKey(regionID, vpcID string) string {
	return "vpc:" + encodeKeySegment(regionID) + ":" + encodeKeySegment(vpcID)
}

func ParseFocusKey(value string) (Focus, error) {
	if value == accountGlobalKey {
		return Focus{Kind: FocusAccountGlobal}, nil
	}
	parts := strings.Split(value, ":")
	switch {
	case len(parts) == 2 && parts[0] == "region":
		regionID, err := decodeKeySegment(parts[1])
		if err != nil {
			return Focus{}, fmt.Errorf("decode Region focus: %w", err)
		}
		return Focus{Kind: FocusRegion, RegionID: regionID}, nil
	case len(parts) == 2 && parts[0] == "region-public":
		regionID, err := decodeKeySegment(parts[1])
		if err != nil {
			return Focus{}, fmt.Errorf("decode Region-public focus: %w", err)
		}
		return Focus{Kind: FocusRegionPublic, RegionID: regionID}, nil
	case len(parts) == 3 && parts[0] == "vpc":
		regionID, err := decodeKeySegment(parts[1])
		if err != nil {
			return Focus{}, fmt.Errorf("decode VPC Region focus: %w", err)
		}
		vpcID, err := decodeKeySegment(parts[2])
		if err != nil {
			return Focus{}, fmt.Errorf("decode VPC focus: %w", err)
		}
		return Focus{Kind: FocusVPC, RegionID: regionID, VPCID: vpcID}, nil
	default:
		return Focus{}, fmt.Errorf("topology focus key is malformed")
	}
}

func encodeKeySegment(value string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(value))
}

func decodeKeySegment(value string) (string, error) {
	if value == "" {
		return "", fmt.Errorf("focus key segment is empty")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return "", err
	}
	if len(decoded) == 0 {
		return "", fmt.Errorf("focus key segment is empty")
	}
	return string(decoded), nil
}

func vSwitchProjectionKey(connectionID, regionID, vSwitchID string) string {
	return "vswitch:" + encodeKeySegment(connectionID) + ":" + encodeKeySegment(regionID) + ":" + encodeKeySegment(vSwitchID)
}
