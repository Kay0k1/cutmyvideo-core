package app

import "github.com/Kay0k1/cutmyvideo-core/internal/toolrun"

func mediaProcessEnvironmentFor(platform string, maxProcs int) []string {
	return toolrun.EnvironmentFor(platform, maxProcs)
}
