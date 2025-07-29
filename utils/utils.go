package utils

import (
	"errors"
	"fmt"
	"net/url"
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

func IsMultiHosts(uri string) (bool, error) {
	if !strings.HasPrefix(uri, "mongodb://") && !strings.HasPrefix(uri, "mongodb+srv://") {
		return false, errors.New("uri format error")
	}

	u, err := url.Parse(uri)
	if err != nil {
		return false, fmt.Errorf("parse uri err: %v", err)
	}

	if strings.HasPrefix(uri, "mongodb+srv://") {
		return true, nil
	}

	hosts := strings.Split(u.Host, ",")
	return len(hosts) > 1, nil
}
