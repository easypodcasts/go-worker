package config

var ExampleConfig = `# This is an example config.yaml file with some sensible defaults.
# Limits how many jobs can run concurrently
limit: 2

# Podcasts instance URL.
url: "https://roig.is-a.dev/podcasts"

# The worker authentication token
token: changeMe

# Defines the interval length, in seconds, between new jobs check. 
# The default value is 3. If set to 0 or lower, the default value is used.
checkInterval: 10
`
