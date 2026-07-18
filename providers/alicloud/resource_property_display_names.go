package alicloud

var commonResourcePropertyDisplayNames = map[string]map[string]string{
	"accountId":             {"zh-CN": "账号 ID", "en-US": "Account ID"},
	"resourceGroupId":       {"zh-CN": "资源组 ID", "en-US": "Resource Group ID"},
	"createTime":            {"zh-CN": "创建时间", "en-US": "Creation Time"},
	"expireTime":            {"zh-CN": "到期时间", "en-US": "Expiration Time"},
	"zoneId":                {"zh-CN": "可用区", "en-US": "Zone"},
	"deleted":               {"zh-CN": "已删除", "en-US": "Deleted"},
	"vpc_id":                {"zh-CN": "所属专有网络", "en-US": "VPC"},
	"vswitch_id":            {"zh-CN": "所属交换机", "en-US": "vSwitch"},
	"zone_id":               {"zh-CN": "可用区", "en-US": "Zone"},
	"security_group_ids":    {"zh-CN": "安全组 ID", "en-US": "Security Group IDs"},
	"network_interface_ids": {"zh-CN": "弹性网卡 ID", "en-US": "Network Interface IDs"},
	"attached_instance_id":  {"zh-CN": "挂载实例 ID", "en-US": "Attached Instance ID"},
	"object_count":          {"zh-CN": "对象数量", "en-US": "Object Count"},
}

func cloneCommonResourcePropertyDisplayNames() map[string]map[string]string {
	cloned := make(map[string]map[string]string, len(commonResourcePropertyDisplayNames))
	for field, labels := range commonResourcePropertyDisplayNames {
		cloned[field] = make(map[string]string, len(labels))
		for locale, label := range labels {
			cloned[field][locale] = label
		}
	}
	return cloned
}
