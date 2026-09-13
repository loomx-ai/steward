package azure

import (
	"context"
	"maps"
	"slices"
	"strings"
)

type dataMigrationMember struct {
	id, kind, parent, service, state string
	raw                              map[string]any
	refs                             map[string][]string
}

type dataMigrationForest struct {
	members map[string]dataMigrationMember
	targets map[string]map[string]any
	nodes   map[string]map[string]any
	missing map[string]bool
}

func dataMigrationClassic(kind string) bool {
	return kind == dataMigrationServiceType || strings.HasPrefix(kind, dataMigrationServiceType+"/")
}

func dataMigrationMemberFrom(id, kind string, raw map[string]any) dataMigrationMember {
	member := dataMigrationMember{id: id, kind: kind, parent: dataMigrationParent(id, kind), state: dataMigrationState(kind, raw), raw: raw}
	if dataMigrationClassic(kind) {
		member.service = strings.Join(strings.Split(id, "/")[:9], "/")
	}
	if kind == dataMigrationMongoServiceType || kind == dataMigrationSQLServiceType {
		member.service = id
	}
	if kind == dataMigrationType {
		member.service = strings.ToLower(text(object(raw["properties"])["migrationService"]))
	}
	return member
}

func (c *client) dataMigrationIndex(ctx context.Context, kind, parent string) (map[string]dataMigrationMember, error) {
	collection := c.root() + "/providers/Microsoft.DataMigration/" + last(kind)
	if parent != "" {
		collection = parent + "/" + last(kind)
	}
	values, err := c.listAll(ctx, collection, dataMigrationVersion)
	if err != nil {
		return nil, err
	}
	members := map[string]dataMigrationMember{}
	for _, value := range values {
		raw := object(value)
		id := strings.ToLower(text(raw["id"]))
		if c.dataMigrationIdentity(id, kind) != nil || members[id].id != "" || dataMigrationParent(id, kind) != parent {
			return nil, serviceDenied("invalid_datamigration_index_identity")
		}
		if err := dataMigrationMetadata(id, kind, raw); err != nil {
			return nil, err
		}
		live, err := c.dataMigrationRead(ctx, id, kind)
		if err != nil {
			return nil, err
		}
		if !nativeConfigurationContains(dataMigrationSnapshot(kind, raw), dataMigrationSnapshot(kind, live)) {
			return nil, serviceDenied("datamigration_index_configuration_changed")
		}
		members[id] = dataMigrationMemberFrom(id, kind, live)
	}
	return members, nil
}

func (c *client) dataMigrationClassicForest(ctx context.Context, hints map[string]dataMigrationMember) (dataMigrationForest, error) {
	out := dataMigrationForest{members: map[string]dataMigrationMember{}, targets: map[string]map[string]any{}, nodes: map[string]map[string]any{}, missing: map[string]bool{}}
	roots, err := c.dataMigrationIndex(ctx, dataMigrationServiceType, "")
	if err != nil {
		return out, err
	}
	maps.Copy(out.members, roots)
	// Every previously observed descendant keeps its own GET, including when
	// its service or project no longer appears in a native collection.
	for _, id := range slices.Sorted(maps.Keys(hints)) {
		if out.members[id].id != "" {
			continue
		}
		hint := hints[id]
		raw, err := c.dataMigrationRead(ctx, id, hint.kind)
		if isNotFound(err) {
			out.missing[id] = true
			continue
		}
		if err != nil {
			return out, err
		}
		out.members[id] = dataMigrationMemberFrom(id, hint.kind, raw)
	}
	visited := map[string]bool{}
	for {
		pending := []string{}
		for id, member := range out.members {
			if !visited[id] && len(dataMigrationChildKinds(member.kind)) != 0 {
				pending = append(pending, id)
			}
		}
		if len(pending) == 0 {
			break
		}
		slices.Sort(pending)
		for _, id := range pending {
			visited[id] = true
			for _, kind := range dataMigrationChildKinds(out.members[id].kind) {
				children, err := c.dataMigrationIndex(ctx, kind, id)
				if err != nil {
					return out, err
				}
				for childID, child := range children {
					if previous := out.members[childID]; previous.id != "" && c.privateConfiguration(dataMigrationSnapshot(kind, previous.raw)) != c.privateConfiguration(dataMigrationSnapshot(kind, child.raw)) {
						return out, serviceDenied("datamigration_known_child_changed_during_walk")
					}
					out.members[childID] = child
					delete(out.missing, childID)
				}
			}
		}
	}
	for _, member := range out.members {
		if member.parent != "" && out.members[member.parent].id == "" {
			return out, serviceDenied("datamigration_live_child_missing_parent")
		}
		if location := text(member.raw["location"]); location != "" && resourceRegion(member.raw) != resourceRegion(out.members[member.service].raw) {
			return out, serviceDenied("datamigration_child_region_changed")
		}
	}
	return out, nil
}

