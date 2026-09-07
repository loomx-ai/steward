package gcp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/spec"
	"gopkg.in/yaml.v3"
)

const inventorySource = "cloud-asset-inventory"
const actionHook = "gcp.resource"

type resourceType struct {
	NativeType string
	Collection string
	Class      string
	Name       string
	Scopes     []asset.ScopeKind
}

var resourceTypes = []resourceType{
	{"compute.googleapis.com/Instance", "instances", "compute.instance", "Compute Engine instance", regional},
	{"compute.googleapis.com/Disk", "disks", "storage.disk", "Persistent disk", regional},
	{"compute.googleapis.com/RegionDisk", "disks", "storage.disk", "Regional persistent disk", regional},
	{"compute.googleapis.com/Snapshot", "snapshots", "storage.snapshot", "Disk snapshot", global},
	{"compute.googleapis.com/Image", "images", "compute.image", "Compute image", global},
	{"compute.googleapis.com/InstanceTemplate", "instanceTemplates", "compute.instance_template", "Instance template", both},
	{"compute.googleapis.com/Network", "networks", "network.vpc", "VPC network", global},
	{"compute.googleapis.com/Subnetwork", "subnetworks", "network.subnet", "VPC subnet", regional},
	{"compute.googleapis.com/Firewall", "firewalls", "network.security_group", "VPC firewall rule", global},
	{"compute.googleapis.com/Route", "routes", "network.route", "VPC route", global},
	{"compute.googleapis.com/Router", "routers", "network.router", "Cloud Router", regional},
	{"compute.googleapis.com/Address", "addresses", "network.public_ip", "Regional IP address", regional},
	{"compute.googleapis.com/GlobalAddress", "addresses", "network.public_ip", "Global IP address", global},
	{"compute.googleapis.com/ForwardingRule", "forwardingRules", "network.load_balancer", "Regional forwarding rule", regional},
	{"compute.googleapis.com/GlobalForwardingRule", "forwardingRules", "network.load_balancer", "Global forwarding rule", global},
	{"compute.googleapis.com/BackendService", "backendServices", "network.load_balancer_backend", "Backend service", both},
	{"compute.googleapis.com/HealthCheck", "healthChecks", "network.health_check", "Health check", both},
	{"compute.googleapis.com/UrlMap", "urlMaps", "network.url_map", "URL map", both},
	{"compute.googleapis.com/TargetHttpProxy", "targetHttpProxies", "network.proxy", "Target HTTP proxy", both},
	{"compute.googleapis.com/TargetHttpsProxy", "targetHttpsProxies", "network.proxy", "Target HTTPS proxy", both},
	{"compute.googleapis.com/SslCertificate", "sslCertificates", "security.certificate", "SSL certificate", both},
	{"storage.googleapis.com/Bucket", "buckets", "storage.bucket", "Cloud Storage bucket", global},
	{"pubsub.googleapis.com/Topic", "topics", "messaging.topic", "Pub/Sub topic", global},
	{"pubsub.googleapis.com/Subscription", "subscriptions", "messaging.subscription", "Pub/Sub subscription", global},
	{"sqladmin.googleapis.com/Instance", "instances", "database.instance", "Cloud SQL instance", regional},
	{"container.googleapis.com/Cluster", "clusters", "container.cluster", "GKE cluster", regional},
	{"run.googleapis.com/Service", "services", "compute.service", "Cloud Run service", regional},
	{"artifactregistry.googleapis.com/Repository", "repositories", "storage.registry", "Artifact Registry repository", regional},
	{"secretmanager.googleapis.com/Secret", "secrets", "security.secret", "Secret Manager secret", both},
}
var regional = []asset.ScopeKind{asset.ScopeRegion}
var global = []asset.ScopeKind{asset.ScopeGlobal}
var both = []asset.ScopeKind{asset.ScopeRegion, asset.ScopeGlobal}

func findType(nativeType string) (resourceType, bool) {
	for _, kind := range resourceTypes {
		if kind.NativeType == nativeType {
			return kind, true
		}
	}
	return resourceType{}, false
}

