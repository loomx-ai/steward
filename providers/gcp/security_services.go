package gcp

const (
	securityServiceHost = "securitycentermanagement.googleapis.com"
	securityServiceType = securityServiceHost + "/SecurityCenterService"
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