func (c *client) dataMigrationTargetRead(ctx context.Context, id string) (map[string]any, error) {
	canonical, kind, err := parseID(id)
	if err != nil || canonical != id || !strings.HasPrefix(id, c.root()+"/") || len(strings.Split(id, "/")) != 9 {
		return nil, serviceDenied("invalid_datamigration_target_identity")
	}
	mapping, ok := findType(kind)
	if !ok {
		switch kind {
		case "microsoft.sql/managedinstances":
			mapping = resourceType{NativeType: "Microsoft.Sql/managedInstances", ReadOperations: []string{"Azure.Microsoft.Sql.ManagedInstances_Get"}}
		case "microsoft.sqlvirtualmachine/sqlvirtualmachines":
			mapping = resourceType{NativeType: "Microsoft.SqlVirtualMachine/sqlVirtualMachines", ReadOperations: []string{"Azure.Microsoft.SqlVirtualMachine.SqlVirtualMachines_Get"}}
		default:
			return nil, serviceDenied("datamigration_target_type_unverified")
		}
	}
	operation, params, err := c.resourceOperation(mapping, id, "GET")
	if err != nil {
		return nil, err
	}
	request, err := bindAzureREST(operation, params)
	if err != nil {
		return nil, err
	}
	res, err := c.requestBody(ctx, request.Method, request.URL, request.Body, request.Headers)
	if err != nil {
		return nil, err
	}
	if res.status != 200 || operationLocation(res.header) != "" || !strings.EqualFold(text(res.data["id"]), id) || !validResponseType(mapping.NativeType, text(res.data["type"])) || !strings.EqualFold(text(res.data["name"]), last(id)) || object(res.data["properties"]) == nil {
		return nil, serviceDenied("datamigration_target_read_changed")
	}
	return res.data, nil
}

func (c *client) dataMigrationMonitor(ctx context.Context, id string) (map[string]any, error) {
	request, err := c.dataMigrationOperation(id, dataMigrationSQLServiceType, "SqlMigrationServices_listMonitoringData", nil)
	if err != nil {
		return nil, err
	}
	res, err := c.requestBody(ctx, request.Method, request.URL, request.Body, request.Headers)
	if err != nil {
		return nil, err
	}
	if res.status != 200 || res.data["error"] != nil || operationLocation(res.header) != "" {
		return nil, serviceDenied("invalid_datamigration_monitor_response")
	}
	name := text(res.data["name"])
	if name == "" || name != strings.TrimSpace(name) || strings.ContainsAny(name, "/\x00\r\n") {
		return nil, serviceDenied("datamigration_runtime_name_missing")
	}
	rows, ok := res.data["nodes"].([]any)
	if !ok {
		return nil, serviceDenied("datamigration_runtime_nodes_missing")
	}
	seen := map[string]bool{}
	for _, value := range rows {
		node := object(value)
		name := text(node["nodeName"])
		if name == "" || name != strings.TrimSpace(name) || strings.ContainsAny(name, "/\x00\r\n") || seen[strings.ToLower(name)] {
			return nil, serviceDenied("invalid_datamigration_runtime_node")
		}
		seen[strings.ToLower(name)] = true
		if jobs, err := batchInteger(node["concurrentJobsRunning"], 32); err != nil || jobs < 0 {
			return nil, serviceDenied("datamigration_runtime_work_unverified")
		}
	}
	return res.data, nil
}

