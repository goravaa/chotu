package internals

type Status struct {
	Timestamp  string     `json:"timestamp"`
	Setup      Setup      `json:"setup"`
	Monitoring Monitoring `json:"monitoring"`
}

type ServiceStatus struct {
	State   string `json:"state"`
	Message string `json:"message"`
}

type Setup struct {
	GPU      ServiceStatus `json:"gpu"`
	Docker   ServiceStatus `json:"docker"`
	VLLM     ServiceStatus `json:"vllm"`
	OpenCode ServiceStatus `json:"opencode"`
	Rclone   ServiceStatus `json:"rclone"`
	S3       ServiceStatus `json:"s3"`
}

type Monitoring struct {
	CPU              CPU               `json:"cpu"`
	RAM              RAM               `json:"ram"`
	GPU              GPU               `json:"gpu"`
	Disks            []Disk            `json:"disks"`
	Sync             Sync              `json:"sync"`
	OpenCodeSessions []OpenCodeSession `json:"opencode_sessions"`
}

type CPU struct {
	UsagePercent float64 `json:"usage_percent"`
}

type RAM struct {
	UsedBytes    uint64  `json:"used_bytes"`
	TotalBytes   uint64  `json:"total_bytes"`
	UsagePercent float64 `json:"usage_percent"`
}

type GPU struct {
	UsagePercent       float64 `json:"usage_percent"`
	MemoryUsedBytes    uint64  `json:"memory_used_bytes"`
	MemoryTotalBytes   uint64  `json:"memory_total_bytes"`
	TemperatureCelsius float64 `json:"temperature_celsius"`
}

type Disk struct {
	Mount        string  `json:"mount"`
	UsedBytes    uint64  `json:"used_bytes"`
	TotalBytes   uint64  `json:"total_bytes"`
	UsagePercent float64 `json:"usage_percent"`
}

type Sync struct {
	State       string `json:"state"`
	LastSuccess string `json:"last_success"`
	LastAttempt string `json:"last_attempt"`
	Message     string `json:"message"`
}

type OpenCodeSession struct {
	Name     string `json:"name"`
	Folder   string `json:"folder"`
	LastUsed string `json:"last_used"`
}
