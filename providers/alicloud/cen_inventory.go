package alicloud

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const (
	cenTopologySource       = "cen-topology"
	cenTopologyPageSize     = 50
	cenTopologyMaxResults   = 100
	cenTopologyMaximumPages = 10000
)

type cenTopologyCollector struct {
	runtime    *Runtime
	ctx        context.Context
	request    contracts.InventoryRequest
	region     string
	items      []contracts.InventoryItem
	requestIDs []string
	// DescribeCenRouteMaps identifies the route table but omits its transit
	// router. Retain the ownership discovered from ListTransitRouterRouteTables
	// so route-map console links can address the owning router.
	transitRouterByRouteTable map[string]string
	// Basic transit routers do not expose ListTransitRouterRouteTables. Retain
	// the region-filtered ListTransitRouters result as the fallback owner for
	// route maps when exactly one router exists for the CEN in this region.
	transitRoutersByCEN map[string][]string
	// Enterprise Edition attachment APIs expose the authoritative attachment
	// ID. DescribeCenAttachedChildInstances exposes the same VPC/VBR again
	// without that ID, so suppress the legacy projection when detailed
	// attachment evidence exists.
	detailedChildInstances map[string]struct{}
}

type cenAttachmentOperation struct {
	operation  string
	itemsPath  string
	nativeType string
}

var cenAttachmentOperations = []cenAttachmentOperation{
	{
		operation:  "AlibabaCloud.CEN.ListTransitRouterVpcAttachments",
		itemsPath:  "TransitRouterAttachments",
		nativeType: CENTransitRouterVPCAttachmentNativeType,
	},
	{
		operation:  "AlibabaCloud.CEN.ListTransitRouterVbrAttachments",
		itemsPath:  "TransitRouterAttachments",
		nativeType: CENTransitRouterVBRAttachmentNativeType,
	},
	{
		operation:  "AlibabaCloud.CEN.ListTransitRouterVpnAttachments",
		itemsPath:  "TransitRouterAttachments",
		nativeType: CENTransitRouterVPNAttachmentNativeType,
	},
	{
		operation:  "AlibabaCloud.CEN.ListTransitRouterEcrAttachments",
		itemsPath:  "TransitRouterAttachments",
		nativeType: CENTransitRouterECRAttachmentNativeType,
	},
	{
		operation:  "AlibabaCloud.CEN.ListTransitRouterPeerAttachments",
		itemsPath:  "TransitRouterAttachments",
		nativeType: CENTransitRouterPeerAttachmentNativeType,
	},
}

func (r *Runtime) listCENTopology(
	ctx context.Context,
	request contracts.InventoryRequest,
) (contracts.InventoryBatch, error) {
	region, err := productAPIRegion(request)
	if err != nil {
		return contracts.InventoryBatch{}, fmt.Errorf("Alibaba Cloud CEN topology: %w", err)
	}
	collector := &cenTopologyCollector{
		runtime:                   r,
		ctx:                       ctx,
		request:                   request,
		region:                    region,
		detailedChildInstances:    map[string]struct{}{},
		transitRouterByRouteTable: map[string]string{},
		transitRoutersByCEN:       map[string][]string{},
	}
	if err := collector.collect(); err != nil {
		return contracts.InventoryBatch{}, err
	}
	if request.ResourceKind != nil {
		filtered := make([]contracts.InventoryItem, 0, len(collector.items))
		for _, item := range collector.items {
			if item.NativeType == request.ResourceKind.NativeType {
				filtered = append(filtered, item)
			}
		}
		collector.items = filtered
	}
	sort.Slice(collector.items, func(i, j int) bool {
		if collector.items[i].NativeType != collector.items[j].NativeType {
			return collector.items[i].NativeType < collector.items[j].NativeType
		}
		return collector.items[i].NativeID < collector.items[j].NativeID
	})

	offset, err := cenTopologyOffset(request.Cursor)
	if err != nil {
		return contracts.InventoryBatch{}, err
	}
	if offset > len(collector.items) {
		return contracts.InventoryBatch{}, fmt.Errorf(
			"Alibaba Cloud CEN topology cursor %d is out of range",
			offset,
		)
	}
	limit := request.Limit
	if limit <= 0 {
		limit = 500
	}
	end := offset + limit
	if end > len(collector.items) {
		end = len(collector.items)
	}
	next := ""
	if end < len(collector.items) {
		next = strconv.Itoa(end)
	}
	requestID := ""
	if len(collector.requestIDs) > 0 {
		requestID = collector.requestIDs[0]
	}
	return contracts.InventoryBatch{
		Items:      append([]contracts.InventoryItem(nil), collector.items[offset:end]...),
		NextCursor: next,
		RequestID:  requestID,
		Complete:   next == "",
	}, nil
}

