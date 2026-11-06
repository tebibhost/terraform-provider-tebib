package client

// API models. Field names follow platform/control-plane/openapi.json. Only the fields the
// provider uses are here; unknown fields in answers are ignored.

// Meta holds the fields every project resource shares.
type Meta struct {
	Lifecycle
	ID          string            `json:"id"`
	TRN         string            `json:"trn"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Tags        map[string]string `json:"tags"`
	ProjectID   string            `json:"project_id"`
	CreatedAt   string            `json:"created_at"`
	UpdatedAt   string            `json:"updated_at"`
}

// GetMeta returns the shared fields; it lets generic code read any project resource.
func (m Meta) GetMeta() Meta { return m }

// ProjectResource is any API type that embeds Meta.
type ProjectResource interface {
	Stateful
	GetMeta() Meta
}

// TagsPatch changes description and tags (PATCH on most resources).
type TagsPatch struct {
	Description *string            `json:"description,omitempty"`
	Tags        *map[string]string `json:"tags,omitempty"`
}

// -- projects -------------------------------------------------------------------------------

type ProjectIn struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Tags        map[string]string `json:"tags"`
}

type Project struct {
	Lifecycle
	ID             string            `json:"id"`
	TRN            string            `json:"trn"`
	Name           string            `json:"name"`
	Description    string            `json:"description"`
	Tags           map[string]string `json:"tags"`
	OrganizationID string            `json:"organization_id"`
	CreatedAt      string            `json:"created_at"`
}

// -- network --------------------------------------------------------------------------------

type VpcIn struct {
	Name        string            `json:"name"`
	CIDR        string            `json:"cidr"`
	Description string            `json:"description"`
	Tags        map[string]string `json:"tags"`
}

type Vpc struct {
	Meta
	CIDR          string `json:"cidr"`
	IPv6CIDR      string `json:"ipv6_cidr"`
	DNSServer     string `json:"dns_server"`
	IsDefault     bool   `json:"is_default"`
	HasNatGateway bool   `json:"has_nat_gateway"`
}

type SubnetIn struct {
	Name        string            `json:"name"`
	VpcID       string            `json:"vpc_id"`
	CIDR        string            `json:"cidr"`
	Description string            `json:"description"`
	Tags        map[string]string `json:"tags"`
}

type Subnet struct {
	Meta
	VpcID         string `json:"vpc_id"`
	CIDR          string `json:"cidr"`
	Gateway       string `json:"gateway"`
	IPv6CIDR      string `json:"ipv6_cidr"`
	IPv6Gateway   string `json:"ipv6_gateway"`
	AvailableIPv4 *int64 `json:"available_ipv4"`
	IsDefault     bool   `json:"is_default"`
}

type SecurityGroupIn struct {
	Name        string            `json:"name"`
	VpcID       string            `json:"vpc_id"`
	Description string            `json:"description"`
	Tags        map[string]string `json:"tags"`
}

type SecurityGroup struct {
	Meta
	VpcID          string `json:"vpc_id"`
	IsDefault      bool   `json:"is_default"`
	AllowSameGroup bool   `json:"allow_same_group"`
	Rules          []Rule `json:"rules"`
}

type RuleIn struct {
	Direction     string  `json:"direction"`
	Protocol      string  `json:"protocol"`
	PortFrom      *int64  `json:"port_from"`
	PortTo        *int64  `json:"port_to"`
	RemoteCIDR    *string `json:"remote_cidr"`
	RemoteGroupID *string `json:"remote_group_id"`
	IPVersion     *string `json:"ip_version,omitempty"`
	Description   string  `json:"description"`
}

type Rule struct {
	ID            string  `json:"id"`
	Direction     string  `json:"direction"`
	Protocol      string  `json:"protocol"`
	PortFrom      *int64  `json:"port_from"`
	PortTo        *int64  `json:"port_to"`
	RemoteCIDR    *string `json:"remote_cidr"`
	RemoteGroupID *string `json:"remote_group_id"`
	IPVersion     string  `json:"ip_version"`
	Description   string  `json:"description"`
	CreatedAt     string  `json:"created_at"`
}

type NatGatewayIn struct {
	Name        string            `json:"name"`
	SubnetID    string            `json:"subnet_id"`
	PublicIPID  *string           `json:"public_ip_id"`
	Description string            `json:"description"`
	Tags        map[string]string `json:"tags"`
}

type NatGateway struct {
	Meta
	VpcID         string  `json:"vpc_id"`
	SubnetID      string  `json:"subnet_id"`
	LanIP         string  `json:"lan_ip"`
	PublicIPID    *string `json:"public_ip_id"`
	PublicAddress *string `json:"public_address"`
}

type PublicIPIn struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Tags        map[string]string `json:"tags"`
}

type PublicIP struct {
	Meta
	Address      string  `json:"address"`
	Association  string  `json:"association"`
	VMID         *string `json:"vm_id"`
	NatGatewayID *string `json:"nat_gateway_id"`
}

type AssociateIn struct {
	VMID         *string `json:"vm_id,omitempty"`
	NatGatewayID *string `json:"nat_gateway_id,omitempty"`
}

// -- compute --------------------------------------------------------------------------------

type SSHKeyIn struct {
	Name        string            `json:"name"`
	PublicKey   string            `json:"public_key"`
	Description string            `json:"description"`
	Tags        map[string]string `json:"tags"`
}

type SSHKey struct {
	Meta
	PublicKey   string `json:"public_key"`
	Fingerprint string `json:"fingerprint"`
	KeyType     string `json:"key_type"`
}

type VMIn struct {
	Name             string            `json:"name"`
	Image            string            `json:"image"`
	InstanceType     string            `json:"instance_type"`
	SSHKeyIDs        []string          `json:"ssh_key_ids"`
	SubnetID         *string           `json:"subnet_id,omitempty"`
	SecurityGroupIDs []string          `json:"security_group_ids,omitempty"`
	RootDiskGiB      *int64            `json:"root_disk_gib,omitempty"`
	UserData         string            `json:"user_data"`
	Hostname         *string           `json:"hostname,omitempty"`
	PowerState       string            `json:"power_state"`
	Description      string            `json:"description"`
	Tags             map[string]string `json:"tags"`
}

type VMAddress struct {
	Address string `json:"address"`
	Family  string `json:"family"`
	Primary bool   `json:"primary"`
}

type VolumeAttachment struct {
	VolumeID string `json:"volume_id"`
	State    string `json:"state"`
}

type VM struct {
	Meta
	Hostname          string             `json:"hostname"`
	Image             string             `json:"image"`
	InstanceType      string             `json:"instance_type"`
	VCPUs             int64              `json:"vcpus"`
	MemoryMiB         int64              `json:"memory_mib"`
	PowerState        string             `json:"power_state"`
	DesiredPowerState string             `json:"desired_power_state"`
	RestartRequired   bool               `json:"restart_required"`
	SubnetID          string             `json:"subnet_id"`
	SecurityGroupIDs  []string           `json:"security_group_ids"`
	SSHKeyIDs         []string           `json:"ssh_key_ids"`
	BootVolumeID      *string            `json:"boot_volume_id"`
	Addresses         []VMAddress        `json:"addresses"`
	Volumes           []VolumeAttachment `json:"volumes"`
	GuestAgent        bool               `json:"guest_agent"`
	StopReason        *string            `json:"stop_reason"`
}

type VolumeIn struct {
	Name                string            `json:"name"`
	SizeGiB             int64             `json:"size_gib"`
	DeleteOnTermination bool              `json:"delete_on_termination"`
	Description         string            `json:"description"`
	Tags                map[string]string `json:"tags"`
}

type Volume struct {
	Meta
	SizeGiB             int64   `json:"size_gib"`
	VMID                *string `json:"vm_id"`
	Boot                bool    `json:"boot"`
	SourceImage         *string `json:"source_image"`
	DeleteOnTermination bool    `json:"delete_on_termination"`
}

type VolumePatch struct {
	Description         *string            `json:"description,omitempty"`
	Tags                *map[string]string `json:"tags,omitempty"`
	DeleteOnTermination *bool              `json:"delete_on_termination,omitempty"`
}

type SnapshotIn struct {
	Name        string            `json:"name"`
	VMID        string            `json:"vm_id"`
	Description string            `json:"description"`
	Tags        map[string]string `json:"tags"`
}

type Snapshot struct {
	Meta
	VMID        string   `json:"vm_id"`
	VolumeIDs   []string `json:"volume_ids"`
	SizeGiB     int64    `json:"size_gib"`
	Consistency *string  `json:"consistency"`
	TakenAt     *string  `json:"taken_at"`
}

// -- catalogue ------------------------------------------------------------------------------

type Image struct {
	Name         string `json:"name"`
	DisplayName  string `json:"display_name"`
	OSFamily     string `json:"os_family"`
	DefaultUser  string `json:"default_user"`
	MinDiskGiB   int64  `json:"min_disk_gib"`
	MinMemoryMiB int64  `json:"min_memory_mib"`
	MinVCPUs     int64  `json:"min_vcpus"`
}

type InstanceType struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Family      string `json:"family"`
	VCPUs       int64  `json:"vcpus"`
	MemoryMiB   int64  `json:"memory_mib"`
}

type Price struct {
	Meter          string  `json:"meter"`
	Unit           string  `json:"unit"`
	UnitPriceMicro int64   `json:"unit_price_micro"`
	UnitPriceUSD   string  `json:"unit_price_usd"`
	MonthlyUSD     *string `json:"monthly_usd"`
	Note           string  `json:"note"`
	EffectiveFrom  string  `json:"effective_from"`
}

type EgressAllowance struct {
	InstanceType  string `json:"instance_type"`
	GBPerMonth    int64  `json:"gb_per_month"`
	EffectiveFrom string `json:"effective_from"`
}

type PriceList struct {
	At               string            `json:"at"`
	Currency         string            `json:"currency"`
	Region           string            `json:"region"`
	HoursPerMonth    int64             `json:"hours_per_month"`
	Prices           []Price           `json:"prices"`
	Scheduled        []Price           `json:"scheduled"`
	EgressAllowances []EgressAllowance `json:"egress_allowances"`
}

// -- IAM ------------------------------------------------------------------------------------

type ServiceAccountIn struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Tags        map[string]string `json:"tags"`
}

type ServiceAccount struct {
	ID          string            `json:"id"`
	TRN         string            `json:"trn"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Disabled    bool              `json:"disabled"`
	Tags        map[string]string `json:"tags"`
	CreatedAt   string            `json:"created_at"`
}

