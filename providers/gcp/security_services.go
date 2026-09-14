package gcp

const (
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
