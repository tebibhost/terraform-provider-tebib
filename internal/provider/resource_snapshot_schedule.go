package provider

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/resourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/tebibhost/terraform-provider-tebib/internal/client"
)

// -- tebib_snapshot_schedule ----------------------------------------------------------------

type snapshotScheduleResource struct{ base }

type snapshotScheduleModel struct {
	projectMeta
	VMID           types.String   `tfsdk:"vm_id"`
	Frequency      types.String   `tfsdk:"frequency"`
	Minute         types.Int64    `tfsdk:"minute"`
	Hour           types.Int64    `tfsdk:"hour"`
	Weekday        types.Int64    `tfsdk:"weekday"`
	Cron           types.String   `tfsdk:"cron"`
	Timezone       types.String   `tfsdk:"timezone"`
	RetentionCount types.Int64    `tfsdk:"retention_count"`
	RetentionDays  types.Int64    `tfsdk:"retention_days"`
	Enabled        types.Bool     `tfsdk:"enabled"`
	EffectiveCron  types.String   `tfsdk:"effective_cron"`
	NextRunAt      types.String   `tfsdk:"next_run_at"`
	SnapshotCount  types.Int64    `tfsdk:"snapshot_count"`
	Failures       types.Int64    `tfsdk:"failures"`
	Timeouts       timeouts.Value `tfsdk:"timeouts"`
}

func newSnapshotScheduleResource() resource.Resource { return &snapshotScheduleResource{} }

func (r *snapshotScheduleResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_snapshot_schedule"
}

func (r *snapshotScheduleResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	timeInt := func(desc string, max int64) schema.Int64Attribute {
		return schema.Int64Attribute{
			Optional: true, Computed: true, Default: int64default.StaticInt64(0),
			MarkdownDescription: desc, Validators: []validator.Int64{int64validator.Between(0, max)},
		}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Takes snapshots of a VM on a schedule and deletes the old ones. " +
			"Snapshots it took are kept when the schedule is deleted (delete them yourself). " +
			"At least one of `retention_count` and `retention_days` is required.",
		Attributes: mergeAttrs(metaAttrs("snapshot schedules", "Snapshot schedule ID (`ssch-...`)."), map[string]schema.Attribute{
			"vm_id": replaceString("VM to snapshot. Changing it replaces the schedule.", true),
			"frequency": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "`hourly` (at `minute`), `daily` (at `hour`:`minute`), `weekly` " +
					"(on `weekday` at `hour`:`minute`) or `custom` (`cron`).",
				Validators: []validator.String{stringvalidator.OneOf("hourly", "daily", "weekly", "custom")},
			},
			"minute":  timeInt("Minute of the hour (0-59). Default: 0.", 59),
			"hour":    timeInt("Hour of the day (0-23), for daily and weekly. Default: 0.", 23),
			"weekday": timeInt("Day of the week for weekly, 0 = Sunday to 6 = Saturday. Default: 0.", 6),
			"cron": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "For frequency `custom` only: minute hour day-of-month month " +
					"day-of-week, e.g. `30 2 * * 1-5`. At most once an hour (the minute is one number).",
			},
			"timezone": schema.StringAttribute{
				Optional: true, Computed: true, Default: stringdefault.StaticString("UTC"),
				MarkdownDescription: "IANA time zone of the times, e.g. `Africa/Nairobi`. Default: `UTC`.",
			},
			"retention_count": schema.Int64Attribute{
				Optional: true, MarkdownDescription: "Keep the newest N snapshots this schedule took (1-100).",
				Validators: []validator.Int64{int64validator.Between(1, 100)},
			},
			"retention_days": schema.Int64Attribute{
				Optional: true, MarkdownDescription: "Delete snapshots this schedule took once they are N days old (1-365).",
				Validators: []validator.Int64{int64validator.Between(1, 365)},
			},
			"enabled": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(true),
				MarkdownDescription: "Whether it takes snapshots. Default: `true`.",
			},
			"effective_cron": volatileString("The cron expression the schedule runs on, presets included."),
			"next_run_at":    volatileString("The next run (UTC, RFC 3339); null while disabled."),
			"snapshot_count": volatileInt("Live snapshots this schedule took."),
			"failures":       volatileInt("Runs in a row that didn't succeed."),
		}),
		Blocks: map[string]schema.Block{
			"timeouts": timeouts.Block(ctx, timeouts.Opts{Create: true, Update: true, Delete: true}),
		},
	}
}

