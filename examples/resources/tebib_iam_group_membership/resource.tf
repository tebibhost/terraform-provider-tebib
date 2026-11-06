resource "tebib_iam_group_membership" "abel" {
  group_id = tebib_iam_group.ops.id
  user_id  = "u-abc123"
}
