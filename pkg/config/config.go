package config

import (
	"net/url"
)

type Config struct {
	Limit         int
	BuildsDir     string
	Token         string
	Endpoint      url.URL
	CheckInterval int
}