func cenTopologyOffset(cursor string) (int, error) {
	cursor = strings.TrimSpace(cursor)
	if cursor == "" {
		return 0, nil
	}
	offset, err := strconv.Atoi(cursor)
	if err != nil || offset < 0 {
		return 0, fmt.Errorf("invalid Alibaba Cloud CEN topology cursor %q", cursor)
	}
	return offset, nil
}

func (c *cenTopologyCollector) collect() error {
	cens, err := c.pageRecords(
		"AlibabaCloud.CEN.DescribeCens",
		map[string]any{},
		"Cens.Cen",
	)
	if err != nil {
		return err
	}
	for _, cen := range cens {
		cenID := cenScalar(cen, "CenId")
		if cenID == "" {
			return fmt.Errorf("Alibaba Cloud CEN DescribeCens returned an item without CenId")
		}
		if err := c.collectCEN(cenID); err != nil {
			return err
		}
	}
	return nil
}

func (c *cenTopologyCollector) collectCEN(cenID string) error {
	transitRouters, err := c.pageRecords(
		"AlibabaCloud.CEN.ListTransitRouters",
		map[string]any{"CenId": cenID, "RegionId": c.region},
		"TransitRouters",
	)
	if err != nil {
		return err
	}
	for _, transitRouter := range transitRouters {
		regionID := firstCENScalar(transitRouter, "RegionId")
		if regionID != "" && regionID != c.region {
			continue
		}
		c.rememberCENTransitRouter(
			cenID,
			cenScalar(transitRouter, "TransitRouterId"),
		)
		if err := c.collectTransitRouter(cenID, transitRouter); err != nil {
			return err
		}
	}
	if err := c.collectChildInstances(cenID); err != nil {
		return err
	}
	if err := c.collectFlowLogs(cenID); err != nil {
		return err
	}
	return c.collectRouteMaps(cenID)
}

func (c *cenTopologyCollector) rememberCENTransitRouter(
	cenID string,
	transitRouterID string,
) {
	transitRouterID = strings.TrimSpace(transitRouterID)
	if transitRouterID == "" {
		return
	}
	for _, existing := range c.transitRoutersByCEN[cenID] {
		if existing == transitRouterID {
			return
		}
	}
	c.transitRoutersByCEN[cenID] = append(
		c.transitRoutersByCEN[cenID],
		transitRouterID,
	)
}

func (c *cenTopologyCollector) singleCENTransitRouter(cenID string) string {
	if candidates := c.transitRoutersByCEN[cenID]; len(candidates) == 1 {
		return candidates[0]
	}
	return ""
}

