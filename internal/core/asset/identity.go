package asset

import (
	"errors"
	"strings"
)

func NewIdentity(provider, partition string, connectionID ConnectionID, nativeType, nativeID string) (Identity, error) {
	identity := Identity{
		Provider:     Provider(strings.TrimSpace(provider)),
		Partition:    strings.TrimSpace(partition),
		ConnectionID: ConnectionID(strings.TrimSpace(string(connectionID))),
		NativeType:   strings.TrimSpace(nativeType),
		NativeID:     strings.TrimSpace(nativeID),
	}
	if err := identity.Validate(); err != nil {
		return Identity{}, err
	}
	return identity, nil
}

func (i Identity) Validate() error {
	if strings.TrimSpace(string(i.Provider)) == "" {
		return errors.New("asset identity requires provider")
	}
	if strings.TrimSpace(i.Partition) == "" {
		return errors.New("asset identity requires partition")
	}
	if strings.TrimSpace(string(i.ConnectionID)) == "" {
		return errors.New("asset identity requires connection")
	}
	if strings.TrimSpace(i.NativeType) == "" {
		return errors.New("asset identity requires native type")
	}
	if strings.TrimSpace(i.NativeID) == "" {
		return errors.New("asset identity requires native id")
	}
	return nil
}

func (i Identity) Key() string {
	parts := []string{
		string(i.Provider),
		i.Partition,
		string(i.ConnectionID),
		i.NativeType,
		i.NativeID,
	}
	// GCP native IDs are full resource names including project and zone/region.
	if scopeKey := strings.TrimSpace(i.ScopeKey); scopeKey != "" && i.Provider != ProviderGCP {
		parts = append(parts, scopeKey)
	}
	return strings.Join(parts, "\x00")
}

func (o Observation) Validate() error {
	if strings.TrimSpace(string(o.AssetID)) == "" {
		return errors.New("observation requires asset")
	}
	if strings.TrimSpace(string(o.ScanRunID)) == "" {
		return errors.New("observation requires scan run")
	}
	if strings.TrimSpace(string(o.ScanShardID)) == "" {
		return errors.New("observation requires scan shard")
	}
	if o.ObservedAt.IsZero() {
		return errors.New("observation requires observed at")
	}
	if strings.TrimSpace(o.Source) == "" {
		return errors.New("observation requires source")
	}
	return nil
}
