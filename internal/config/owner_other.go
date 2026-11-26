//go:build !unix

package config

import "os"

func keepOwner(*os.File, os.FileInfo) {}