func (r *snapshotScheduleResource) ConfigValidators(context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{
		resourcevalidator.AtLeastOneOf(path.MatchRoot("retention_count"), path.MatchRoot("retention_days")),
	}
}

func (r *snapshotScheduleResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.configure(req.ProviderData, &resp.Diagnostics)
}

func (r *snapshotScheduleResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	importProjectScoped(ctx, req, resp)
}

func schedulePath(project, id string) string {
	return client.ProjectPath(project, "snapshot-schedules", id)
}

// presetParts reads minute, hour and weekday back from a preset's cron expression
// ("M * * * *", "M H * * *", "M H * * W"), so drift and imports show the real times.
func presetParts(expr string) (minute, hour, weekday int64, ok bool) {
	f := strings.Fields(expr)
	if len(f) != 5 {
		return 0, 0, 0, false
	}
	num := func(s string) (int64, bool) {
		n, err := strconv.ParseInt(s, 10, 64)
		return n, err == nil
	}
	if minute, ok = num(f[0]); !ok {
		return 0, 0, 0, false
	}
	if f[1] != "*" {
		if hour, ok = num(f[1]); !ok {
			return 0, 0, 0, false
		}
	}
	if f[4] != "*" {
		if weekday, ok = num(f[4]); !ok {
			return 0, 0, 0, false
		}
		weekday %= 7
	}
	return minute, hour, weekday, true
}

func (m *snapshotScheduleModel) fromAPI(s *client.SnapshotSchedule) {
	m.projectMeta.fromAPI(s.Meta)
	m.VMID = types.StringValue(s.VMID)
	m.Frequency = types.StringValue(s.Frequency)
	if s.Frequency == "custom" {
		// Keep the user's spelling; the API may normalize it.
		if m.Cron.IsNull() || m.Cron.IsUnknown() {
			m.Cron = types.StringValue(s.Cron)
		}
		for _, v := range []*types.Int64{&m.Minute, &m.Hour, &m.Weekday} {
			if v.IsNull() || v.IsUnknown() {
				*v = types.Int64Value(0)
			}
		}
	} else {
		m.Cron = types.StringNull()
		if minute, hour, weekday, ok := presetParts(s.Cron); ok {
			m.Minute = types.Int64Value(minute)
			if s.Frequency != "hourly" {
				m.Hour = types.Int64Value(hour)
			} else if m.Hour.IsNull() || m.Hour.IsUnknown() {
				m.Hour = types.Int64Value(0)
			}
			if s.Frequency == "weekly" {
				m.Weekday = types.Int64Value(weekday)
			} else if m.Weekday.IsNull() || m.Weekday.IsUnknown() {
				m.Weekday = types.Int64Value(0)
			}
		}
	}
	m.Timezone = types.StringValue(s.Timezone)
	m.RetentionCount = optInt(s.RetentionCount)
	m.RetentionDays = optInt(s.RetentionDays)
	m.Enabled = types.BoolValue(s.Enabled)
	m.EffectiveCron = types.StringValue(s.Cron)
	m.NextRunAt = optString(s.NextRunAt)
	m.SnapshotCount = types.Int64Value(s.SnapshotCount)
	m.Failures = types.Int64Value(s.Failures)
}

func (r *snapshotScheduleResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan snapshotScheduleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	project := r.projectID(plan.ProjectID, &resp.Diagnostics)
	d, ds := plan.Timeouts.Create(ctx, 5*time.Minute)
	ctx, cancel := withTimeout(ctx, d, ds, &resp.Diagnostics)
	defer cancel()
	body := client.SnapshotScheduleIn{
		Name: plan.Name.ValueString(), VMID: plan.VMID.ValueString(),
		Frequency: plan.Frequency.ValueString(),
		Minute:    plan.Minute.ValueInt64(), Hour: plan.Hour.ValueInt64(), Weekday: plan.Weekday.ValueInt64(),
		Cron: strPtr(plan.Cron), Timezone: plan.Timezone.ValueString(),
		RetentionCount: int64Ptr(plan.RetentionCount), RetentionDays: int64Ptr(plan.RetentionDays),
		Enabled:     plan.Enabled.ValueBool(),
		Description: plan.Description.ValueString(), Tags: tagsFrom(ctx, plan.Tags, &resp.Diagnostics),
	}
	if resp.Diagnostics.HasError() {
		return
	}
	var s client.SnapshotSchedule
	if err := r.client().Create(ctx, client.ProjectPath(project, "snapshot-schedules"), body, &s); err != nil {
		apiError(&resp.Diagnostics, "Creating the snapshot schedule", err)
		return
	}
	plan.fromAPI(&s)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	settled, err := client.Wait[client.SnapshotSchedule](ctx, r.client(), schedulePath(project, s.ID), nil)
	if settled != nil {
		plan.fromAPI(settled)
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	}
	if err != nil {
		waitError(&resp.Diagnostics, "the snapshot schedule", err)
	}
}

