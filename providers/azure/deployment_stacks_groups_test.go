package azure

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestDeploymentStackGroupClosure(t *testing.T) {
	for _, mode := range []string{"complete", "paginated", "unreviewed", "foreign", "duplicate", "omitted", "forbidden", "member_forbidden", "async", "changed", "group_changed", "managed", "protected", "filter", "foreign_page", "version", "repeated_page", "spaces", "member_missing", "unreviewed_rbac", "unreviewed_diagnostic", "unreviewed_stack", "retained_group", "containing_root", "root_omitted", "root_omitted_second_pass", "root_duplicate", "root_wrong_type", "root_stale_birth", "root_paginated", "retained_containing_root", "neighbor_root"} {
		t.Run(mode, func(t *testing.T) {
			_, root, member := stackGraphAssets(t)
			group := member
			group.ID, group.Identity.NativeType = "group", groupType
			group.Identity.NativeID = "/subscriptions/" + testSubscription + "/resourcegroups/test"
			parent := group.ID
			groupDelete := mode != "retained_group" && mode != "retained_containing_root"
			if !groupDelete {
				parent = root.ID
			}
			c, req := stackDeletePlanFixture(t, false, contracts.ActionImpact{Asset: group, ControllerID: root.ID, Delete: groupDelete}, contracts.ActionImpact{Asset: member, ControllerID: parent, Delete: true})
			containingRoot := mode == "containing_root" || mode == "retained_containing_root" || strings.HasPrefix(mode, "root_")
			if containingRoot {
				req.Asset.Identity.NativeID = group.Identity.NativeID + "/providers/microsoft.resources/deploymentstacks/stack"
			}
			if mode == "neighbor_root" {
				req.Asset.Identity.NativeID = group.Identity.NativeID + "-other/providers/microsoft.resources/deploymentstacks/stack"
			}
			rows := []any{map[string]any{"id": group.Identity.NativeID, "status": "managed", "denyStatus": "none"}}
			if !groupDelete {
				rows = append(rows, map[string]any{"id": member.Identity.NativeID, "status": "managed", "denyStatus": "none"})
			}
			raw := map[string]any{"id": req.Asset.Identity.NativeID, "type": deploymentStackType, "systemData": map[string]any{"createdAt": "2020-02-01T01:01:01.1075056Z"}, "properties": map[string]any{"resources": rows}}
			review, err := c.deploymentStackMemberReview(raw)
			if err != nil {
				t.Fatal(err)
			}
			req.Asset.Normalized[deploymentStackReviewKey] = review
			req.Asset.Normalized[deploymentStackProofKey] = c.deploymentStackProof(req.Asset.Identity.NativeID, req.Asset.Identity.ConnectionID, review)
			vm := map[string]any{"id": member.Identity.NativeID, "type": vmType, "properties": map[string]any{"vmId": "original"}}
			ownGroup := map[string]any{"id": group.Identity.NativeID, "type": groupType, "location": "eastus", "properties": map[string]any{"provisioningState": "Succeeded"}}
			if mode == "managed" {
				ownGroup["managedBy"] = resourceID("Microsoft.Solutions/applications", "manager")
			}
			if mode == "protected" {
				vm["tags"] = map[string]any{"steward/protected": "true"}
			}
			listCalls, groupReads, roots, vmReads := 0, 0, 0, 0
			c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.Method != "GET" {
					t.Fatal("group closure mutated a resource")
				}
				switch strings.ToLower(r.URL.Path) {
				case req.Asset.Identity.NativeID:
					roots++
					return jsonResponse(200, raw, nil), nil
				case group.Identity.NativeID:
					groupReads++
					if mode == "group_changed" && groupReads >= 3 {
						ownGroup["tags"] = map[string]any{"changed": "yes"}
					}
					return jsonResponse(200, ownGroup, nil), nil
				case member.Identity.NativeID:
					vmReads++
					if mode == "member_missing" && vmReads > 1 {
						return jsonResponse(404, map[string]any{}, nil), nil
					}
					if mode == "member_forbidden" {
						return jsonResponse(403, map[string]any{}, nil), nil
					}
					if mode == "changed" && listCalls >= 2 {
						object(vm["properties"])["vmId"] = "replacement"
					}
					return jsonResponse(200, vm, nil), nil
				case group.Identity.NativeID + "/resources":
					listCalls++
					if r.URL.Query().Get("api-version") != resourcesVersion || r.URL.Query().Has("$filter") || r.URL.Query().Has("$top") || r.URL.Query().Has("$expand") {
						t.Fatal("group enumeration was filtered")
					}
					if mode == "forbidden" {
						return jsonResponse(403, map[string]any{}, nil), nil
					}
					entry := map[string]any{"id": member.Identity.NativeID, "type": vmType}
					listed := []any{entry}
					switch mode {
					case "spaces":
						entry["id"] = " " + member.Identity.NativeID
					case "unreviewed_rbac":
						entry["id"] = group.Identity.NativeID + "/providers/Microsoft.Authorization/roleAssignments/" + testTenant
						entry["type"] = "Microsoft.Authorization/roleAssignments"
					case "unreviewed_diagnostic":
						entry["id"] = member.Identity.NativeID + "/providers/Microsoft.Insights/diagnosticSettings/setting"
						entry["type"] = diagnosticSettingsType
					case "unreviewed_stack":
						entry["id"] = group.Identity.NativeID + "/providers/Microsoft.Resources/deploymentStacks/another"
						entry["type"] = deploymentStackType
					case "unreviewed":
						entry["id"] = resourceID(diskType, "unreviewed")
						entry["type"] = diskType
					case "foreign":
						entry["id"] = strings.Replace(member.Identity.NativeID, "/resourcegroups/test/", "/resourcegroups/other/", 1)
					case "duplicate":
						listed = append(listed, entry)
					case "omitted":
						listed = []any{}
					}
					if containingRoot && mode != "root_omitted" && !(mode == "root_omitted_second_pass" && listCalls == 2) {
						stackEntry := map[string]any{"id": req.Asset.Identity.NativeID, "type": deploymentStackType, "systemData": raw["systemData"]}
						if mode == "root_wrong_type" {
							stackEntry["type"] = diskType
						}
						if mode == "root_stale_birth" {
							stackEntry["systemData"] = map[string]any{"createdAt": "2021-02-01T01:01:01.1075056Z"}
						}
						listed = append(listed, stackEntry)
						if mode == "root_duplicate" {
							listed = append(listed, stackEntry)
						}
					}
					response := map[string]any{"value": listed}
					if (mode == "paginated" || mode == "root_paginated") && r.URL.Query().Get("$skiptoken") == "" {
						response["value"] = []any{}
						response["nextLink"] = apiURL(group.Identity.NativeID+"/resources", resourcesVersion) + "&$skiptoken=next"
					}
					if mode == "filter" {
						response["nextLink"] = apiURL(group.Identity.NativeID+"/resources", resourcesVersion) + "&$filter=resourceType%20eq%20%27Microsoft.Compute/virtualMachines%27"
					}
					switch mode {
					case "foreign_page":
						response["nextLink"] = "https://example.com" + group.Identity.NativeID + "/resources?api-version=" + resourcesVersion
					case "version":
						response["nextLink"] = apiURL(group.Identity.NativeID+"/resources", "2022-01-01")
					case "repeated_page":
						response["nextLink"] = apiURL(group.Identity.NativeID+"/resources", resourcesVersion)
					}
					header := http.Header{}
					if mode == "async" {
						header.Set("Azure-AsyncOperation", "https://management.azure.com/unexpected")
					}
					return jsonResponse(200, response, header), nil
				default:
					t.Fatalf("unexpected group endpoint: %s", r.URL.Path)
					return nil, nil
				}
			})
			groups, err := c.deploymentStackObserveGroupClosure(t.Context(), req)
			success := mode == "complete" || mode == "paginated" || mode == "retained_group" || mode == "containing_root" || mode == "root_paginated" || mode == "retained_containing_root" || mode == "neighbor_root"
			if !success {
				expected := map[string]string{"root_omitted": "deployment_stack_group_index_omitted_stack", "root_omitted_second_pass": "deployment_stack_group_index_omitted_stack", "unreviewed": "deployment_stack_group_resource_not_reviewed", "unreviewed_rbac": "deployment_stack_group_resource_not_reviewed", "unreviewed_diagnostic": "deployment_stack_group_resource_not_reviewed", "unreviewed_stack": "deployment_stack_group_resource_not_reviewed", "foreign": "invalid_deployment_stack_group_resource", "duplicate": "invalid_deployment_stack_group_resource", "spaces": "invalid_deployment_stack_group_resource", "omitted": "deployment_stack_group_index_omitted_reviewed_resource", "managed": "deployment_stack_group_has_native_manager", "protected": "azure_protected_tag", "group_changed": "deployment_stack_group_changed_during_read", "changed": "deployment_stack_group_members_changed"}[mode]
				if expected != "" && (err == nil || !strings.Contains(err.Error(), expected)) {
					t.Fatal("wrong rejection", mode, err)
				}

				if err == nil || groups != nil {
					t.Fatal("incomplete group closure accepted", mode, groups, err)
				}
				if (mode == "filter" || mode == "foreign_page" || mode == "version" || mode == "repeated_page") && listCalls != 1 {
					t.Fatal("changed filter reached HTTP", listCalls)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !groupDelete {
				if len(groups) != 0 || listCalls != 0 {
					t.Fatal("retained group acquired a group delete scope", groups, listCalls)
				}
				return
			}
			expectedLists := 2
			if mode == "paginated" || mode == "root_paginated" {
				expectedLists = 4
			}
			expectedRoots := 4
			if containingRoot {
				expectedRoots += 2
			}
			if !slices.Equal(groups, []asset.AssetID{group.ID}) || listCalls != expectedLists || groupReads != 6 || roots != expectedRoots || vmReads != 4 {
				t.Fatal(groups, listCalls, groupReads, roots, vmReads)
			}
		})
	}
}