func (c *client) dataMigrationModernForest(ctx context.Context, hints map[string]dataMigrationMember) (dataMigrationForest, error) {
	out := dataMigrationForest{members: map[string]dataMigrationMember{}, targets: map[string]map[string]any{}, nodes: map[string]map[string]any{}, missing: map[string]bool{}}
	for _, kind := range []string{dataMigrationSQLServiceType, dataMigrationMongoServiceType} {
		roots, err := c.dataMigrationIndex(ctx, kind, "")
		if err != nil {
			return out, err
		}
		maps.Copy(out.members, roots)
	}
	for _, id := range slices.Sorted(maps.Keys(hints)) {
		if out.members[id].id != "" {
			continue
		}
		hint := hints[id]
		raw, err := c.dataMigrationRead(ctx, id, hint.kind)
		if isNotFound(err) {
			out.missing[id] = true
			continue
		}
		if err != nil {
			return out, err
		}
		out.members[id] = dataMigrationMemberFrom(id, hint.kind, raw)
	}
	// Mongo supplies a target-scoped list independent of migration-service
	// indexes. SQL supplies only service indexes plus each known migration GET.
	for _, kind := range []string{"Microsoft.DocumentDB/databaseAccounts", "Microsoft.DocumentDB/mongoClusters"} {
		mapping, ok := findType(kind)
		if !ok {
			return out, serviceDenied("datamigration_mongo_target_binding_missing")
		}
		values, err := c.listAll(ctx, c.root()+"/providers/Microsoft.DocumentDB/"+mapping.Collection, mapping.Version)
		if err != nil {
			return out, err
		}
		seen := map[string]bool{}
		for _, value := range values {
			raw := object(value)
			id := strings.ToLower(text(raw["id"]))
			canonical, typ, err := parseID(id)
			if err != nil || canonical != id || !strings.EqualFold(typ, kind) || !strings.HasPrefix(id, c.root()+"/") || len(strings.Split(id, "/")) != 9 || !validResponseType(kind, text(raw["type"])) || seen[id] {
				return out, serviceDenied("invalid_datamigration_mongo_target_index")
			}
			seen[id] = true
			target, err := c.dataMigrationTargetRead(ctx, id)
			if err != nil {
				return out, err
			}
			out.targets[id] = target
			rows, err := c.listAll(ctx, id+"/providers/Microsoft.DataMigration/databaseMigrations", dataMigrationVersion)
			if err != nil {
				return out, err
			}
			if err := c.dataMigrationMergeIndex(ctx, &out, rows, "", id); err != nil {
				return out, err
			}
		}
	}
	visited := map[string]bool{}
	for {
		// A migration's own service reference can reveal a service omitted
		// from LIST. Read it by identity and walk its index before closing.
		for _, member := range maps.Clone(out.members) {
			if member.kind != dataMigrationType {
				continue
			}
			if member.service == "" {
				return out, serviceDenied("datamigration_service_reference_missing")
			}
			_, typ, _ := parseID(member.service)
			kind := dataMigrationKind(typ)
			if c.dataMigrationIdentity(member.service, kind) != nil || !slices.Contains([]string{dataMigrationSQLServiceType, dataMigrationMongoServiceType}, kind) {
				return out, serviceDenied("datamigration_service_outside_connection")
			}
			if out.members[member.service].id == "" {
				raw, err := c.dataMigrationRead(ctx, member.service, kind)
				if err != nil {
					return out, err
				}
				out.members[member.service] = dataMigrationMemberFrom(member.service, kind, raw)
				delete(out.missing, member.service)
			}
		}
		pending := []string{}
		for id, member := range out.members {
			if member.kind != dataMigrationType && !visited[id] {
				pending = append(pending, id)
			}
		}
		if len(pending) == 0 {
			break
		}
		slices.Sort(pending)
		for _, id := range pending {
			visited[id] = true
			rows, err := c.listAll(ctx, id+"/listMigrations", dataMigrationVersion)
			if err != nil {
				return out, err
			}
			if err := c.dataMigrationMergeIndex(ctx, &out, rows, id, ""); err != nil {
				return out, err
			}
			if out.members[id].kind == dataMigrationSQLServiceType {
				out.nodes[id], err = c.dataMigrationMonitor(ctx, id)
				if err != nil {
					return out, err
				}
			}
		}
	}
	for _, member := range out.members {
		if member.kind != dataMigrationType {
			continue
		}
		target, migrationKind := dataMigrationTarget(member.id)
		root := out.members[member.service]
		if root.id == "" || migrationKind != "MongoToCosmosDbMongo" && root.kind != dataMigrationSQLServiceType || migrationKind == "MongoToCosmosDbMongo" && root.kind != dataMigrationMongoServiceType {
			return out, serviceDenied("datamigration_service_kind_changed")
		}
		if out.targets[target] == nil {
			raw, err := c.dataMigrationTargetRead(ctx, target)
			if err != nil {
				return out, err
			}
			out.targets[target] = raw
		}
	}
	return out, nil
}

func (c *client) dataMigrationMergeIndex(ctx context.Context, out *dataMigrationForest, rows []any, service, target string) error {
	seen := map[string]bool{}
	for _, value := range rows {
		raw := object(value)
		id := strings.ToLower(text(raw["id"]))
		if c.dataMigrationIdentity(id, dataMigrationType) != nil || seen[id] {
			return serviceDenied("invalid_datamigration_migration_index")
		}
		seen[id] = true
		if err := dataMigrationMetadata(id, dataMigrationType, raw); err != nil {
			return err
		}
		parent, _ := dataMigrationTarget(id)
		if target != "" && parent != target {
			return serviceDenied("datamigration_index_changed_target")
		}
		live, err := c.dataMigrationRead(ctx, id, dataMigrationType)
		if err != nil {
			return err
		}
		if !nativeConfigurationContains(dataMigrationSnapshot(dataMigrationType, raw), dataMigrationSnapshot(dataMigrationType, live)) {
			return serviceDenied("datamigration_migration_index_changed")
		}
		member := dataMigrationMemberFrom(id, dataMigrationType, live)
		if service != "" && member.service != service {
			return serviceDenied("datamigration_index_changed_service")
		}
		if previous := out.members[id]; previous.id != "" && c.privateConfiguration(dataMigrationSnapshot(dataMigrationType, previous.raw)) != c.privateConfiguration(dataMigrationSnapshot(dataMigrationType, live)) {
			return serviceDenied("datamigration_migration_changed_during_walk")
		}
		out.members[id] = member
		delete(out.missing, id)
	}
	return nil
}

func dataMigrationNodesSnapshot(raw map[string]any) map[string]any {
	result := batchClone(raw)
	for _, value := range array(result["nodes"]) {
		for _, key := range []string{"availableMemoryInMB", "cpuUtilization", "concurrentJobsRunning", "sentBytes", "receivedBytes"} {
			delete(object(value), key)
		}
	}
	slices.SortFunc(array(result["nodes"]), func(a, b any) int { return strings.Compare(text(object(a)["nodeName"]), text(object(b)["nodeName"])) })
	return result
}