func (c *cenTopologyCollector) collectTransitRouter(
	cenID string,
	transitRouter map[string]any,
) error {
	transitRouterID := cenScalar(transitRouter, "TransitRouterId")
	if transitRouterID == "" {
		return fmt.Errorf(
			"Alibaba Cloud CEN ListTransitRouters returned an item without TransitRouterId",
		)
	}
	if !strings.EqualFold(cenScalar(transitRouter, "Type"), "Enterprise") {
		return nil
	}
	for _, attachmentOperation := range cenAttachmentOperations {
		records, err := c.tokenRecords(
			attachmentOperation.operation,
			map[string]any{"TransitRouterId": transitRouterID},
			attachmentOperation.itemsPath,
		)
		if err != nil {
			return err
		}
		for _, record := range records {
			recordRegion := cenAttachmentRegion(record, c.region)
			if attachmentOperation.nativeType == CENTransitRouterPeerAttachmentNativeType {
				if recordRegion != "" && recordRegion != c.region {
					continue
				}
				// A cross-region attachment is returned from both endpoint routers with
				// the same provider ID. Project it only from the lexicographically first
				// region so one physical connection cannot become two cleanup steps.
				peerRegion := firstCENScalar(record, "PeerTransitRouterRegionId")
				if canonicalCENPeerAttachmentRegion(recordRegion, peerRegion) != c.region {
					continue
				}
			}
			normalized := cenAttachmentNormalized(
				record,
				cenID,
				transitRouterID,
				recordRegion,
			)
			if childType, childID := cenAttachmentChildIdentity(
				attachmentOperation.nativeType,
				normalized,
			); childType != "" && childID != "" {
				c.detailedChildInstances[cenChildKey(cenID, childType, childID)] = struct{}{}
			}
			if err := c.addItem(
				attachmentOperation.nativeType,
				cenScalar(record, "TransitRouterAttachmentId"),
				record,
				normalized,
			); err != nil {
				return err
			}
		}
	}

	if err := c.collectTransitRouterCIDRs(cenID, transitRouterID); err != nil {
		return err
	}
	if err := c.collectRouteTables(cenID, transitRouterID); err != nil {
		return err
	}
	if err := c.collectTrafficPolicies(cenID, transitRouterID); err != nil {
		return err
	}
	if cenBool(transitRouter["SupportMulticast"]) {
		if err := c.collectMulticastDomains(cenID, transitRouterID); err != nil {
			return err
		}
	}
	return nil
}

func (c *cenTopologyCollector) collectTransitRouterCIDRs(cenID, transitRouterID string) error {
	records, err := c.records(
		"AlibabaCloud.CEN.ListTransitRouterCidr",
		map[string]any{"TransitRouterId": transitRouterID, "RegionId": c.region},
		"CidrLists",
	)
	if err != nil {
		return err
	}
	for _, record := range records {
		cidr := cenScalar(record, "Cidr")
		if cidr == "" {
			return fmt.Errorf(
				"Alibaba Cloud CEN ListTransitRouterCidr returned an item without Cidr",
			)
		}
		normalized := map[string]any{
			NormalizedCENInstanceIDField:      cenID,
			NormalizedCENTransitRouterIDField: transitRouterID,
			NormalizedCENRegionIDField:        c.region,
			"cidr":                            cidr,
			"name":                            cenScalar(record, "Name"),
			"description":                     cenScalar(record, "Description"),
		}
		if err := c.addItem(
			CENTransitRouterCidrNativeType,
			firstNonEmptyCENScalar(
				cenScalar(record, "TransitRouterCidrId"),
				transitRouterID+"/"+cidr,
			),
			record,
			normalized,
		); err != nil {
			return err
		}
	}
	return nil
}

