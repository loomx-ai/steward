package gcp

import "strings"

const (
	securityClusterServiceGet      = "securitycentermanagement.projects.locations.clusters.securityCenterServices.get"
	securityProjectServiceList     = "securitycentermanagement.projects.locations.securityCenterServices.list"
	securityProjectServiceGet      = "securitycentermanagement.projects.locations.securityCenterServices.get"
	securityServiceHost            = "securitycentermanagement.googleapis.com"
	securityServiceSource          = "security-services"
	securityServiceType            = securityServiceHost + "/SecurityCenterService"
	securityBillingType            = securityServiceHost + "/BillingMetadata"
	securityBillingSource          = "security-billing"
	securityOrganizationBillingGet = "securitycentermanagement.organizations.locations.getBillingMetadata"
	securityBillingGet             = "securitycentermanagement.projects.locations.getBillingMetadata"
)

// State strings remain forward compatible; malformed settings must not replace
// the last observation. Missing module entries mean inheritance, not disabled.
func securityServiceSettings(data map[string]any) error {
	check := func(settings map[string]any) error {
		for _, field := range []string{"intendedEnablementState", "effectiveEnablementState"} {
			if value, present := settings[field]; present {
				if _, ok := value.(string); !ok {
					return groupDenied("security_service_state_invalid")
				}
			}
		}
		return nil
	}
	if err := check(data); err != nil {
		return err
	}
	if value, present := data["modules"]; present {
		modules, ok := value.(map[string]any)
		if !ok {
			return groupDenied("security_service_modules_invalid")
		}
		for name, value := range modules {
			settings, ok := value.(map[string]any)
			if name == "" || !ok {
				return groupDenied("security_service_module_invalid")
			}
			if err := check(settings); err != nil {
				return err
			}
		}
	}
	return nil
}

// Billing metadata describes the tier explicitly set on this project or organization/location.
// It does not describe an inherited tier or a subscription's trial/expiry dates.
func (c *client) securityBillingMetadata(data map[string]any, name string) error {
	if c.canonicalName("//"+securityServiceHost+"/"+text(data["name"])) != c.canonicalName("//"+securityServiceHost+"/"+name) {
		return groupDenied("security_billing_identity_changed")
	}
	if value, present := data["billingTier"]; present {
		if _, ok := value.(string); !ok {
			return groupDenied("security_billing_tier_invalid")
		}
	}
	return nil
}

// A GET response must identify the requested service. Project-number aliases are
// canonicalized, but a different cluster, location, service or project is not.
func (c *client) securityServiceMetadata(data map[string]any, name string) error {
	if c.canonicalName("//"+securityServiceHost+"/"+text(data["name"])) != c.canonicalName("//"+securityServiceHost+"/"+name) {
		return groupDenied("security_service_identity_changed")
	}
	if err := checkListCompleteness(data); err != nil {
		return err
	}
	return securityServiceSettings(data)
}

// Validate the whole native page before exposing any record or advancing its token.
func (c *client) securityServiceListMetadata(data map[string]any, parent string) error {
	if err := checkListCompleteness(data); err != nil {
		return err
	}
	if err := cloudNatScalars(data, []string{"nextPageToken"}, nil, nil, nil); err != nil {
		return err
	}
	records, err := cloudNatObjects(data, "securityCenterServices")
	if err != nil {
		return err
	}
	metadata, err := providerData()
	if err != nil {
		return err
	}
	prefix := c.canonicalName("//"+securityServiceHost+"/"+parent) + "/securityCenterServices/"
	seen := map[string]bool{}
	for _, record := range records {
		name := text(record["name"])
		id := c.canonicalName("//" + securityServiceHost + "/" + name)
		if !strings.HasPrefix(id, prefix) || seen[id] {
			return groupDenied("security_service_list_identity_changed")
		}
		if _, _, err := c.securitySettingsOperation(metadata, securityServiceType, name, "GET"); err != nil {
			return err
		}
		if err := c.securityServiceMetadata(record, name); err != nil {
			return err
		}
		seen[id] = true
	}
	return nil
}
