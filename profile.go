package main

import (
	"time"
	// Embed the IANA timezone database so profile-timezone resolution works
	// even when the runtime image ships without tzdata.
	_ "time/tzdata"
)

const (
	dayTypeWeekday = "weekday"
	dayTypeWeekend = "weekend"
	// profileRetentionDays bounds day-grain bucket history at ~30 days so a
	// schedule change washes out instead of lingering for months (ADR 0004).
	profileRetentionDays = 30
	bucketDateLayout     = "2006-01-02"
)

// bucketKey locates one day-grain usage-profile row: the (day type, hour) cell
// plus the calendar date it was observed on, all in the profile timezone.
type bucketKey struct {
	DayType string
	Hour    int
	Date    string
}

// bucketFor assigns an instant to its profile bucket (CONTEXT.md: bucket, day
// type). Assignment uses wall-clock fields in loc, so DST transitions land on
// the hour the user actually experienced, and a UTC instant near midnight can
// belong to a different day type than its UTC calendar day.
func bucketFor(at time.Time, loc *time.Location) bucketKey {
	local := at.In(loc)
	dayType := dayTypeWeekday
	if weekday := local.Weekday(); weekday == time.Saturday || weekday == time.Sunday {
		dayType = dayTypeWeekend
	}
	return bucketKey{DayType: dayType, Hour: local.Hour(), Date: local.Format(bucketDateLayout)}
}

// usageBucketRow is one stored day-grain profile row.
type usageBucketRow struct {
	Provider       string
	AuthIndex      string
	DayType        string
	Hour           int
	Date           string
	TokenSum       int64
	RecordsSeen    int64
	RecordsTokened int64
}

func (s *healthStore) usageBuckets() ([]usageBucketRow, error) {
	rows, err := s.db.Query(`SELECT provider,auth_index,day_type,hour,date,token_sum,records_seen,records_tokened FROM usage_buckets ORDER BY provider,auth_index,date,day_type,hour`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []usageBucketRow
	for rows.Next() {
		var row usageBucketRow
		if err := rows.Scan(&row.Provider, &row.AuthIndex, &row.DayType, &row.Hour, &row.Date, &row.TokenSum, &row.RecordsSeen, &row.RecordsTokened); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}