func (r *snapshotScheduleResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state snapshotScheduleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	project := r.projectID(state.ProjectID, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	var s client.SnapshotSchedule
	err := r.client().Get(ctx, schedulePath(project, state.ID.ValueString()), &s)
	if client.IsNotFound(err) || (err == nil && s.State == "deleted") {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "Reading the snapshot schedule", err)
		return
	}
	state.fromAPI(&s)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *snapshotScheduleResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state snapshotScheduleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	d, ds := plan.Timeouts.Update(ctx, 5*time.Minute)
	ctx, cancel := withTimeout(ctx, d, ds, &resp.Diagnostics)
	defer cancel()
	p := schedulePath(state.ProjectID.ValueString(), state.ID.ValueString())
	var patch client.SnapshotSchedulePatch
	changed := false
	if tp := tagsPatch(ctx, plan.projectMeta, state.projectMeta, &resp.Diagnostics); tp != nil {
		patch.Description, patch.Tags = tp.Description, tp.Tags
		changed = true
	}
	timing := !plan.Frequency.Equal(state.Frequency) || !plan.Minute.Equal(state.Minute) ||
		!plan.Hour.Equal(state.Hour) || !plan.Weekday.Equal(state.Weekday) ||
		!plan.Cron.Equal(state.Cron) || !plan.Timezone.Equal(state.Timezone)
	if timing {
		// The whole timing goes together: the API merges what is missing from the old one.
		patch.Frequency = ptr(plan.Frequency.ValueString())
		patch.Minute, patch.Hour, patch.Weekday = ptr(plan.Minute.ValueInt64()), ptr(plan.Hour.ValueInt64()), ptr(plan.Weekday.ValueInt64())
		patch.Timezone = ptr(plan.Timezone.ValueString())
		if plan.Frequency.ValueString() == "custom" {
			patch.Cron = strPtr(plan.Cron)
		}
		changed = true
	}
	if !plan.RetentionCount.Equal(state.RetentionCount) {
		if plan.RetentionCount.IsNull() {
			patch.ClearRetentionCount = true
		} else {
			patch.RetentionCount = int64Ptr(plan.RetentionCount)
		}
		changed = true
	}
	if !plan.RetentionDays.Equal(state.RetentionDays) {
		if plan.RetentionDays.IsNull() {
			patch.ClearRetentionDays = true
		} else {
			patch.RetentionDays = int64Ptr(plan.RetentionDays)
		}
		changed = true
	}
	if !plan.Enabled.Equal(state.Enabled) {
		patch.Enabled = ptr(plan.Enabled.ValueBool())
		changed = true
	}
	if changed {
		if err := r.client().Patch(ctx, p, patch, nil); err != nil {
			apiError(&resp.Diagnostics, "Updating the snapshot schedule", err)
			return
		}
	}
	settled, err := client.Wait[client.SnapshotSchedule](ctx, r.client(), p, nil)
	if err != nil {
		waitError(&resp.Diagnostics, "the snapshot schedule", err)
		return
	}
	plan.fromAPI(settled)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *snapshotScheduleResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state snapshotScheduleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	d, ds := state.Timeouts.Delete(ctx, 5*time.Minute)
	ctx, cancel := withTimeout(ctx, d, ds, &resp.Diagnostics)
	defer cancel()
	if err := deleteAndWait[client.SnapshotSchedule](ctx, r.client(), schedulePath(state.ProjectID.ValueString(), state.ID.ValueString())); err != nil {
		apiError(&resp.Diagnostics, "Deleting the snapshot schedule", err)
	}
}
