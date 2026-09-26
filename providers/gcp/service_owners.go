package gcp

import (
	"context"
	"strings"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
)

const (
	serviceOwnerSource       = "gcp:service-owner"
	vpcConnectorType         = "vpcaccess.googleapis.com/Connector"
	composerEnvironmentType  = "composer.googleapis.com/Environment"
	workbenchInstanceType    = "notebooks.googleapis.com/Instance"
	indexEndpointType        = "aiplatform.googleapis.com/IndexEndpoint"
	trainingPipelineType     = "aiplatform.googleapis.com/TrainingPipeline"
	caPoolType               = "privateca.googleapis.com/CaPool"
	certificateAuthorityType = "privateca.googleapis.com/CertificateAuthority"
	netappStoragePoolType    = "netapp.googleapis.com/StoragePool"
)

// ServiceOwners records resources that another service creates and deletes in
// the project, and children a native delete refuses to remove.
type ServiceOwners struct{}

func NewServiceOwners() *ServiceOwners { return &ServiceOwners{} }

// HasServiceOwner reports whether assets of this type need ServiceOwners.
func HasServiceOwner(nativeType string) bool {
	switch nativeType {
	case composerEnvironmentType, workbenchInstanceType, caPoolType, netappStoragePoolType:
		return true
	}
	return false
}

func (*ServiceOwners) Contribute(_ context.Context, _ asset.ScopeID, assets []asset.Asset) (governance.Contribution, error) {
	result := governance.Contribution{}
	find := func(owner asset.Asset, nativeType, nativeID string) *asset.Asset {
		for i := range assets {
			candidate := &assets[i]
			if candidate.ClosedAt == nil && candidate.Identity.Provider == owner.Identity.Provider && candidate.Identity.ConnectionID == owner.Identity.ConnectionID && candidate.Identity.Partition == owner.Identity.Partition && candidate.Identity.NativeType == nativeType && candidate.Identity.NativeID == nativeID {
				return candidate
			}
		}
		return nil
	}
	for _, owner := range assets {
		if owner.Identity.Provider != asset.ProviderGCP || owner.ClosedAt != nil {
			continue
		}
		c := &client{project: text(owner.Normalized["project_id"]), number: text(owner.Normalized["project_number"])}
		switch owner.Identity.NativeType {
		case composerEnvironmentType:
			// Deleting an environment deletes the GKE cluster it runs on. The
			// environment bucket is kept and stays an ordinary dependency.
			if cluster := text(object(owner.Normalized["config"])["gkeCluster"]); strings.HasPrefix(cluster, "projects/") {
				if managed := find(owner, clusterType, c.canonicalName("//container.googleapis.com/"+cluster)); managed != nil {
					addServiceOwned(&result, "gcp_composer_environment_cluster", owner, *managed)
				}
			}
		case workbenchInstanceType:
			// A Workbench instance runs on the Compute Engine VM of the same name
			// in its zone, and its delete removes that VM.
			path := strings.TrimPrefix(owner.Identity.NativeID, "//notebooks.googleapis.com/")
			parts := strings.Split(path, "/")
			if len(parts) == 6 && parts[0] == "projects" && parts[2] == "locations" && parts[4] == "instances" {
				vm := "//compute.googleapis.com/projects/" + parts[1] + "/zones/" + parts[3] + "/instances/" + parts[5]
				if managed := find(owner, instanceType, c.canonicalName(vm)); managed != nil {
					addServiceOwned(&result, "gcp_workbench_instance_vm", owner, *managed)
				}
			}
		case caPoolType, netappStoragePoolType:
			// A CA pool is deleted only after all its certificate authorities,
			// and a storage pool only after its volumes. Neither is removed
			// by the parent delete, so each must be selected on its own.
			for _, child := range assets {
				if child.ClosedAt != nil || child.Identity.Provider != owner.Identity.Provider || child.Identity.ConnectionID != owner.Identity.ConnectionID || child.Identity.Partition != owner.Identity.Partition {
					continue
				}
				if (owner.Identity.NativeType == caPoolType && child.Identity.NativeType == certificateAuthorityType && strings.HasPrefix(child.Identity.NativeID, owner.Identity.NativeID+"/certificateAuthorities/")) ||
					(owner.Identity.NativeType == netappStoragePoolType && child.Identity.NativeType == netappVolumeType && netappVolumePool(child) == owner.Identity.NativeID) {
					addServiceRequired(&result, owner, child)
				}
			}
		}
	}
	return result, nil
}

func netappVolumePool(volume asset.Asset) string {
	location, _, ok := strings.Cut(volume.Identity.NativeID, "/volumes/")
	pool := text(volume.Normalized["storagePool"])
	if !ok || pool == "" || strings.Contains(pool, "/") {
		return ""
	}
	return location + "/storagePools/" + pool
}

func addServiceOwned(result *governance.Contribution, kind string, owner, managed asset.Asset) {
	evidence := map[string]any{
		"lifecycle_kind": kind, "resource_type": managed.Identity.NativeType, "instance_id": managed.Identity.NativeID,
		"delete_by_default": true, "retention_supported": false, graph.LifecycleEvidenceControllerDeleteGuaranteed: true,
	}
	result.Bindings = append(result.Bindings, graph.LifecycleBinding{
		ControllerAssetID: owner.ID, ManagedAssetID: managed.ID, Authority: graph.AuthorityAuthoritative,
		Ownership: graph.OwnershipExclusive, CleanupPolicy: graph.CleanupDelegate,
		EvidenceSource: serviceOwnerSource, Evidence: evidence, Confidence: 1,
	})
	result.Relationships = append(result.Relationships, graph.Relationship{
		SourceAssetID: managed.ID, TargetAssetID: owner.ID, Type: graph.RelationshipMemberOf,
		Source: serviceOwnerSource, Evidence: evidence, Confidence: 1,
	})
}

func addServiceRequired(result *governance.Contribution, parent, child asset.Asset) {
	result.Relationships = append(result.Relationships, graph.Relationship{
		SourceAssetID: parent.ID, TargetAssetID: child.ID, Type: graph.RelationshipDependsOn, Source: serviceOwnerSource, Confidence: 1,
		Evidence: map[string]any{
			"resource_type": child.Identity.NativeType, "instance_id": child.Identity.NativeID,
			graph.RelationshipEvidenceRequiredDeletion: true, graph.RelationshipEvidenceAutomaticSelection: false,
			graph.RelationshipEvidenceAuthority: string(graph.AuthorityAuthoritative), graph.RelationshipEvidenceDeletionOrder: graph.DeletionOrderTargetBeforeSource,
		},
	})
}
