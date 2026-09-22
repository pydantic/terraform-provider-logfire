# Weekday office hours in London. Use the schedule in a channel assignment on
# `logfire_alert` or `logfire_slo` to notify a channel only inside its windows.
resource "logfire_schedule" "office_hours" {
  label    = "Office hours"
  timezone = "Europe/London"
  windows = [
    { days = [1, 2, 3, 4, 5], start_time = "09:00", end_time = "18:00" },
  ]
}

# Several windows: weekday evenings and all of the weekend.
resource "logfire_schedule" "out_of_hours" {
  label    = "Out of hours"
  timezone = "America/New_York"
  windows = [
    { days = [1, 2, 3, 4, 5], start_time = "18:00", end_time = "23:59" },
    { days = [6, 7], start_time = "00:00", end_time = "23:59" },
  ]
}
