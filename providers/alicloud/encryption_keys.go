package alicloud

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const (
	rdsInstanceNativeType    = "ACS::RDS::DBInstance"
	polarDBClusterNativeType = "ACS::PolarDB::DBCluster"
	redisInstanceNativeType  = "ACS::Redis::DBInstance"

	// NormalizedTDEEncryptionKeyField holds the KMS key behind transparent
	// data encryption; the disk or cloud-disk key stays in EncryptionKey.
	NormalizedTDEEncryptionKeyField = "TDEEncryptionKey"
)

// encryptionKeyLookup reads the KMS keys a database uses through its
// product's own encryption APIs, which the list responses omit. Each call
// names the error codes the official API documents for an engine, edition or
// instance that cannot use a customer key: those prove there is no key. Any
// other failure fails the batch, so an unreadable key is never mistaken for
// the absence of a dependency on it.
type encryptionKeyLookup struct {
	operation string
	parameter string
	// statusPath gates the key on the reported TDE status; empty means the
	// response carries no status and the key alone is authoritative.
	statusPath string
	keyField   string
	keyPath    string
	noKeyCodes []string
	// followUp reads the key after a status-only call reports TDE enabled.
	followUp *encryptionKeyLookup
}

var encryptionKeyLookups = map[string][]encryptionKeyLookup{
	rdsInstanceNativeType: {
		{
			operation: "AlibabaCloud.RDS.DescribeDBInstanceTDE", parameter: "DBInstanceId",
			statusPath: "TDEStatus", keyField: NormalizedTDEEncryptionKeyField, keyPath: "EncryptionKey",
			noKeyCodes: []string{"Api.NotSupport", "IncorrectDBInstanceType", "IncorrectEngineVersion", "InvalidDBInstanceId.NotFound"},
		},
		{
			operation: "AlibabaCloud.RDS.DescribeDBInstanceEncryptionKey", parameter: "DBInstanceId",
			keyField: "EncryptionKey", keyPath: "EncryptionKey",
			noKeyCodes: []string{"Api.NotSupport", "NoActiveBYOK", "InvalidDBInstanceId.NotFound"},
		},
	},
	polarDBClusterNativeType: {
		{
			operation: "AlibabaCloud.PolarDB.DescribeDBClusterTDE", parameter: "DBClusterId",
			statusPath: "TDEStatus", keyField: NormalizedTDEEncryptionKeyField, keyPath: "EncryptionKey",
			noKeyCodes: []string{"InvalidDBCluster.NotFound", "InvalidDBClusterId.NotFound"},
		},
	},
	redisInstanceNativeType: {
		{
			operation: "AlibabaCloud.Redis.DescribeInstanceTDEStatus", parameter: "InstanceId",
			statusPath: "TDEStatus",
			noKeyCodes: []string{"InvalidInstanceId.NotFound"},
			followUp: &encryptionKeyLookup{
				operation: "AlibabaCloud.Redis.DescribeEncryptionKey", parameter: "InstanceId",
				keyField: NormalizedTDEEncryptionKeyField, keyPath: "EncryptionKey",
				noKeyCodes: []string{"InstanceType.NotSupport"},
			},
		},
	},
}

func (r *Runtime) enrichEncryptionKeys(
	ctx context.Context,
	request contracts.InventoryRequest,
	items []contracts.InventoryItem,
) ([]contracts.InventoryItem, error) {
	if strings.TrimSpace(request.Source) == "resource-center" {
		return items, nil
	}
	var indexes []int
	for index := range items {
		if len(encryptionKeyLookups[items[index].NativeType]) > 0 && strings.TrimSpace(items[index].NativeID) != "" {
			indexes = append(indexes, index)
		}
	}
	if len(indexes) == 0 {
		return items, nil
	}
	region, err := inventoryRegion(request)
	if err != nil {
		return nil, err
	}
	credential, err := r.resolveCredential(ctx, request.ConnectionID)
	if err != nil {
		return nil, err
	}
	err = ForEachConcurrently(len(indexes), func(position int) error {
		item := &items[indexes[position]]
		nativeID := strings.TrimSpace(item.NativeID)
		for _, lookup := range encryptionKeyLookups[item.NativeType] {
			for current := &lookup; current != nil; {
				key, next, err := r.readEncryptionKey(ctx, request, &credential, region, nativeID, *current)
				if err != nil {
					return err
				}
				if key != "" {
					if item.Normalized == nil {
						item.Normalized = make(map[string]any)
					}
					item.Normalized[current.keyField] = key
				}
				current = next
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return items, nil
}

// readEncryptionKey returns the key the lookup found, or the follow-up call to
// make when a status-only call reports encryption enabled.
func (r *Runtime) readEncryptionKey(
	ctx context.Context,
	request contracts.InventoryRequest,
	credential *contracts.Credential,
	region, nativeID string,
	lookup encryptionKeyLookup,
) (string, *encryptionKeyLookup, error) {
	result, err := r.invoke(ctx, credential, contracts.Invocation{
		ConnectionID: request.ConnectionID,
		Operation:    lookup.operation,
		Scope:        map[string]string{"region": region},
		Parameters:   map[string]any{lookup.parameter: nativeID},
	})
	if err != nil {
		var providerError *contracts.ProviderCallError
		if errors.As(err, &providerError) {
			for _, code := range lookup.noKeyCodes {
				if strings.EqualFold(providerError.Provider.Code, code) {
					return "", nil, nil
				}
			}
		}
		return "", nil, fmt.Errorf("read the encryption key of %s with %s: %w",
			nativeID, strings.TrimPrefix(lookup.operation, "AlibabaCloud."), err)
	}
	if lookup.statusPath != "" {
		status, ok := nonEmptyString(valueAtPath(result.Data, lookup.statusPath))
		if !ok {
			return "", nil, fmt.Errorf("%s response for %s has no %s",
				strings.TrimPrefix(lookup.operation, "AlibabaCloud."), nativeID, lookup.statusPath)
		}
		if !strings.EqualFold(status, "Enabled") {
			return "", nil, nil
		}
	}
	if lookup.keyPath == "" {
		return "", lookup.followUp, nil
	}
	key, _ := nonEmptyString(valueAtPath(result.Data, lookup.keyPath))
	return key, lookup.followUp, nil
}
