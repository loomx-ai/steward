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
	// Rows are checked in order before any read. Rows past the first invalid
	// one are not read, and its error follows the earlier rows' checks.
	var rows []map[string]any
	var invalid error
	seen := map[string]bool{}
	for _, value := range values {
		raw := object(value)
		id := strings.ToLower(text(raw["id"]))
		if c.dataMigrationIdentity(id, kind) != nil || seen[id] || dataMigrationParent(id, kind) != parent {
			invalid = serviceDenied("invalid_datamigration_index_identity")
			break
		}
		seen[id] = true
		if invalid = dataMigrationMetadata(id, kind, raw); invalid != nil {
			break
		}
		rows = append(rows, raw)
	}
	lives, errs := readConcurrently(len(rows), func(i int) (map[string]any, error) {
		return c.dataMigrationRead(ctx, strings.ToLower(text(rows[i]["id"])), kind)
	})
	for i, raw := range rows {
		id := strings.ToLower(text(raw["id"]))
		live, err := lives[i], errs[i]
		if err != nil {
			return nil, err
		}
		if !nativeConfigurationContains(dataMigrationSnapshot(kind, raw), dataMigrationSnapshot(kind, live)) {
			return nil, serviceDenied("datamigration_index_configuration_changed")
		}
		members[id] = dataMigrationMemberFrom(id, kind, live)
	}
	if invalid != nil {
		return nil, invalid
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
	var reads []string
	for _, id := range slices.Sorted(maps.Keys(hints)) {
		if out.members[id].id == "" {
			reads = append(reads, id)
		}
	}
	raws, errs := readConcurrently(len(reads), func(i int) (map[string]any, error) { return c.dataMigrationRead(ctx, reads[i], hints[reads[i]].kind) })
	for i, id := range reads {
		hint := hints[id]
		raw, err := raws[i], errs[i]
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
		// Each parent's indexes are read concurrently and merged in order; a
		// parent's partial result is its kinds indexed before its error.
		indexes, errs := readConcurrently(len(pending), func(i int) ([]map[string]dataMigrationMember, error) {
			var result []map[string]dataMigrationMember
			for _, kind := range dataMigrationChildKinds(out.members[pending[i]].kind) {
				children, err := c.dataMigrationIndex(ctx, kind, pending[i])
				if err != nil {
					return result, err
				}
				result = append(result, children)
			}
			return result, nil
		})
		for i, id := range pending {
			visited[id] = true
			for _, children := range indexes[i] {
				for childID, child := range children {
					if previous := out.members[childID]; previous.id != "" && c.privateConfiguration(dataMigrationSnapshot(child.kind, previous.raw)) != c.privateConfiguration(dataMigrationSnapshot(child.kind, child.raw)) {
						return out, serviceDenied("datamigration_known_child_changed_during_walk")
					}
					out.members[childID] = child
					delete(out.missing, childID)
				}
			}
			if errs[i] != nil {
				return out, errs[i]
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
	var reads []string
	for _, id := range slices.Sorted(maps.Keys(hints)) {
		if out.members[id].id == "" {
			reads = append(reads, id)
		}
	}
	raws, errs := readConcurrently(len(reads), func(i int) (map[string]any, error) { return c.dataMigrationRead(ctx, reads[i], hints[reads[i]].kind) })
	for i, id := range reads {
		hint := hints[id]
		raw, err := raws[i], errs[i]
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
		// Rows are checked in order before any read. Rows past the first invalid
		// one are not read, and its error follows the earlier rows' checks.
		seen := map[string]bool{}
		var ids []string
		var invalid error
		for _, value := range values {
			raw := object(value)
			id := strings.ToLower(text(raw["id"]))
			canonical, typ, err := parseID(id)
			if err != nil || canonical != id || !strings.EqualFold(typ, kind) || !strings.HasPrefix(id, c.root()+"/") || len(strings.Split(id, "/")) != 9 || !validResponseType(kind, text(raw["type"])) || seen[id] {
				invalid = serviceDenied("invalid_datamigration_mongo_target_index")
				break
			}
			seen[id] = true
			ids = append(ids, id)
		}
		type mongoTarget struct {
			target map[string]any
			rows   []any
			listed error
		}
		reads, errs := readConcurrently(len(ids), func(i int) (mongoTarget, error) {
			target, err := c.dataMigrationTargetRead(ctx, ids[i])
			if err != nil {
				return mongoTarget{}, err
			}
			rows, err := c.listAll(ctx, ids[i]+"/providers/Microsoft.DataMigration/databaseMigrations", dataMigrationVersion)
			return mongoTarget{target: target, rows: rows, listed: err}, err
		})
		for i, id := range ids {
			if errs[i] != nil && reads[i].target == nil {
				return out, errs[i]
			}
			out.targets[id] = reads[i].target
			if reads[i].listed != nil {
				return out, reads[i].listed
			}
			if err := c.dataMigrationMergeIndex(ctx, &out, reads[i].rows, "", id); err != nil {
				return out, err
			}
		}
		if invalid != nil {
			return out, invalid
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
	// Migrations are checked in one walk order before any target read; targets
	// past the first invalid migration are not read ahead.
	var migrations []dataMigrationMember
	var targets []string
	var invalid error
	for _, member := range out.members {
		if member.kind != dataMigrationType {
			continue
		}
		target, migrationKind := dataMigrationTarget(member.id)
		root := out.members[member.service]
		if root.id == "" || migrationKind != "MongoToCosmosDbMongo" && root.kind != dataMigrationSQLServiceType || migrationKind == "MongoToCosmosDbMongo" && root.kind != dataMigrationMongoServiceType {
			invalid = serviceDenied("datamigration_service_kind_changed")
			break
		}
		migrations = append(migrations, member)
		if out.targets[target] == nil {
			targets = append(targets, target)
		}
	}
	targetsAhead := readAhead(targets, func(id string) (map[string]any, error) { return c.dataMigrationTargetRead(ctx, id) })
	for _, member := range migrations {
		target, _ := dataMigrationTarget(member.id)
		if out.targets[target] == nil {
			raw, err := targetsAhead(target)
			if err != nil {
				return out, err
			}
			out.targets[target] = raw
		}
	}
	if invalid != nil {
		return out, invalid
	}
	return out, nil
}

func (c *client) dataMigrationMergeIndex(ctx context.Context, out *dataMigrationForest, rows []any, service, target string) error {
	// Rows are checked in order before any read. Rows past the first invalid
	// one are not read, and its error follows the earlier rows' checks.
	seen := map[string]bool{}
	var ids []string
	var raws []map[string]any
	var invalid error
	for _, value := range rows {
		raw := object(value)
		id := strings.ToLower(text(raw["id"]))
		if c.dataMigrationIdentity(id, dataMigrationType) != nil || seen[id] {
			invalid = serviceDenied("invalid_datamigration_migration_index")
			break
		}
		seen[id] = true
		if invalid = dataMigrationMetadata(id, dataMigrationType, raw); invalid != nil {
			break
		}
		parent, _ := dataMigrationTarget(id)
		if target != "" && parent != target {
			invalid = serviceDenied("datamigration_index_changed_target")
			break
		}
		ids, raws = append(ids, id), append(raws, raw)
	}
	lives, errs := readConcurrently(len(ids), func(i int) (map[string]any, error) { return c.dataMigrationRead(ctx, ids[i], dataMigrationType) })
	for i, id := range ids {
		raw, live, err := raws[i], lives[i], errs[i]
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
	return invalid
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
