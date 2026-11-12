package server

// Tests never ask ipinfo.io where they are.
func init() { placeLookupURL = "" }