func (c *cenTopologyCollector) collectRouteTables(cenID, transitRouterID string) error {
	routeTables, err := c.tokenRecords(
		"AlibabaCloud.CEN.ListTransitRouterRouteTables",
		map[string]any{"TransitRouterId": transitRouterID},
		"TransitRouterRouteTables",
	)
	if err != nil {
		return err
	}
	for _, routeTable := range routeTables {
		routeTableID := cenScalar(routeTable, "TransitRouterRouteTableId")
		if routeTableID == "" {
			return fmt.Errorf(
				"Alibaba Cloud CEN ListTransitRouterRouteTables returned an item without TransitRouterRouteTableId",
			)
		}
		associations, err := c.tokenRecords(
			"AlibabaCloud.CEN.ListTransitRouterRouteTableAssociations",
			map[string]any{"TransitRouterRouteTableId": routeTableID},
			"TransitRouterAssociations",
		)
		if err != nil {
			return err
		}
		propagations, err := c.tokenRecords(
			"AlibabaCloud.CEN.ListTransitRouterRouteTablePropagations",
			map[string]any{"TransitRouterRouteTableId": routeTableID},
			"TransitRouterPropagations",
		)
		if err != nil {
			return err
		}
		routeEntries, err := c.tokenRecords(
			"AlibabaCloud.CEN.ListTransitRouterRouteEntries",
			map[string]any{
				"TransitRouterRouteTableId":     routeTableID,
				"TransitRouterRouteEntryStatus": "All",
			},
			"TransitRouterRouteEntries",
		)
		if err != nil {
			return err
		}
		prefixLists, err := c.pageRecords(
			"AlibabaCloud.CEN.ListTransitRouterPrefixListAssociation",
			map[string]any{
				"TransitRouterId":      transitRouterID,
				"TransitRouterTableId": routeTableID,
				"RegionId":             c.region,
			},
			"PrefixLists",
		)
		if err != nil {
			return err
		}
		aggregations, err := c.tokenRecords(
			"AlibabaCloud.CEN.DescribeTransitRouteTableAggregation",
			map[string]any{"TransitRouteTableId": routeTableID},
			"Data",
		)
		if err != nil {
			return err
		}

		raw := cloneCENMap(routeTable)
		raw[NormalizedCENRouteTableAssociationsField] = associations
		raw[NormalizedCENRouteTablePropagationsField] = propagations
		raw[NormalizedCENRouteEntriesField] = routeEntries
		raw[NormalizedCENPrefixListAssociationsField] = prefixLists
		raw[NormalizedCENRouteTableAggregationsField] = aggregations
		normalized := map[string]any{
			NormalizedCENInstanceIDField:                cenID,
			NormalizedCENTransitRouterIDField:           transitRouterID,
			NormalizedCENRegionIDField:                  c.region,
			NormalizedCENTransitRouterRouteTableIDField: routeTableID,
			NormalizedCENRouteTableAssociationsField:    associations,
			NormalizedCENRouteTablePropagationsField:    propagations,
			NormalizedCENRouteEntriesField:              routeEntries,
			NormalizedCENPrefixListAssociationsField:    prefixLists,
			NormalizedCENRouteTableAggregationsField:    aggregations,
			"name":                                      cenScalar(routeTable, "TransitRouterRouteTableName"),
			"state":                                     cenScalar(routeTable, "TransitRouterRouteTableStatus"),
			"routeTableType":                            cenScalar(routeTable, "TransitRouterRouteTableType"),
		}
		c.transitRouterByRouteTable[cenRouteTableKey(cenID, routeTableID)] = transitRouterID
		if err := c.addItem(
			CENTransitRouterRouteTableNativeType,
			routeTableID,
			raw,
			normalized,
		); err != nil {
			return err
		}
	}
	return nil
}

func (c *cenTopologyCollector) collectTrafficPolicies(cenID, transitRouterID string) error {
	markingPolicies, err := c.tokenRecords(
		"AlibabaCloud.CEN.ListTrafficMarkingPolicies",
		map[string]any{"TransitRouterId": transitRouterID},
		"TrafficMarkingPolicies",
	)
	if err != nil {
		return err
	}
	for _, record := range markingPolicies {
		normalized := map[string]any{
			NormalizedCENInstanceIDField:      cenID,
			NormalizedCENTransitRouterIDField: transitRouterID,
			NormalizedCENRegionIDField:        c.region,
			"name":                            cenScalar(record, "TrafficMarkingPolicyName"),
			"state":                           cenScalar(record, "TrafficMarkingPolicyStatus"),
		}
		if err := c.addItem(
			CENTrafficMarkingPolicyNativeType,
			cenScalar(record, "TrafficMarkingPolicyId"),
			record,
			normalized,
		); err != nil {
			return err
		}
	}

	qosPolicies, err := c.tokenRecords(
		"AlibabaCloud.CEN.ListCenInterRegionTrafficQosPolicies",
		map[string]any{"TransitRouterId": transitRouterID},
		"TrafficQosPolicies",
	)
	if err != nil {
		return err
	}
	for _, record := range qosPolicies {
		normalized := map[string]any{
			NormalizedCENInstanceIDField:                cenID,
			NormalizedCENTransitRouterIDField:           transitRouterID,
			NormalizedCENRegionIDField:                  c.region,
			NormalizedCENTransitRouterAttachmentIDField: cenScalar(record, "TransitRouterAttachmentId"),
			"name":   cenScalar(record, "TrafficQosPolicyName"),
			"state":  cenScalar(record, "TrafficQosPolicyStatus"),
			"queues": firstCENValue(record, "TrafficQosQueues", "TrafficQosQueue"),
		}
		if err := c.addItem(
			CENInterRegionTrafficQosPolicyNativeType,
			cenScalar(record, "TrafficQosPolicyId"),
			record,
			normalized,
		); err != nil {
			return err
		}
	}
	return nil
}

