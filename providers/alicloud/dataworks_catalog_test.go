package alicloud

import "testing"

func TestDataWorksGetProjectUsesSDKGetTransport(t *testing.T) {
	t.Parallel()

	providerCatalog, err := loadCatalog()
	if err != nil {
		t.Fatal(err)
	}
	operation, ok := providerCatalog.Operation("AlibabaCloud.DataWorks.GetProject")
	if !ok || operation.Call == nil || operation.Call.Method != "GET" ||
		operation.Call.Path != "/" || operation.Call.ParameterPosition != "query" {
		t.Fatalf("DataWorks GetProject transport = %#v", operation.Call)
	}
}

func TestDataWorksGetResourceGroupUsesSDKGetTransport(t *testing.T) {
	t.Parallel()

	providerCatalog, err := loadCatalog()
	if err != nil {
		t.Fatal(err)
	}
	operation, ok := providerCatalog.Operation("AlibabaCloud.DataWorks.GetResourceGroup")
	if !ok || operation.Call == nil || operation.Call.Method != "GET" ||
		operation.Call.Path != "/" || operation.Call.ParameterPosition != "query" {
		t.Fatalf("DataWorks GetResourceGroup transport = %#v", operation.Call)
	}
}

func TestDataWorksProjectActionUsesNumericProjectID(t *testing.T) {
	t.Parallel()

	bundle, err := LoadBundle()
	if err != nil {
		t.Fatal(err)
	}
	for _, compiled := range bundle.Specs {
		if compiled.ResourceKind.NativeType != DataWorksProjectNativeType {
			continue
		}
		deleteAction := compiled.Definition.Actions["delete"]
		if deleteAction.Parameters["Id"] != "resource.normalized.configuration.ProjectId" ||
			deleteAction.Waiter != "terminal" ||
			len(deleteAction.TerminalStates) != 1 ||
			deleteAction.TerminalStates[0] != "Deleting" ||
			deleteAction.Read == nil ||
			deleteAction.Read.Parameters["Id"] != "resource.normalized.configuration.ProjectId" ||
			deleteAction.Read.IdentityPath != "Name" {
			t.Fatalf("DataWorks project action = %#v", deleteAction)
		}
		return
	}
	t.Fatalf("DataWorks project spec %q was not loaded", DataWorksProjectNativeType)
}