type ServiceAccountPatch struct {
	Description *string            `json:"description,omitempty"`
	Disabled    *bool              `json:"disabled,omitempty"`
	Tags        *map[string]string `json:"tags,omitempty"`
}

type AccessKeyIn struct {
	Description  string   `json:"description"`
	ExpiresAt    *string  `json:"expires_at,omitempty"`
	NeverExpires bool     `json:"never_expires"`
	AllowedCIDRs []string `json:"allowed_cidrs"`
}

type AccessKey struct {
	ID           string   `json:"id"`
	TRN          string   `json:"trn"`
	Owner        string   `json:"owner"`
	Description  string   `json:"description"`
	Status       string   `json:"status"`
	StatusReason string   `json:"status_reason"`
	ExpiresAt    *string  `json:"expires_at"`
	AllowedCIDRs []string `json:"allowed_cidrs"`
	CreatedAt    string   `json:"created_at"`
	LastUsedAt   *string  `json:"last_used_at"`
	DisablesAt   *string  `json:"disables_at"`
	// Only in the create answer.
	Secret string `json:"secret,omitempty"`
	Token  string `json:"token,omitempty"`
}

type AccessKeyPatch struct {
	Description *string `json:"description,omitempty"`
	Status      *string `json:"status,omitempty"`
}

