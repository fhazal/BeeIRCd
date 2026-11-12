zones.go is generated from the IANA tz database 2026a (public domain,
https://github.com/eggert/tz): zone.tab, zone1970.tab and backward.

  go run ./tools/tzplaces -dir <folder with those three files> -version 2026a > internal/tzplace/zones.go

sha256 (first 16): zone.tab 586b4207e6c76722, zone1970.tab e9d9fe30942a880f, backward d2f4c8953f204982
