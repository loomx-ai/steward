package azure

import (
	"slices"
	"strings"
)

// Only native fields with resource-reference semantics participate in the
// graph. SQL text, connection strings, clientData and arbitrary input objects
// never become dependencies merely because they contain an ARM-looking value.
func dataMigrationReferences(member dataMigrationMember, targetRaw map[string]any) (map[string][]string, error) {
	refs := map[string][]string{}
	add := func(value any, expected string) error {
		if value == nil || value == "" {
			return nil
		}
		id, kind, err := parseID(text(value))
		if err != nil || value != text(value) || expected != "" && !strings.EqualFold(kind, expected) {
			return serviceDenied("invalid_datamigration_native_reference")
		}
		if mapping, ok := findType(kind); ok {
			kind = mapping.NativeType
		}
		refs[kind] = append(refs[kind], id)
		return nil
	}
	props := object(member.raw["properties"])
	if member.parent != "" {
		_, kind, _ := parseID(member.parent)
		if err := add(member.parent, kind); err != nil {
			return nil, err
		}
	}
	if member.kind == dataMigrationServiceType {
		for key, kind := range map[string]string{"virtualSubnetId": subnetType, "virtualNicId": nicType} {
			if err := add(props[key], kind); err != nil {
				return nil, err
			}
		}
	}
	if member.kind == dataMigrationType {
		target, kind := dataMigrationTarget(member.id)
		_, targetKind, _ := parseID(target)
		if targetRaw == nil {
			return nil, serviceDenied("datamigration_inventory_target_missing")
		}
		if err := add(target, targetKind); err != nil {
			return nil, err
		}
		// The SQL routes name this path parameter targetDbName in the native
		// contract; it denotes a database, not an arbitrary migration label.
		if kind == "SqlDb" || kind == "SqlMi" {
			if err := add(target+"/databases/"+last(member.id), targetKind+"/databases"); err != nil {
				return nil, err
			}
		}
		// These target types currently supply native GET context, but have no
		// inventory rule to carry their network/compute edge into scope closure.
		switch strings.ToLower(targetKind) {
		case "microsoft.sql/managedinstances":
			if err := add(object(targetRaw["properties"])["subnetId"], subnetType); err != nil {
				return nil, err
			}
		case "microsoft.sqlvirtualmachine/sqlvirtualmachines":
			if err := add(object(targetRaw["properties"])["virtualMachineResourceId"], vmType); err != nil {
				return nil, err
			}
		}
		if err := add(member.service, ""); err != nil {
			return nil, err
		}
		if kind != "MongoToCosmosDbMongo" {
			backup := object(props["backupConfiguration"])
			blob := object(object(backup["sourceLocation"])["azureBlob"])
			for _, location := range []map[string]any{object(backup["targetLocation"]), object(object(backup["sourceLocation"])["azureBlob"])} {
				if err := add(location["storageAccountResourceId"], storageType); err != nil {
					return nil, err
				}
			}
			for id := range object(object(blob["identity"])["userAssignedIdentities"]) {
				if err := add(id, rbacUserIdentityType); err != nil {
					return nil, err
				}
			}
		}
	}
	if member.kind == dataMigrationProjectType || member.kind == dataMigrationTaskType || member.kind == dataMigrationServiceTaskType {
		input := props
		if member.kind != dataMigrationProjectType {
			input = object(props["input"])
		}
		for _, key := range []string{"sourceConnectionInfo", "targetConnectionInfo"} {
			connection := object(input[key])
			switch connection["type"] {
			case "SqlConnectionInfo":
				if err := add(connection["resourceId"], ""); err != nil {
					return nil, err
				}
			case "MiSqlConnectionInfo":
				if err := add(connection["managedInstanceResourceId"], "Microsoft.Sql/managedInstances"); err != nil {
					return nil, err
				}
			}
		}
		if member.kind != dataMigrationProjectType && slices.Contains([]string{"Migrate.SqlServer.AzureSqlDbMI.Sync.LRS", "ValidateMigrationInput.SqlServer.AzureSqlDbMI.Sync.LRS"}, text(props["taskType"])) {
			if err := add(input["storageResourceId"], storageType); err != nil {
				return nil, err
			}
		}
		if member.kind == dataMigrationTaskType && props["taskType"] == "MigrateSchemaSqlServerSqlDb" {
			for _, database := range array(input["selectedDatabases"]) {
				setting := object(object(database)["schemaSetting"])
				if setting["schemaOption"] != "UseStorageFile" {
					continue
				}
				file := text(setting["fileId"])
				if file == "" {
					return nil, serviceDenied("datamigration_schema_file_unverified")
				}
				if !strings.HasPrefix(file, "/") {
					file = member.parent + "/files/" + file
				}
				if err := add(file, dataMigrationFileType); err != nil {
					return nil, err
				}
			}
		}
	}
	for kind, ids := range refs {
		slices.Sort(ids)
		refs[kind] = slices.Compact(ids)
	}
	return refs, nil
}
