package utils

import (
	"strings"
	"time"

	"github.com/fatih/color"
)

// BlockPassword
// block password in mongo_urls:
// two kind mongo_urls:
//  1. mongodb://username:password@address
//  2. username:password@address
func BlockPassword(url, replace string) string {
	colon := strings.Index(url, ":")
	if colon == -1 || colon == len(url)-1 {
		return url
	} else if url[colon+1] == '/' {
		// find the second '/'
		for colon++; colon < len(url); colon++ {
			if url[colon] == ':' {
				break
			}
		}

		if colon == len(url) {
			return url
		}
	}

	at := strings.Index(url, "@")
	if at == -1 || at == len(url)-1 || at <= colon {
		return url
	}

	newUrl := make([]byte, 0, len(url))
	for i := 0; i < len(url); i++ {
		if i <= colon || i > at {
			newUrl = append(newUrl, byte(url[i]))
		} else if i == at {
			newUrl = append(newUrl, []byte(replace)...)
			newUrl = append(newUrl, byte(url[i]))
		}
	}
	return string(newUrl)
}

func PrintCost(start time.Time) {
	print("Cost: ")
	color.Green(time.Since(start).String())
}
