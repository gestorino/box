package health

import (
	"cmp"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Unit says how the console formats a value.
type Unit string

const (
	Percent        Unit = "percent"
	Bytes          Unit = "bytes"
	BytesPerSecond Unit = "bytes_per_second"
	Celsius        Unit = "celsius"
	Watts          Unit = "watts"
	GHz            Unit = "ghz"
	Number         Unit = "number"
)

// Tabs are the Health page's tabs, in order: the box as a whole, its hardware, then one per service.
var Tabs = []string{"overview", "hardware", "engine", "browser", "hostd", "secrets", "sms", "calls", "whatsapp", "telegram", "email"}

// ServiceUser is the user each service runs as: process-exporter groups processes by it.
var ServiceUser = map[string]string{
	"engine": "concierge", "browser": "concierge-worker", "hostd": "concierge-host",
	"secrets": "concierge-secrets", "sms": "concierge-sms", "calls": "concierge-calls",
	"whatsapp": "concierge-wa", "telegram": "concierge-tg", "email": "concierge-mail",
}

// serviceLabel is each service tab's name in the console; it also names the service's series.
var serviceLabel = map[string]string{
	"engine": "Engine", "browser": "Browser", "hostd": "hostd", "secrets": "Secrets", "sms": "SMS",
	"calls": "Calls", "whatsapp": "WhatsApp", "telegram": "Telegram", "email": "Email",
}

// A tile is one current value; value and total are PromQL, total optional.
type tile struct {
	id, label    string
	unit         Unit
	value, total string
}

// A panel is one graph: its series, and lines (horizontal markers read at the range's end).
type panel struct {
	id, title string
	unit      Unit
	stacked   bool
	series    []source
	lines     []source
}

// A source is one PromQL expression and the name of the series it gives, or, when it gives one
// series per label value, nameBy to name each. `$w` in expr is the rate window, `$step` the gauge window.
type source struct {
	name   string
	expr   string
	nameBy func(labels map[string]string) string
}

type tabDef struct {
	procs  string // PromQL: the service's process count; empty on overview and hardware
	tiles  []tile
	panels []panel
}

// PromQL. Every `rate` reads over `$w`, which the handler sets to the graph's step (at least 30 s),
// so a point averages its whole step instead of sampling 30 s of it. A graph's gauge reads over
// `$step`, the step but at least 20 s: heat and load as the step's max, memory and clock as its
// average, so a 30-day graph still shows a spike between its points. A tile reads a gauge's current
// value.
const (
	cpus     = `scalar(count(node_cpu_seconds_total{mode="idle"}))`
	boxCPU   = `100 * sum(rate(node_cpu_seconds_total{mode!~"idle|iowait"}[$w])) / ` + cpus
	memUsed  = `node_memory_MemTotal_bytes - node_memory_MemAvailable_bytes`
	memTotal = `node_memory_MemTotal_bytes`
	diskUsed = `node_filesystem_size_bytes{mountpoint="/"} - node_filesystem_avail_bytes{mountpoint="/"}`
	diskSize = `node_filesystem_size_bytes{mountpoint="/"}`
	diskRead = `sum(rate(node_disk_read_bytes_total[$w]))`
	diskWrit = `sum(rate(node_disk_written_bytes_total[$w]))`
	clockNow = `avg(node_cpu_scaling_frequency_hertz) / 1e9`
	clockAvg = `avg(avg_over_time(node_cpu_scaling_frequency_hertz[$step])) / 1e9`
	clockMax = `max(max_over_time(node_cpu_scaling_frequency_hertz[$step])) / 1e9`

	// hwmon: temp1 is coretemp's package and the NVMe's composite; cores are named by their label.
	cpuTempNow = `max(node_hwmon_temp_celsius{sensor="temp1"} * on(chip) group_left node_hwmon_chip_names{chip_name="coretemp"})`
	cpuTemp    = `max(max_over_time(node_hwmon_temp_celsius{sensor="temp1"}[$step]) * on(chip) group_left node_hwmon_chip_names{chip_name="coretemp"})`
	coreTemps  = `max_over_time(node_hwmon_temp_celsius[$step]) * on(chip, sensor) group_left(label) node_hwmon_sensor_label{label=~"(?i)core.*"}`
	diskTemp   = `max(max_over_time(node_hwmon_temp_celsius{sensor="temp1"}[$step]) * on(chip) group_left node_hwmon_chip_names{chip_name="nvme"})`
	wifiTemp   = `max(max_over_time(node_hwmon_temp_celsius{sensor="temp1"}[$step]) * on(chip) group_left node_hwmon_chip_names{chip_name=~"iwlwifi.*"})`

	// sensord and calld.
	power      = `rate(concierge_package_energy_joules_total[$w])`
	limitLong  = `concierge_power_limit_watts{limit="long_term"}`
	limitShort = `concierge_power_limit_watts{limit="short_term"}`
	boardMax   = `max(concierge_board_temp_celsius)`
	fanAuto    = `concierge_fan_output_automatic`
	fanPWM     = `concierge_fan_output_manual_pwm_ratio`
	cstate     = `concierge_package_cstate_limit`
	cstateLock = `concierge_package_cstate_locked`
	storage    = `concierge_storage_bytes`

	// process-exporter, grouped by user; %s is the groupname matcher.
	groupCPU    = `100 * sum by (groupname) (rate(namedprocess_namegroup_cpu_seconds_total{%s}[$w])) / ` + cpus
	groupMemNow = `sum by (groupname) (namedprocess_namegroup_memory_bytes{memtype="resident",%s})`
	groupMemory = `sum by (groupname) (avg_over_time(namedprocess_namegroup_memory_bytes{memtype="resident",%s}[$step]))`
	groupRead   = `sum by (groupname) (rate(namedprocess_namegroup_read_bytes_total{%s}[$w]))`
	groupWrite  = `sum by (groupname) (rate(namedprocess_namegroup_write_bytes_total{%s}[$w]))`
	groupProcs  = `sum by (groupname) (namedprocess_namegroup_num_procs{%s})`
)

var (
	board     = func(n string) string { return `max_over_time(concierge_board_temp_celsius{sensor="` + n + `"}[$step])` }
	modem     = func(s string) string { return `max_over_time(concierge_modem_temp_celsius{sensor="` + s + `"}[$step])` }
	load      = func(m string) string { return `max_over_time(node_load` + m + `[$step])` }
	powerLine = []source{{name: "Long-term limit", expr: limitLong}, {name: "Short-term limit", expr: limitShort}}
)

func overview() tabDef {
	return tabDef{
		tiles: []tile{
			{id: "cpu", label: "CPU", unit: Percent, value: boxCPU},
			{id: "memory", label: "Memory", unit: Bytes, value: memUsed, total: memTotal},
			{id: "disk", label: "Disk", unit: Bytes, value: diskUsed, total: diskSize},
			{id: "cpu_temp", label: "CPU temperature", unit: Celsius, value: cpuTempNow},
			{id: "power", label: "Power", unit: Watts, value: power},
		},
		panels: []panel{
			{id: "cpu_by_service", title: "CPU by service", unit: Percent, stacked: true, series: perService(groupCPU)},
			{id: "memory_by_service", title: "Memory by service", unit: Bytes, stacked: true, series: perService(groupMemory)},
			{id: "temperatures", title: "Temperatures", unit: Celsius, series: []source{
				{name: "CPU package", expr: cpuTemp},
				{name: "Board 1", expr: board("1")}, {name: "Board 2", expr: board("2")}, {name: "Board 3", expr: board("3")},
				{name: "Disk", expr: diskTemp},
				{name: "Wi-Fi", expr: wifiTemp},
				{name: "Modem", expr: modem("pmic")},
			}},
			{id: "power", title: "Power", unit: Watts, series: []source{{name: "Package", expr: power}}, lines: powerLine},
			{id: "clock", title: "Clock", unit: GHz, series: []source{{name: "Average", expr: clockAvg}}},
			{id: "storage_by_service", title: "Storage by service", unit: Bytes, stacked: true, series: []source{
				{expr: storage, nameBy: func(l map[string]string) string { return cmp.Or(serviceLabel[l["service"]], l["service"]) }},
			}},
			{id: "disk_io", title: "Disk I/O", unit: BytesPerSecond, series: []source{
				{name: "Read", expr: diskRead}, {name: "Write", expr: diskWrit},
			}},
		},
	}
}

func hardware() tabDef {
	return tabDef{
		tiles: []tile{
			{id: "cpu_temp", label: "CPU temperature", unit: Celsius, value: cpuTempNow},
			{id: "power", label: "Power", unit: Watts, value: power},
			{id: "clock", label: "Clock", unit: GHz, value: clockNow},
			{id: "board_temp_max", label: "Board temperature", unit: Celsius, value: boardMax},
		},
		panels: []panel{
			{id: "power", title: "Power", unit: Watts, series: []source{{name: "Package", expr: power}}, lines: powerLine},
			{id: "clock", title: "Clock", unit: GHz, series: []source{{name: "Average", expr: clockAvg}, {name: "Max", expr: clockMax}}},
			{id: "temp_cpu", title: "CPU temperature", unit: Celsius, series: []source{
				{name: "Package", expr: cpuTemp},
				{expr: coreTemps, nameBy: func(l map[string]string) string { return capitalise(strings.ReplaceAll(l["label"], "_", " ")) }},
			}},
			{id: "temp_board", title: "Board temperature", unit: Celsius, series: []source{
				{name: "Board 1", expr: board("1")}, {name: "Board 2", expr: board("2")}, {name: "Board 3", expr: board("3")},
			}},
			{id: "temp_disk", title: "Disk temperature", unit: Celsius, series: []source{{name: "Disk", expr: diskTemp}}},
			{id: "temp_wifi", title: "Wi-Fi temperature", unit: Celsius, series: []source{{name: "Wi-Fi", expr: wifiTemp}}},
			{id: "temp_modem", title: "Modem temperature", unit: Celsius, series: []source{
				{name: "PMIC", expr: modem("pmic")}, {name: "XO", expr: modem("xo")}, {name: "PA", expr: modem("pa")},
			}},
			{id: "load", title: "Load", unit: Number, series: []source{
				{name: "1m", expr: load("1")}, {name: "5m", expr: load("5")}, {name: "15m", expr: load("15")},
			}},
		},
	}
}

func service(tab string) tabDef {
	user, label := ServiceUser[tab], serviceLabel[tab]
	match := `groupname="` + user + `"`
	mine := func(q string) []source { return []source{{name: label, expr: fmt.Sprintf(q, match)}} }
	stored := `concierge_storage_bytes{service="` + tab + `"}`
	return tabDef{
		procs: fmt.Sprintf(groupProcs, match),
		tiles: []tile{
			{id: "cpu", label: "CPU", unit: Percent, value: fmt.Sprintf(groupCPU, match)},
			{id: "memory", label: "Memory", unit: Bytes, value: fmt.Sprintf(groupMemNow, match)},
			{id: "storage", label: "Storage", unit: Bytes, value: stored},
		},
		panels: []panel{
			{id: "cpu", title: "CPU", unit: Percent, series: mine(groupCPU)},
			{id: "memory", title: "Memory", unit: Bytes, series: mine(groupMemory)},
			{id: "disk_io", title: "Disk I/O", unit: BytesPerSecond, series: []source{
				{name: "Read", expr: fmt.Sprintf(groupRead, match)}, {name: "Write", expr: fmt.Sprintf(groupWrite, match)},
			}},
			{id: "storage", title: "Storage", unit: Bytes, series: []source{{name: label, expr: stored}}},
		},
	}
}

// catalog is a tab's tiles and panels; false for a tab that doesn't exist.
func catalog(tab string) (tabDef, bool) {
	switch {
	case tab == "overview":
		return overview(), true
	case tab == "hardware":
		return hardware(), true
	case ServiceUser[tab] != "":
		return service(tab), true
	}
	return tabDef{}, false
}

// perService is one series per service from a groupCPU-style query, plus System for every other
// group (the kernel's threads, ModemManager, Prometheus and the exporters themselves).
func perService(q string) []source {
	users := make([]string, 0, len(ServiceUser))
	tabOf := map[string]string{}
	for _, tab := range Tabs {
		if u := ServiceUser[tab]; u != "" {
			users = append(users, u)
			tabOf[u] = tab
		}
	}
	all := strings.Join(users, "|")
	return []source{
		{expr: fmt.Sprintf(q, `groupname=~"`+all+`"`), nameBy: func(l map[string]string) string { return serviceLabel[tabOf[l["groupname"]]] }},
		{name: "System", expr: "sum(" + fmt.Sprintf(q, `groupname!~"`+all+`"`) + ")"},
	}
}

// seriesOrder sorts the series one source names: services in tab order, then by name with a
// trailing number compared as a number ("Core 2" before "Core 10").
func seriesOrder(a, b string) int {
	rank := func(name string) int {
		for i, tab := range Tabs {
			if serviceLabel[tab] == name {
				return i
			}
		}
		return len(Tabs)
	}
	if c := cmp.Compare(rank(a), rank(b)); c != 0 {
		return c
	}
	pa, na := splitNumber(a)
	pb, nb := splitNumber(b)
	return cmp.Or(strings.Compare(pa, pb), cmp.Compare(na, nb))
}

func splitNumber(s string) (string, int) {
	i := len(s)
	for i > 0 && s[i-1] >= '0' && s[i-1] <= '9' {
		i--
	}
	n, _ := strconv.Atoi(s[i:])
	return s[:i], n
}

func capitalise(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// Step is a graph's step for the range: about 500 points, a multiple of the 10 s scrape, never less.
func Step(from, to time.Time) time.Duration {
	secs := int64(to.Sub(from)+500*time.Second-1) / int64(500*time.Second) // ceil(span/500) in seconds
	secs = (secs + 9) / 10 * 10
	return time.Duration(max(secs, 10)) * time.Second
}

// rateWindow is what `$w` stands for: the step, at least 30 s so a rate always has three samples.
func rateWindow(step time.Duration) time.Duration { return max(step, 30*time.Second) }

// gaugeWindow is what `$step` stands for: the step, at least 20 s. A range is left-open, (t-w, t],
// so a 10 s window would hold no sample whenever a scrape is late or missed, and the graph would
// show a gap that isn't there.
func gaugeWindow(step time.Duration) time.Duration { return max(step, 20*time.Second) }

// expand sets an expression's windows: `$w` (rates) and `$step` (a graph's gauges).
func expand(expr string, rateWindow, gaugeWindow time.Duration) string {
	secs := func(d time.Duration) string { return strconv.FormatInt(int64(d/time.Second), 10) + "s" }
	return strings.NewReplacer("$step", secs(gaugeWindow), "$w", secs(rateWindow)).Replace(expr)
}