func (c *cenTopologyCollector) collectMulticastDomains(cenID, transitRouterID string) error {
	domains, err := c.tokenRecords(
		"AlibabaCloud.CEN.ListTransitRouterMulticastDomains",
		map[string]any{"TransitRouterId": transitRouterID},
		"TransitRouterMulticastDomains",
	)
	if err != nil {
		return err
	}
	for _, domain := range domains {
		domainID := cenScalar(domain, "TransitRouterMulticastDomainId")
		if domainID == "" {
			return fmt.Errorf(
				"Alibaba Cloud CEN ListTransitRouterMulticastDomains returned an item without TransitRouterMulticastDomainId",
			)
		}
		associations, err := c.tokenRecords(
			"AlibabaCloud.CEN.ListTransitRouterMulticastDomainAssociations",
			map[string]any{"TransitRouterMulticastDomainId": domainID},
			"TransitRouterMulticastAssociations",
		)
		if err != nil {
			return err
		}
		raw := cloneCENMap(domain)
		raw["associations"] = associations
		normalized := map[string]any{
			NormalizedCENInstanceIDField:      cenID,
			NormalizedCENTransitRouterIDField: transitRouterID,
			NormalizedCENRegionIDField:        c.region,
			"name":                            cenScalar(domain, "TransitRouterMulticastDomainName"),
			"state":                           cenScalar(domain, "Status"),
			"associations":                    associations,
		}
		if err := c.addItem(
			CENTransitRouterMulticastDomainNativeType,
			domainID,
			raw,
			normalized,
		); err != nil {
			return err
		}
	}
	return nil
}

func (c *cenTopologyCollector) collectChildInstances(cenID string) error {
	records, err := c.pageRecords(
		"AlibabaCloud.CEN.DescribeCenAttachedChildInstances",
		map[string]any{"CenId": cenID, "ChildInstanceRegionId": c.region},
		"ChildInstances.ChildInstance",
	)
	if err != nil {
		return err
	}
	for _, record := range records {
		childID := cenScalar(record, "ChildInstanceId")
		if childID == "" {
			return fmt.Errorf(
				"Alibaba Cloud CEN DescribeCenAttachedChildInstances returned an item without ChildInstanceId",
			)
		}
		recordRegion := firstCENScalar(record, "ChildInstanceRegionId", "RegionId")
		if recordRegion != "" && recordRegion != c.region {
			continue
		}
		normalized := map[string]any{
			NormalizedCENInstanceIDField: cenID,
			NormalizedCENRegionIDField:   firstNonEmptyCENScalar(recordRegion, c.region),
			"childInstanceId":            childID,
			"childInstanceType":          cenScalar(record, "ChildInstanceType"),
			"state":                      cenScalar(record, "Status"),
		}
		if _, detailed := c.detailedChildInstances[cenChildKey(
			cenID,
			cenScalar(normalized, "childInstanceType"),
			childID,
		)]; detailed {
			continue
		}
		if err := c.addItem(
			CENChildInstanceAttachmentNativeType,
			cenID+"/"+childID,
			record,
			normalized,
		); err != nil {
			return err
		}
	}
	return nil
}

func (c *cenTopologyCollector) collectFlowLogs(cenID string) error {
	records, err := c.pageRecords(
		"AlibabaCloud.CEN.DescribeFlowlogs",
		map[string]any{"CenId": cenID, "RegionId": c.region},
		"FlowLogs.FlowLog",
	)
	if err != nil {
		return err
	}
	for _, record := range records {
		recordRegion := firstCENScalar(record, "RegionId")
		if recordRegion != "" && recordRegion != c.region {
			continue
		}
		normalized := map[string]any{
			NormalizedCENInstanceIDField:                cenID,
			NormalizedCENRegionIDField:                  firstNonEmptyCENScalar(recordRegion, c.region),
			NormalizedCENTransitRouterAttachmentIDField: firstCENScalar(record, "TransitRouterAttachmentId", "AttachmentId"),
			"name":  cenScalar(record, "FlowLogName"),
			"state": cenScalar(record, "Status"),
		}
		if err := c.addItem(
			CENFlowLogNativeType,
			cenScalar(record, "FlowLogId"),
			record,
			normalized,
		); err != nil {
			return err
		}
	}
	return nil
}

