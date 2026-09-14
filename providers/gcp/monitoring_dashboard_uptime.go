package gcp

import "strings"

func (p monitoringConsumer) dashboardReference(check string) monitoringReference {
	route := monitoringNoReference
	if p.Local {
		route = monitoringHasReference
	}
	for _, sink := range p.LogRoutes {
		route = monitoringOr(route, loggingRouteReference(sink, check))
	}
	return monitoringDashboardReference(p.Data, func(kind string, data map[string]any) monitoringReference {
		switch kind {
		case "TimeSeriesFilter", "RatioPart":
			if !p.Metrics {
				return monitoringNoReference
			}
			filter := text(data["filter"])
			if strings.Contains(filter, "${") {
				return monitoringUnresolvedReference
			}
			return monitoringFilterReference(filter, check)
		case "TimeSeriesQuery":
			for _, key := range []string{"prometheusQuery", "timeSeriesQueryLanguage", "opsAnalyticsQuery", "traceQuery"} {
				if _, ok := data[key]; ok && (p.Metrics || route != monitoringNoReference) {
					return monitoringUnresolvedReference
				}
			}
		case "LogsPanel":
			filter := text(data["filter"])
			if strings.Contains(filter, "${") {
				return monitoringUnresolvedReference
			}
			match := loggingFilterReference(filter, check)
			sources := route
			if raw, exists := data["resourceNames"]; exists {
				names, ok := raw.([]any)
				if !ok {
					return monitoringUnresolvedReference
				}
				if len(names) > 0 {
					sources = monitoringNoReference
					for _, name := range names {
						value := text(name)
						switch {
						case value == "projects/"+p.Project || value == "projects/"+p.ProjectNumber:
							sources = monitoringOr(sources, route)
						case value == "projects/"+p.MonitoredProject || value == "projects/"+p.MonitoredNumber:
							sources = monitoringHasReference
						default:
							// Explicit foreign projects and log views need their own routing/view
							// review. A host project's empty route cannot prove their data absent.
							sources = monitoringOr(sources, monitoringUnresolvedReference)
						}
					}
				}
			}
			return monitoringAnd(sources, match)
		}
		return monitoringNoReference
	})
}