type RoleIn struct {
	Name              string            `json:"name"`
	Description       string            `json:"description"`
	TrustPolicy       map[string]any    `json:"trust_policy"`
	MaxSessionSeconds int64             `json:"max_session_seconds"`
	Tags              map[string]string `json:"tags"`
}

type Role struct {
	ID                string            `json:"id"`
	TRN               string            `json:"trn"`
	Name              string            `json:"name"`
	Description       string            `json:"description"`
	TrustPolicy       map[string]any    `json:"trust_policy"`
	MaxSessionSeconds int64             `json:"max_session_seconds"`
	Tags              map[string]string `json:"tags"`
	CreatedAt         string            `json:"created_at"`
}

type RolePatch struct {
	Description       *string            `json:"description,omitempty"`
	TrustPolicy       map[string]any     `json:"trust_policy,omitempty"`
	MaxSessionSeconds *int64             `json:"max_session_seconds,omitempty"`
	Tags              *map[string]string `json:"tags,omitempty"`
}

type PolicyIn struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Document    map[string]any `json:"document"`
}

type Policy struct {
	ID              string         `json:"id"`
	TRN             string         `json:"trn"`
	Name            string         `json:"name"`
	Description     string         `json:"description"`
	Document        map[string]any `json:"document"`
	DefaultVersion  int64          `json:"default_version"`
	Managed         bool           `json:"managed"`
	AttachmentCount int64          `json:"attachment_count"`
	CreatedAt       string         `json:"created_at"`
}

type PolicyVersion struct {
	Version   int64          `json:"version"`
	IsDefault bool           `json:"is_default"`
	Document  map[string]any `json:"document"`
	CreatedAt string         `json:"created_at"`
}

type VersionIn struct {
	Document     map[string]any `json:"document"`
	SetAsDefault bool           `json:"set_as_default"`
}

type PolicyAttachmentIn struct {
	Policy    string  `json:"policy"`
	Principal string  `json:"principal"`
	ProjectID *string `json:"project_id,omitempty"`
}

type PolicyAttachment struct {
	ID            string  `json:"id"`
	PolicyID      string  `json:"policy_id"`
	PolicyTRN     string  `json:"policy_trn"`
	Principal     string  `json:"principal"`
	PrincipalType string  `json:"principal_type"`
	ProjectID     *string `json:"project_id"`
	CreatedAt     string  `json:"created_at"`
}

type GroupIn struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type Group struct {
	ID          string `json:"id"`
	TRN         string `json:"trn"`
	Name        string `json:"name"`
	Description string `json:"description"`
	MemberCount int64  `json:"member_count"`
	CreatedAt   string `json:"created_at"`
}

type GroupMember struct {
	UserID  string `json:"user_id"`
	Email   string `json:"email"`
	TRN     string `json:"trn"`
	AddedAt string `json:"added_at"`
}

type OrgUser struct {
	ID       string `json:"id"`
	TRN      string `json:"trn"`
	Email    string `json:"email"`
	FullName string `json:"full_name"`
}