func (c *cenTopologyCollector) collectRouteMaps(cenID string) error {
	records, err := c.pageRecords(
		"AlibabaCloud.CEN.DescribeCenRouteMaps",
		map[string]any{"CenId": cenID, "CenRegionId": c.region},
		"RouteMaps.RouteMap",
	)
	if err != nil {
		return err
	}
	for _, record := range records {
		recordRegion := firstCENScalar(record, "CenRegionId", "RegionId")
		if recordRegion != "" && recordRegion != c.region {
			continue
		}
		routeTableID := cenScalar(record, "TransitRouterRouteTableId")
		normalized := map[string]any{
			NormalizedCENInstanceIDField: cenID,
			NormalizedCENTransitRouterIDField: firstNonEmptyCENScalar(
				cenScalar(record, "TransitRouterId"),
				c.transitRouterByRouteTable[cenRouteTableKey(cenID, routeTableID)],
				c.singleCENTransitRouter(cenID),
			),
			NormalizedCENRegionIDField:                  firstNonEmptyCENScalar(recordRegion, c.region),
			NormalizedCENTransitRouterRouteTableIDField: routeTableID,
			"state":                    cenScalar(record, "Status"),
			"priority":                 record["Priority"],
			"transmitDirection":        cenScalar(record, "TransmitDirection"),
			"sourceRouteTableIds":      firstCENValue(record, "SourceRouteTableIds"),
			"destinationRouteTableIds": firstCENValue(record, "DestinationRouteTableIds"),
			"originalRouteTableIds":    firstCENValue(record, "OriginalRouteTableIds"),
			"sourceInstanceIds":        firstCENValue(record, "SourceInstanceIds"),
			"destinationInstanceIds":   firstCENValue(record, "DestinationInstanceIds"),
		}
		if err := c.addItem(
			CENRouteMapNativeType,
			cenScalar(record, "RouteMapId"),
			record,
			normalized,
		); err != nil {
			return err
		}
	}
	return nil
}

func (c *cenTopologyCollector) addItem(
	nativeType string,
	nativeID string,
	raw map[string]any,
	normalized map[string]any,
) error {
	nativeID = strings.TrimSpace(nativeID)
	if nativeID == "" {
		return fmt.Errorf("Alibaba Cloud CEN %s item has no native ID", nativeType)
	}
	if normalized == nil {
		normalized = map[string]any{}
	}
	normalized[inventorySourceField] = cenTopologySource
	name := firstNonEmptyCENScalar(
		cenScalar(normalized, "name"),
		firstCENScalar(
			raw,
			"Name",
			"TransitRouterAttachmentName",
			"TransitRouterRouteTableName",
			"TrafficMarkingPolicyName",
			"TrafficQosPolicyName",
		),
	)
	state := firstNonEmptyCENScalar(
		cenScalar(normalized, "state"),
		firstCENScalar(
			raw,
			"Status",
			"TransitRouterAttachmentStatus",
			"TransitRouterRouteTableStatus",
			"TrafficMarkingPolicyStatus",
			"TrafficQosPolicyStatus",
		),
	)
	c.items = append(c.items, contracts.InventoryItem{
		NativeType:   nativeType,
		NativeID:     nativeID,
		ResourceKind: c.runtime.resourceKind(nativeType),
		Scope: contracts.InventoryScope{
			Kind:     c.request.Scope.Kind,
			NativeID: c.request.Scope.NativeID,
			Name:     c.request.Scope.Name,
			Location: c.request.Scope.Location,
		},
		Name:              name,
		State:             state,
		Location:          c.region,
		Tags:              productTags(raw),
		Normalized:        normalized,
		Raw:               raw,
		NativeAliases:     []string{nativeID},
		NetworkReferences: appendUniqueReferences(scalarConfigurationReferences(raw, nil)),
	})
	return nil
}

