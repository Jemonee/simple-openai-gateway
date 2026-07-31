package gateway

import "time"

const (
	eastEightOffset = 8 * time.Hour
	// Strip fractional seconds before shifting because this SQLite driver rounds
	// 23:59:59.999999 into the next day inside date()/unixepoch().
	sqliteEastEightCreatedDate = "date(substr(created_at, 1, 19), '+8 hours')"
	sqliteEastEightCreatedHour = "strftime('%Y-%m-%dT%H:00:00+08:00', substr(created_at, 1, 19), '+8 hours')"
)

var eastEightLocation = time.FixedZone("UTC+8", int(eastEightOffset/time.Second))

func eastEightStartOfDay(value time.Time) time.Time {
	local := value.In(eastEightLocation)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, eastEightLocation)
}

func eastEightDate(value time.Time) string {
	return value.In(eastEightLocation).Format(time.DateOnly)
}

func utcQueryTime(value time.Time) time.Time {
	return value.UTC()
}

func utcInclusiveMillisecondEnd(value time.Time) time.Time {
	return value.UTC().Truncate(time.Millisecond).Add(time.Millisecond - time.Nanosecond)
}
