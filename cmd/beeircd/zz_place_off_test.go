package main

import "os"

// Servers started by these tests never ask ipinfo.io where they are.
func init() { os.Setenv("BEEIRCD_NO_PLACE_LOOKUP", "1") }