func (c *cenTopologyCollector) tokenRecords(
	operation string,
	parameters map[string]any,
	itemsPath string,
) ([]map[string]any, error) {
	records := []map[string]any{}
	nextToken := ""
	seenTokens := map[string]struct{}{}
	for page := 0; page < cenTopologyMaximumPages; page++ {
		pageParameters := cloneCENMap(parameters)
		pageParameters["MaxResults"] = cenTopologyMaxResults
		if nextToken != "" {
			pageParameters["NextToken"] = nextToken
		}
		result, err := c.invoke(operation, pageParameters)
		if err != nil {
			return nil, err
		}
		pageRecords, err := cenRecordsAtPath(result.Data, itemsPath, operation)
		if err != nil {
			return nil, err
		}
		records = append(records, pageRecords...)
		nextToken = strings.TrimSpace(firstNonEmptyCENScalar(
			result.NextToken,
			cenScalar(result.Data, "NextToken"),
		))
		if nextToken == "" {
			return records, nil
		}
		if _, duplicate := seenTokens[nextToken]; duplicate {
			return nil, fmt.Errorf(
				"Alibaba Cloud CEN operation %q repeated pagination token %q",
				operation,
				nextToken,
			)
		}
		seenTokens[nextToken] = struct{}{}
	}
	return nil, fmt.Errorf(
		"Alibaba Cloud CEN operation %q exceeded %d pages",
		operation,
		cenTopologyMaximumPages,
	)
}

func (c *cenTopologyCollector) records(
	operation string,
	parameters map[string]any,
	itemsPath string,
) ([]map[string]any, error) {
	result, err := c.invoke(operation, cloneCENMap(parameters))
	if err != nil {
		return nil, err
	}
	return cenRecordsAtPath(result.Data, itemsPath, operation)
}

func (c *cenTopologyCollector) pageRecords(
	operation string,
	parameters map[string]any,
	itemsPath string,
) ([]map[string]any, error) {
	records := []map[string]any{}
	for page := 1; page <= cenTopologyMaximumPages; page++ {
		pageParameters := cloneCENMap(parameters)
		pageParameters["PageNumber"] = page
		pageParameters["PageSize"] = cenTopologyPageSize
		result, err := c.invoke(operation, pageParameters)
		if err != nil {
			return nil, err
		}
		pageRecords, err := cenRecordsAtPath(result.Data, itemsPath, operation)
		if err != nil {
			return nil, err
		}
		records = append(records, pageRecords...)
		total, hasTotal := cenInteger(firstCENValue(result.Data, "TotalCount", "Total"))
		if hasTotal && len(records) >= total {
			return records, nil
		}
		if len(pageRecords) < cenTopologyPageSize {
			return records, nil
		}
	}
	return nil, fmt.Errorf(
		"Alibaba Cloud CEN operation %q exceeded %d pages",
		operation,
		cenTopologyMaximumPages,
	)
}

func (c *cenTopologyCollector) invoke(
	operation string,
	parameters map[string]any,
) (contracts.InvocationResult, error) {
	result, err := c.runtime.Invoke(c.ctx, contracts.Invocation{
		ConnectionID: c.request.ConnectionID,
		Operation:    operation,
		Scope:        map[string]string{"region": c.region},
		Parameters:   parameters,
	})
	if err != nil {
		return contracts.InvocationResult{}, fmt.Errorf(
			"collect Alibaba Cloud CEN topology with %s: %w",
			operation,
			err,
		)
	}
	if requestID := strings.TrimSpace(result.RequestID); requestID != "" {
		c.requestIDs = append(c.requestIDs, requestID)
	}
	return result, nil
}

func cenRecordsAtPath(
	data map[string]any,
	path string,
	operation string,
) ([]map[string]any, error) {
	raw := valueAtPath(data, path)
	if raw == nil {
		if total, ok := cenInteger(firstCENValue(data, "TotalCount", "Total")); ok && total == 0 {
			return []map[string]any{}, nil
		}
	}
	if raw == nil && explicitlyEmptyPathValue(data, path) {
		return []map[string]any{}, nil
	}
	items, ok := productAPIListValue(raw)
	if !ok {
		return nil, fmt.Errorf(
			"Alibaba Cloud CEN operation %q response path %q is not an array",
			operation,
			path,
		)
	}
	records := make([]map[string]any, 0, len(items))
	for index, item := range items {
		record, ok := productAPIResourceMap(item)
		if !ok {
			return nil, fmt.Errorf(
				"Alibaba Cloud CEN operation %q item %d at %q has type %T",
				operation,
				index,
				path,
				item,
			)
		}
		records = append(records, record)
	}
	return records, nil
}

