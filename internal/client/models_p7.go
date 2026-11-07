package client

// Phase 7 API models: object storage, load balancers, hosted DNS and snapshot schedules
// (ADRs 0038-0041). Field names follow platform/control-plane/openapi.json.

// -- object storage -------------------------------------------------------------------------

type Storage struct {
	ProjectID   string `json:"project_id"`
	Endpoint    string `json:"endpoint"`
	Region      string `json:"region"`
	Buckets     int64  `json:"buckets"`
	AccessKeys  int64  `json:"access_keys"`
	StoredBytes int64  `json:"stored_bytes"`
	QuotaGiB    int64  `json:"quota_gib"`
	State       string `json:"state"`
}

type BucketIn struct {
	Name        string            `json:"name"`
	Visibility  string            `json:"visibility"`
	MaxSizeGiB  *int64            `json:"max_size_gib"`
	Description string            `json:"description"`
	Tags        map[string]string `json:"tags"`
}

type BucketPatch struct {
	Description *string            `json:"description,omitempty"`
	Tags        *map[string]string `json:"tags,omitempty"`
	Visibility  *string            `json:"visibility,omitempty"`
	// MaxSizeGiB 0 removes the bucket's own cap.
	MaxSizeGiB *int64 `json:"max_size_gib,omitempty"`
}

type Bucket struct {
	Meta
	Visibility  string `json:"visibility"`
	MaxSizeGiB  *int64 `json:"max_size_gib"`
	StoredBytes int64  `json:"stored_bytes"`
	ObjectCount int64  `json:"object_count"`
	Endpoint    string `json:"endpoint"`
	URL         string `json:"url"`
}

type StorageKeyIn struct {
	Name             string            `json:"name"`
	ServiceAccountID *string           `json:"service_account_id,omitempty"`
	Description      string            `json:"description"`
	Tags             map[string]string `json:"tags"`
}

type StorageKeyPatch struct {
	Description *string            `json:"description,omitempty"`
	Tags        *map[string]string `json:"tags,omitempty"`
	Status      *string            `json:"status,omitempty"`
}

type StorageKey struct {
	Meta
	AccessKeyID     string `json:"access_key_id"`
	Holder          string `json:"holder"`
	Status          string `json:"status"`
	EffectiveStatus string `json:"effective_status"`
	Endpoint        string `json:"endpoint"`
	// Only in the create answer.
	SecretAccessKey *string `json:"secret_access_key"`
	Region          string  `json:"region"`
}

// -- load balancers -------------------------------------------------------------------------

type HealthCheck struct {
	Protocol           string `json:"protocol"`
	Port               *int64 `json:"port"`
	Path               string `json:"path"`
	Matcher            string `json:"matcher"`
	IntervalSeconds    int64  `json:"interval_seconds"`
	TimeoutSeconds     int64  `json:"timeout_seconds"`
	HealthyThreshold   int64  `json:"healthy_threshold"`
	UnhealthyThreshold int64  `json:"unhealthy_threshold"`
}

type LBTarget struct {
	ID            string `json:"id"`
	VMID          string `json:"vm_id"`
	Port          *int64 `json:"port"`
	EffectivePort int64  `json:"effective_port"`
	Health        string `json:"health"`
	HealthReason  string `json:"health_reason"`
}

type TargetGroup struct {
	ID            string      `json:"id"`
	Name          string      `json:"name"`
	Protocol      string      `json:"protocol"`
	Port          int64       `json:"port"`
	Algorithm     string      `json:"algorithm"`
	ProxyProtocol bool        `json:"proxy_protocol"`
	HealthCheck   HealthCheck `json:"health_check"`
	Targets       []LBTarget  `json:"targets"`
}

type Listener struct {
	ID            string `json:"id"`
	Protocol      string `json:"protocol"`
	Port          int64  `json:"port"`
	TargetGroupID string `json:"target_group_id"`
}

type LoadBalancerIn struct {
	Name             string            `json:"name"`
	Scheme           string            `json:"scheme"`
	SubnetID         *string           `json:"subnet_id,omitempty"`
	PublicIPID       *string           `json:"public_ip_id,omitempty"`
	SecurityGroupIDs []string          `json:"security_group_ids,omitempty"`
	Description      string            `json:"description"`
	Tags             map[string]string `json:"tags"`
}

type LoadBalancer struct {
	Meta
	Scheme           string        `json:"scheme"`
	VpcID            string        `json:"vpc_id"`
	SubnetID         string        `json:"subnet_id"`
	SecurityGroupIDs []string      `json:"security_group_ids"`
	Address          *string       `json:"address"`
	PublicIPID       *string       `json:"public_ip_id"`
	PublicAddress    *string       `json:"public_address"`
	Listeners        []Listener    `json:"listeners"`
	TargetGroups     []TargetGroup `json:"target_groups"`
	Nodes            struct {
		Desired int64 `json:"desired"`
		Ready   int64 `json:"ready"`
	} `json:"nodes"`
}

