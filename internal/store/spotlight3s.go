package store

import "time"

// Hive Panel Spotlight (13 Nov 2026): the newest account and the accounts
// registered on this day in an earlier year, read from the cache in one
// pass without copying accounts.

// AccountMoment is an account name with a time or a count of years.
type AccountMoment struct {
	Name  string
	When  time.Time
	Years int
}

// SpotlightAccounts returns the newest account registered after since
// (nil when none) and up to max accounts registered on now's day and
// month in an earlier year, oldest first.
func (s *Store) SpotlightAccounts(now, since time.Time, max int) (*AccountMoment, []AccountMoment) {
	var newest *AccountMoment
	var years []AccountMoment
	_, m, d := now.Date()
	for _, a := range s.c.accounts {
		r := a.Registered.In(now.Location())
		if r.After(since) && (newest == nil || r.After(newest.When)) {
			newest = &AccountMoment{Name: a.Name, When: a.Registered}
		}
		if _, rm, rd := r.Date(); rm == m && rd == d {
			if y := now.Year() - r.Year(); y >= 1 {
				years = append(years, AccountMoment{Name: a.Name, When: a.Registered, Years: y})
			}
		}
	}
	// Oldest first, then by name, so the list doesn't jump between calls.
	for i := 1; i < len(years); i++ {
		for j := i; j > 0 && (years[j].When.Before(years[j-1].When) || years[j].When.Equal(years[j-1].When) && years[j].Name < years[j-1].Name); j-- {
			years[j], years[j-1] = years[j-1], years[j]
		}
	}
	if len(years) > max {
		years = years[:max]
	}
	return newest, years
}