func compileBundle() (spec.Bundle, error) {
	payload, _ := json.Marshal(resourceTypes)
	digest := sha256.Sum256(payload)
	c := catalog.Catalog{Provider: asset.ProviderGCP, Source: catalog.Source{Format: "google-discovery", URI: "https://www.googleapis.com/discovery/v1/apis", Checksum: hex.EncodeToString(digest[:]), Generator: "steward/gcp"}}
	c.Operations = []catalog.Operation{{ID: "gcp.resources.get", Name: "GetResource"}, {ID: "gcp.resources.delete", Name: "DeleteResource", Destructive: true}}
	for _, kind := range resourceTypes {
		c.ResourceTypes = append(c.ResourceTypes, catalog.ResourceType{NativeType: kind.NativeType, Class: kind.Class, DisplayName: kind.Name, ScopeKinds: kind.Scopes})
	}
	sources := make([][]byte, 0, len(resourceTypes))
	for _, kind := range resourceTypes {
		definition := spec.ResourceKindSpec{Schema: spec.SchemaIdentifier, Kind: "ResourceKind", Metadata: spec.Metadata{Provider: asset.ProviderGCP, NativeType: kind.NativeType, Class: kind.Class}, Scope: spec.ScopeSpec{Kind: kind.Scopes[0]}, Presentation: spec.PresentationSpec{DisplayNames: map[string]string{"en-US": kind.Name}}, Discovery: spec.DiscoverySpec{Source: inventorySource, Detail: &spec.DetailSpec{Operation: "gcp.resources.get", ItemsPath: "resource", IdentityPath: "name"}}, Extensions: spec.Extensions{Hook: actionHook}, Actions: map[string]spec.ActionSpec{"delete": {Operation: "gcp.resources.delete", Idempotency: "readback", Waiter: "gcp_operation", Readback: "gcp_absent"}}}
		definition.Fields = map[string]spec.FieldSpec{"projectId": {Path: "project_id", Type: spec.PropertyString}, "zoneId": {Path: "zone_id", Type: spec.PropertyString}}
		definition.Presentation.ConsoleLinkTemplate = "https://console.cloud.google.com/home/dashboard?project={projectId}"
		switch kind.NativeType {
		case "compute.googleapis.com/Instance":
			definition.Presentation.ConsoleLinkTemplate = "https://console.cloud.google.com/compute/instancesDetail/zones/{zoneId}/instances/{nativeId|suffix:/}?project={projectId}"
		case "storage.googleapis.com/Bucket":
			definition.Presentation.ConsoleLinkTemplate = "https://console.cloud.google.com/storage/browser/{nativeId|suffix:/}?project={projectId}"
		case "pubsub.googleapis.com/Topic":
			definition.Presentation.ConsoleLinkTemplate = "https://console.cloud.google.com/cloudpubsub/topic/detail/{nativeId|suffix:/}?project={projectId}"
		case "pubsub.googleapis.com/Subscription":
			definition.Presentation.ConsoleLinkTemplate = "https://console.cloud.google.com/cloudpubsub/subscription/detail/{nativeId|suffix:/}?project={projectId}"
		case "sqladmin.googleapis.com/Instance":
			definition.Presentation.ConsoleLinkTemplate = "https://console.cloud.google.com/sql/instances/{nativeId|suffix:/}/overview?project={projectId}"
		case "run.googleapis.com/Service":
			definition.Presentation.ConsoleLinkTemplate = "https://console.cloud.google.com/run/detail/{regionId}/{nativeId|suffix:/}/metrics?project={projectId}"
		}
		// All references use canonical full resource names. Missing targets remain
		// unresolved and continue to block unsafe dependency assumptions.
		for _, target := range resourceTypes {
			if target.NativeType == kind.NativeType {
				continue
			}
			relation := "uses"
			if kind.NativeType == "compute.googleapis.com/Instance" && target.Class == "storage.disk" {
				relation = "attached_to"
			}
			if target.Class == "network.vpc" || target.Class == "network.subnet" {
				relation = "member_of"
			}
			definition.Relationships = append(definition.Relationships, spec.RelationshipSpec{Type: relation, TargetType: target.NativeType, TargetIDPath: referenceKey(target.NativeType)})
		}
		if kind.NativeType == "container.googleapis.com/Cluster" {
			definition.Actions = nil
		}
		source, err := yaml.Marshal(definition)
		if err != nil {
			return spec.Bundle{}, err
		}
		sources = append(sources, source)
	}
	return spec.CompileBundle(sources, c, spec.HookRegistry{actionHook: {spec.HookPreflight, spec.HookAction, spec.HookWaiter, spec.HookReadback}})
}

func referenceKey(nativeType string) string {
	return "refs_" + strings.NewReplacer(".", "_", "/", "_").Replace(nativeType)
}

// Canonical names, unlike short resource names, are unique across all GCP
// regions and zones in a project. Only known collections become action URLs.
func (c *client) resourceURL(kind resourceType, nativeID string) (string, error) {
	host := strings.Split(kind.NativeType, "/")[0]
	prefix := "//" + host + "/"
	if !strings.HasPrefix(nativeID, prefix) {
		return "", fmt.Errorf("invalid GCP resource identity")
	}
	path := strings.TrimPrefix(nativeID, prefix)
	parts := strings.Split(path, "/")
	for _, part := range parts {
		if !segmentPattern.MatchString(part) || part == "." || part == ".." {
			return "", fmt.Errorf("invalid GCP resource path")
		}
	}
	if host == "storage.googleapis.com" {
		if len(parts) == 1 {
			return "https://storage.googleapis.com/storage/v1/b/" + parts[0], nil
		}
		// CAI uses //storage.googleapis.com/BUCKET; accept the documented IAM
		// full resource name too when resolving explicit references.
		return "", fmt.Errorf("invalid Cloud Storage bucket identity")
	}
	if len(parts) < 4 || parts[0] != "projects" || (parts[1] != c.project && parts[1] != c.number) {
		return "", fmt.Errorf("GCP resource belongs to another project")
	}
	if parts[len(parts)-2] != kind.Collection {
		return "", fmt.Errorf("GCP resource collection mismatch")
	}
	version := "v1"
	switch host {
	case "compute.googleapis.com":
		if !((len(parts) == 5 && parts[2] == "global") || (len(parts) == 6 && (parts[2] == "regions" || parts[2] == "zones"))) {
			return "", fmt.Errorf("invalid Compute resource scope")
		}
		version = "compute/v1"
	case "sqladmin.googleapis.com":
		if len(parts) != 4 {
			return "", fmt.Errorf("invalid Cloud SQL identity")
		}
		version = "sql/v1beta4"
	case "secretmanager.googleapis.com":
		if len(parts) != 4 && !(len(parts) == 6 && parts[2] == "locations") {
			return "", fmt.Errorf("invalid Secret Manager identity")
		}
	case "pubsub.googleapis.com":
		if len(parts) != 4 {
			return "", fmt.Errorf("invalid project resource identity")
		}
	case "container.googleapis.com", "artifactregistry.googleapis.com", "run.googleapis.com":
		if len(parts) != 6 || parts[2] != "locations" {
			return "", fmt.Errorf("invalid regional resource identity")
		}
		if host == "run.googleapis.com" {
			version = "v2"
		}
	default:
		return "", fmt.Errorf("unsupported Google API")
	}
	return "https://" + host + "/" + version + "/" + path, nil
}