type HealthCheckIn struct {
	Protocol           string `json:"protocol"`
	Port               *int64 `json:"port,omitempty"`
	Path               string `json:"path"`
	Matcher            string `json:"matcher"`
	IntervalSeconds    int64  `json:"interval_seconds"`
	TimeoutSeconds     int64  `json:"timeout_seconds"`
	HealthyThreshold   int64  `json:"healthy_threshold"`
	UnhealthyThreshold int64  `json:"unhealthy_threshold"`
}

type TargetGroupIn struct {
	Name          string         `json:"name"`
	Protocol      string         `json:"protocol"`
	Port          int64          `json:"port"`
	Algorithm     string         `json:"algorithm"`
	ProxyProtocol bool           `json:"proxy_protocol"`
	HealthCheck   *HealthCheckIn `json:"health_check,omitempty"`
}

type TargetGroupPatch struct {
	Port          *int64         `json:"port,omitempty"`
	Algorithm     *string        `json:"algorithm,omitempty"`
	ProxyProtocol *bool          `json:"proxy_protocol,omitempty"`
	HealthCheck   *HealthCheckIn `json:"health_check,omitempty"`
}

type ListenerIn struct {
	Port          int64  `json:"port"`
	Protocol      string `json:"protocol"`
	TargetGroupID string `json:"target_group_id"`
}

type TargetIn struct {
	VMID string `json:"vm_id"`
	Port *int64 `json:"port,omitempty"`
}

// -- hosted DNS -----------------------------------------------------------------------------

type DNSZoneIn struct {
	Domain      string            `json:"domain"`
	Name        *string           `json:"name,omitempty"`
	Description string            `json:"description"`
	Tags        map[string]string `json:"tags"`
}

type DNSZone struct {
	Meta
	Domain       string   `json:"domain"`
	Status       string   `json:"status"`
	Nameservers  []string `json:"nameservers"`
	Served       bool     `json:"served"`
	Serial       *int64   `json:"serial"`
	DelegationOK *bool    `json:"delegation_ok"`
	Verification struct {
		State    string  `json:"state"`
		Method   *string `json:"method"`
		TXTName  string  `json:"txt_name"`
		TXTValue string  `json:"txt_value"`
		Message  string  `json:"message"`
	} `json:"verification"`
}

type DNSRecordIn struct {
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	TTL         int64    `json:"ttl"`
	Values      []string `json:"values"`
	Description string   `json:"description"`
}

type DNSRecordPatch struct {
	TTL         *int64    `json:"ttl,omitempty"`
	Values      *[]string `json:"values,omitempty"`
	Description *string   `json:"description,omitempty"`
}

type DNSRecord struct {
	ID          string   `json:"id"`
	ZoneID      string   `json:"zone_id"`
	Name        string   `json:"name"`
	FQDN        string   `json:"fqdn"`
	Type        string   `json:"type"`
	TTL         int64    `json:"ttl"`
	Values      []string `json:"values"`
	Description string   `json:"description"`
	CreatedAt   string   `json:"created_at"`
}

// -- snapshot schedules ---------------------------------------------------------------------

type SnapshotScheduleIn struct {
	Name           string            `json:"name"`
	VMID           string            `json:"vm_id"`
	Frequency      string            `json:"frequency"`
	Minute         int64             `json:"minute"`
	Hour           int64             `json:"hour"`
	Weekday        int64             `json:"weekday"`
	Cron           *string           `json:"cron,omitempty"`
	Timezone       string            `json:"timezone"`
	RetentionCount *int64            `json:"retention_count,omitempty"`
	RetentionDays  *int64            `json:"retention_days,omitempty"`
	Enabled        bool              `json:"enabled"`
	Description    string            `json:"description"`
	Tags           map[string]string `json:"tags"`
}

type SnapshotSchedulePatch struct {
	Frequency           *string            `json:"frequency,omitempty"`
	Minute              *int64             `json:"minute,omitempty"`
	Hour                *int64             `json:"hour,omitempty"`
	Weekday             *int64             `json:"weekday,omitempty"`
	Cron                *string            `json:"cron,omitempty"`
	Timezone            *string            `json:"timezone,omitempty"`
	RetentionCount      *int64             `json:"retention_count,omitempty"`
	RetentionDays       *int64             `json:"retention_days,omitempty"`
	ClearRetentionCount bool               `json:"clear_retention_count,omitempty"`
	ClearRetentionDays  bool               `json:"clear_retention_days,omitempty"`
	Enabled             *bool              `json:"enabled,omitempty"`
	Description         *string            `json:"description,omitempty"`
	Tags                *map[string]string `json:"tags,omitempty"`
}

type SnapshotSchedule struct {
	Meta
	VMID           string  `json:"vm_id"`
	Frequency      string  `json:"frequency"`
	Cron           string  `json:"cron"`
	Timezone       string  `json:"timezone"`
	RetentionCount *int64  `json:"retention_count"`
	RetentionDays  *int64  `json:"retention_days"`
	Enabled        bool    `json:"enabled"`
	NextRunAt      *string `json:"next_run_at"`
	Failures       int64   `json:"failures"`
	SnapshotCount  int64   `json:"snapshot_count"`
}