func cenAttachmentNormalized(
	record map[string]any,
	cenID string,
	transitRouterID string,
	region string,
) map[string]any {
	return map[string]any{
		NormalizedCENInstanceIDField:                firstNonEmptyCENScalar(cenScalar(record, "CenId"), cenID),
		NormalizedCENTransitRouterIDField:           firstNonEmptyCENScalar(cenScalar(record, "TransitRouterId"), transitRouterID),
		NormalizedCENTransitRouterAttachmentIDField: cenScalar(record, "TransitRouterAttachmentId"),
		NormalizedCENRegionIDField:                  region,
		NormalizedCENVPCIDField:                     cenScalar(record, "VpcId"),
		NormalizedCENVBRIDField:                     cenScalar(record, "VbrId"),
		NormalizedCENVPNIDField:                     firstCENScalar(record, "VpnId", "VpnConnectionId"),
		NormalizedCENECRIDField:                     cenScalar(record, "EcrId"),
		NormalizedCENPeerTransitRouterIDField:       cenScalar(record, "PeerTransitRouterId"),
		NormalizedCENPeerRegionIDField:              cenScalar(record, "PeerTransitRouterRegionId"),
		NormalizedCENBandwidthPackageIDField:        cenScalar(record, "CenBandwidthPackageId"),
		NormalizedCENZoneMappingsField:              firstCENValue(record, "ZoneMappings"),
		"name":                                      cenScalar(record, "TransitRouterAttachmentName"),
		"state":                                     cenScalar(record, "TransitRouterAttachmentStatus"),
		"resourceType":                              cenScalar(record, "ResourceType"),
	}
}

func canonicalCENPeerAttachmentRegion(region, peerRegion string) string {
	region = strings.TrimSpace(region)
	peerRegion = strings.TrimSpace(peerRegion)
	if region == "" {
		return peerRegion
	}
	if peerRegion == "" || region < peerRegion {
		return region
	}
	return peerRegion
}

func cenAttachmentRegion(record map[string]any, fallback string) string {
	return firstNonEmptyCENScalar(
		firstCENScalar(
			record,
			"VpcRegionId",
			"VbrRegionId",
			"VpnRegionId",
			"EcrRegionId",
			"RegionId",
		),
		fallback,
	)
}

func cenAttachmentChildIdentity(
	nativeType string,
	normalized map[string]any,
) (string, string) {
	switch nativeType {
	case CENTransitRouterVPCAttachmentNativeType:
		return "VPC", cenScalar(normalized, NormalizedCENVPCIDField)
	case CENTransitRouterVBRAttachmentNativeType:
		return "VBR", cenScalar(normalized, NormalizedCENVBRIDField)
	case CENTransitRouterVPNAttachmentNativeType:
		return "VPN", cenScalar(normalized, NormalizedCENVPNIDField)
	case CENTransitRouterECRAttachmentNativeType:
		return "ECR", cenScalar(normalized, NormalizedCENECRIDField)
	default:
		return "", ""
	}
}

func cenChildKey(cenID, childType, childID string) string {
	return strings.Join([]string{
		strings.TrimSpace(cenID),
		strings.ToUpper(strings.TrimSpace(childType)),
		strings.TrimSpace(childID),
	}, "\x00")
}

func cenRouteTableKey(cenID, routeTableID string) string {
	return strings.Join([]string{
		strings.TrimSpace(cenID),
		strings.TrimSpace(routeTableID),
	}, "\x00")
}

func cloneCENMap(value map[string]any) map[string]any {
	if value == nil {
		return map[string]any{}
	}
	payload, err := json.Marshal(value)
	if err == nil {
		var clone map[string]any
		if json.Unmarshal(payload, &clone) == nil && clone != nil {
			return clone
		}
	}
	clone := make(map[string]any, len(value))
	for key, item := range value {
		clone[key] = item
	}
	return clone
}

func firstCENValue(object map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, exists := object[key]; exists && value != nil {
			return value
		}
	}
	return nil
}

func firstCENScalar(object map[string]any, keys ...string) string {
	return strings.TrimSpace(stringValue(firstCENValue(object, keys...)))
}

func cenScalar(object map[string]any, key string) string {
	return strings.TrimSpace(stringValue(object[key]))
}

func firstNonEmptyCENScalar(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func cenBool(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		parsed, _ := strconv.ParseBool(strings.TrimSpace(typed))
		return parsed
	default:
		return false
	}
}

func cenInteger(value any) (int, bool) {
	return integerValue(value)
}
