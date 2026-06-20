package store

// Migrate 建表/补列（开发期用 AutoMigrate；生产迁移策略后续在 controller 子规格细化）。
func (s *Store) Migrate() error {
	return s.db.AutoMigrate(
		&Node{}, &Lease{}, &EnrollKey{}, &Cert{}, &RelayLink{}, &ACLPolicy{},
		&CARoot{}, &RelayInfo{},
		&QualityEdge{}, &TopoResult{}, &IngressRow{},
		&AdminCredential{},
		&SystemSecret{},
		&NodeAlias{}, &PublishedRoute{}, &DiscoveredMapping{}, &DNSSettings{},
		&SplitRuleRow{}, &GeoIPMeta{},
	)
}
