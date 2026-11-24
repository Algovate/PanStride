package transfer

import (
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Keep only aria2's numeric download readout. Its free-form logs can contain signed URLs.
var ariaReadout = regexp.MustCompile(`^\[#[0-9a-fA-F]+ ([0-9.]+)([KMGTPE]?i?B)/([0-9.]+)([KMGTPE]?i?B)\(([0-9]+)%\) CN:([0-9]+) DL:([0-9.]+)([KMGTPE]?i?B)( ETA:([0-9dhms]+))?\]$`)

type transferProgress struct {
	out      io.Writer
	inline   bool
	pending  string
	width    int
	lastLine time.Time
	total    int64
	rate     transferRate
}

func (p *transferProgress) Write(data []byte) (int, error) {
	text := p.pending + string(data)
	for {
		end := strings.IndexAny(text, "\r\n")
		if end < 0 {
			p.pending = text
			return len(data), nil
		}
		p.report(text[:end])
		text = text[end+1:]
	}
}

func (p *transferProgress) report(line string) {
	values := ariaReadout.FindStringSubmatch(line)
	if values == nil {
		return
	}
	now := time.Now()
	amount := ariaBytes(values[1], values[2])
	rawSpeed := ariaBytes(values[7], values[8])
	speed := p.rate.update(now, rawSpeed)
	if !p.inline && now.Sub(p.lastLine) < 5*time.Second {
		return
	}

	connections := values[6] + " connections"
	if values[6] == "1" {
		connections = "1 connection"
	}
	status := fmt.Sprintf("Progress: %s%% | %s %s / %s %s | %s/s | %s",
		values[5], values[1], values[2], values[3], values[4], formatRate(speed), connections)
	total := float64(p.total)
	if total == 0 {
		total = ariaBytes(values[3], values[4])
	}
	remaining := total - amount
	if speed > 0 && remaining > 0 && values[5] != "100" {
		seconds := math.Round(remaining / speed)
		if seconds < float64(math.MaxInt64)/float64(time.Second) {
			status += " | ETA " + (time.Duration(max(1, seconds)) * time.Second).String()
		}
	}
	if p.inline {
		fmt.Fprintf(p.out, "\r%s%s", status, strings.Repeat(" ", max(0, p.width-len(status))))
		p.width = len(status)
	} else {
		fmt.Fprintln(p.out, status)
	}
	p.lastLine = now
}

func (p *transferProgress) finish() {
	p.report(p.pending)
	if p.inline && p.width > 0 {
		fmt.Fprintln(p.out)
	}
}

type transferRate struct {
	value      float64
	lastSample time.Time
	lastActive time.Time
}

func (r *transferRate) update(now time.Time, sample float64) float64 {
	if sample > 0 {
		r.lastActive = now
	}
	switch {
	case sample == 0 && now.Sub(r.lastActive) >= 5*time.Second:
		r.value = 0
	case r.value == 0:
		r.value = sample
	default:
		elapsed := now.Sub(r.lastSample).Seconds()
		weight := -math.Expm1(-elapsed / 4)
		r.value += weight * (sample - r.value)
	}
	r.lastSample = now
	return r.value
}

func ariaBytes(value, unit string) float64 {
	n, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsInf(n, 0) || math.IsNaN(n) {
		return 0
	}
	if unit == "B" {
		return n
	}
	power := strings.IndexByte("KMGTPE", unit[0]) + 1
	return n * math.Pow(1024, float64(power))
}

func formatRate(bytes float64) string {
	units := [...]string{"B", "KiB", "MiB", "GiB", "TiB", "PiB", "EiB"}
	unit := 0
	for bytes >= 1024 && unit < len(units)-1 {
		bytes /= 1024
		unit++
	}
	value := strings.TrimSuffix(strconv.FormatFloat(bytes, 'f', 1, 64), ".0")
	return value + " " + units[unit]
}
