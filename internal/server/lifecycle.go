package server

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	"github.com/loomx-ai/steward/providers/alicloud"
	alihooks "github.com/loomx-ai/steward/providers/alicloud/hooks"
	provideraws "github.com/loomx-ai/steward/providers/aws"
	awshooks "github.com/loomx-ai/steward/providers/aws/hooks"
)

type lifecycleRuntimeDirectory interface {
	Resolve(asset.Provider) (contracts.Provider, error)
}

type ackRuntime interface {
	ACK(context.Context, asset.ConnectionID, string) (alicloud.ACKClient, error)
}

type cloudFormationRuntime interface {
	CloudFormation(context.Context, asset.ConnectionID, string) (provideraws.CloudFormationClient, error)
}

type lifecycleContributorResolver struct {
	runtimes lifecycleRuntimeDirectory
}

func newLifecycleContributorResolver(runtimes lifecycleRuntimeDirectory) *lifecycleContributorResolver {
	return &lifecycleContributorResolver{runtimes: runtimes}
}

func (r *lifecycleContributorResolver) ResolveContributors(ctx context.Context, connection asset.CloudConnection, assets []asset.Asset) ([]governance.Contributor, error) {
	if r == nil || r.runtimes == nil {
		return nil, fmt.Errorf("lifecycle contributor resolver requires provider runtimes")
	}
	runtime, err := r.runtimes.Resolve(connection.Provider)
	if err != nil {
		return nil, err
	}
	switch connection.Provider {
	case asset.ProviderAliCloud:
		controllerRegions, err := controllerLocations(connection.Provider, assets)
		if err != nil {
			return nil, err
		}
		contributors := make([]governance.Contributor, 0, len(controllerRegions)+12)
		contributors = append(
			contributors,
			alihooks.NewROSStackTags(),
			alihooks.NewECSDisks(),
			alihooks.NewSystemRouteTables(),
			alihooks.NewNASMountTargets(),
			alihooks.NewCENTopology(),
			alihooks.NewDataWorks(),
			alihooks.NewNLBEIPs(),
			alihooks.NewPrivateLinkEndpoints(),
			alihooks.NewNATIPs(),
			alihooks.NewServiceManagedNetworks(),
			alihooks.NewARMSEnvironments(),
			alihooks.NewConfigurationTopology(),
		)
		if len(controllerRegions) == 0 {
			return contributors, nil
		}
		lifecycle, ok := runtime.(ackRuntime)
		if !ok {
			return nil, fmt.Errorf("Alibaba Cloud runtime does not expose ACK lifecycle discovery")
		}
		for _, location := range controllerRegions {
			client, err := lifecycle.ACK(ctx, connection.ID, location)
			if err != nil {
				return nil, err
			}
			contributors = append(contributors, alihooks.NewACK(client, location))
		}
		return contributors, nil
	case asset.ProviderAWS:
		locations, err := controllerLocations(connection.Provider, assets)
		if err != nil {
			return nil, err
		}
		if len(locations) == 0 {
			return nil, nil
		}
		provider, ok := runtime.(cloudFormationRuntime)
		if !ok {
			return nil, fmt.Errorf("AWS runtime does not expose CloudFormation lifecycle discovery")
		}
		contributors := make([]governance.Contributor, 0, len(locations))
		for _, location := range locations {
			client, err := provider.CloudFormation(ctx, connection.ID, location)
			if err != nil {
				return nil, err
			}
			contributors = append(contributors, awshooks.NewCloudFormation(client, location))
		}
		return contributors, nil
	default:
		return nil, nil
	}
}

func controllerLocations(provider asset.Provider, assets []asset.Asset) ([]string, error) {
	locations := make(map[string]struct{})
	for _, value := range assets {
		if value.Identity.Provider != provider {
			continue
		}
		isController := provider == asset.ProviderAliCloud && value.Identity.NativeType == alicloud.ACKClusterNativeType ||
			provider == asset.ProviderAWS && value.Identity.NativeType == provideraws.CloudFormationStackNativeType
		if !isController {
			continue
		}
		location := strings.TrimSpace(value.Location)
		if location == "" {
			return nil, fmt.Errorf("lifecycle controller %q requires a location", value.ID)
		}
		locations[location] = struct{}{}
	}
	result := make([]string, 0, len(locations))
	for location := range locations {
		result = append(result, location)
	}
	sort.Strings(result)
	return result, nil
}
