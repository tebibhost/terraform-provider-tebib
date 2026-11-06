package provider

import (
	"context"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/tebibhost/terraform-provider-tebib/internal/client"
)

// -- tebib_ssh_key --------------------------------------------------------------------------

type sshKeyModel struct {
	projectMeta
	PublicKey   types.String   `tfsdk:"public_key"`
	Fingerprint types.String   `tfsdk:"fingerprint"`
	KeyType     types.String   `tfsdk:"key_type"`
	Timeouts    timeouts.Value `tfsdk:"timeouts"`
}

func (m *sshKeyModel) meta() *projectMeta            { return &m.projectMeta }
func (m *sshKeyModel) timeoutsValue() timeouts.Value { return m.Timeouts }

// sameKey is true when two public keys differ only in spacing.
func sameKey(a, b string) bool {
	return strings.Join(strings.Fields(a), " ") == strings.Join(strings.Fields(b), " ")
}

func newSSHKeyResource() resource.Resource {
	return &simpleResource[client.SSHKey, sshKeyModel, *sshKeyModel]{spec: simpleSpec[client.SSHKey, sshKeyModel]{
		typeName: "ssh_key", noun: "SSH key", plural: "SSH keys", collection: "ssh-keys",
		summary: "An SSH public key to install on new VMs. VMs copy the key when they are " +
			"created: deleting or replacing the key later doesn't change them.",
		idDesc:   "SSH key ID (`key-...`).",
		timeouts: [3]time.Duration{2 * time.Minute, 2 * time.Minute, 2 * time.Minute},
		attrs: map[string]schema.Attribute{
			"public_key": replaceString("Public key in OpenSSH format (ed25519, ECDSA, or RSA of at "+
				"least 3072 bits), e.g. `file(\"~/.ssh/id_ed25519.pub\")`. Changing it replaces the key.", true),
			"fingerprint": computedString("SHA256 fingerprint."),
			"key_type":    computedString("Key type, e.g. `ssh-ed25519`."),
		},
		body: func(ctx context.Context, m *sshKeyModel, diags *diag.Diagnostics) any {
			return client.SSHKeyIn{
				Name: m.Name.ValueString(), PublicKey: strings.TrimSpace(m.PublicKey.ValueString()),
				Description: m.Description.ValueString(), Tags: tagsFrom(ctx, m.Tags, diags),
			}
		},
		read: func(m *sshKeyModel, a *client.SSHKey) {
			// The API keeps a canonical spelling; keep the user's when it's the same key.
			if m.PublicKey.IsNull() || m.PublicKey.IsUnknown() || !sameKey(m.PublicKey.ValueString(), a.PublicKey) {
				m.PublicKey = types.StringValue(a.PublicKey)
			}
			m.Fingerprint = types.StringValue(a.Fingerprint)
			m.KeyType = types.StringValue(a.KeyType)
		},
	}}
}

// -- tebib_snapshot -------------------------------------------------------------------------

type snapshotModel struct {
	projectMeta
	VMID        types.String   `tfsdk:"vm_id"`
	VolumeIDs   types.List     `tfsdk:"volume_ids"`
	SizeGiB     types.Int64    `tfsdk:"size_gib"`
	Consistency types.String   `tfsdk:"consistency"`
	TakenAt     types.String   `tfsdk:"taken_at"`
	Timeouts    timeouts.Value `tfsdk:"timeouts"`
}

func (m *snapshotModel) meta() *projectMeta            { return &m.projectMeta }
func (m *snapshotModel) timeoutsValue() timeouts.Value { return m.Timeouts }

func newSnapshotResource() resource.Resource {
	return &simpleResource[client.Snapshot, snapshotModel, *snapshotModel]{spec: simpleSpec[client.Snapshot, snapshotModel]{
		typeName: "snapshot", noun: "snapshot", plural: "snapshots", collection: "snapshots",
		summary: "A point-in-time snapshot of all of a VM's volumes. It is application-consistent " +
			"when the guest agent runs, crash-consistent otherwise. It is kept until deleted, even " +
			"after the VM is gone.",
		idDesc:   "Snapshot ID (`snap-...`).",
		timeouts: [3]time.Duration{30 * time.Minute, 5 * time.Minute, 10 * time.Minute},
		attrs: map[string]schema.Attribute{
			"vm_id": replaceString("VM to snapshot. Changing it replaces the snapshot.", true),
			"volume_ids": schema.ListAttribute{
				ElementType: types.StringType, Computed: true,
				MarkdownDescription: "The volumes it holds.",
			},
			"size_gib":    stableInt("Total size in GiB."),
			"consistency": computedString("`application` or `crash`."),
			"taken_at":    computedString("When it was taken (RFC 3339)."),
		},
		body: func(ctx context.Context, m *snapshotModel, diags *diag.Diagnostics) any {
			return client.SnapshotIn{
				Name: m.Name.ValueString(), VMID: m.VMID.ValueString(),
				Description: m.Description.ValueString(), Tags: tagsFrom(ctx, m.Tags, diags),
			}
		},
		read: func(m *snapshotModel, a *client.Snapshot) {
			m.VMID = types.StringValue(a.VMID)
			m.VolumeIDs = stringListValue(a.VolumeIDs)
			m.SizeGiB = types.Int64Value(a.SizeGiB)
			m.Consistency = optString(a.Consistency)
			m.TakenAt = optString(a.TakenAt)
		},
	}}
}
