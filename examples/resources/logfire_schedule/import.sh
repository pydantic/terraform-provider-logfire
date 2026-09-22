# Import an existing schedule by its UUID. The API has no schedule list and no
# lookup by label. The UUID is the `schedule_id` of any channel assignment that
# uses the schedule, for example in `logfire_slo.alerts` or in the alert
# API response.
terraform import 'logfire_schedule.office_hours' "9f9b2f9e-aaaa-bbbb-cccc-ddddeeeeffff"
