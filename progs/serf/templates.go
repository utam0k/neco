package serf

import "html/template"

type tags struct {
	OsVersion string `json:"os-version"`
	Serial    string `json:"serial"`
}

type serfConfig struct {
	Tags              tags     `json:"tags"`
	Interface         string   `json:"interface"`
	EventHandlers     []string `json:"event_handlers"`
	ReconnectInterval string   `json:"reconnect_interval"`
	ReconnectTimeout  string   `json:"reconnect_timeout"`
	TombstoneTimeout  string   `json:"tombstone_timeout"`
	RetryJoin         []string `json:"retry_join"`
	RetryMaxAttempts  int      `json:"retry_max_attempts"`
	RetryInterval     string   `json:"retry_interval"`
	LogLevel          string   `json:"log_level"`
	Protocol          int      `json:"protocol"`
}

var serviceTmpl = template.Must(template.New("serf.service").
	Parse(`[Unit]
Description=Serf container on rkt
Wants=network-online.target
After=network-online.target
ConditionPathExists=/etc/serf/serf.json

[Service]
Slice=machine.slice
Type=simple
KillMode=mixed
Restart=always
RestartSec=10s
ExecStart=/usr/bin/rkt run \
  --pull-policy never --net=host \
  --volume conf,kind=host,source=/etc/serf \
  --mount volume=conf,target=/etc/serf \
  --hostname %H \
  {{ .Image }} \
    --name serf \
    --readonly-rootfs=true \
  -- \
    agent -config-file {{ .ConfFile }}

[Install]
WantedBy=multi-user.target
`))
