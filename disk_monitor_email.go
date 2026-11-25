package main

// Disk Monitor email bodies (port of sshDiskMon's alert templates): a plain
// text part plus a mobile-friendly inline-styled HTML alternative.

import (
	"bytes"
	"fmt"
	"html/template"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	diskColorOK      = "#16a34a"
	diskColorTripped = "#dc2626"
	diskColorFailed  = "#d97706"
	diskColorMuted   = "#6b7280"

	diskEmailBrand = "Command Guard · Disk Monitor"
)

// diskEmailSubjectPlaceholders are the runtime placeholders of the
// disk_monitor.email_subject template. They must also be listed in the
// template-variable whitelist of substituteVariables (main.go), otherwise the
// config load rejects them as undefined config variables.
var diskEmailSubjectPlaceholders = map[string]struct{}{
	"summary":  {},
	"type":     {},
	"tripped":  {},
	"failed":   {},
	"ok":       {},
	"volumes":  {},
	"hosts":    {},
	"app_host": {},
}

var diskEmailSubjectPlaceholderPattern = regexp.MustCompile(`\{\{([^{}]+)\}\}`)

// validateDiskEmailSubjectText checks the parts of the subject template that
// do not depend on config variables.
func validateDiskEmailSubjectText(subject string) error {
	if strings.ContainsAny(subject, "\r\n") {
		return fmt.Errorf("email_subject must not contain line breaks")
	}
	if utf8.RuneCountInString(subject) > diskMonitorMaxEmailSubjectLen {
		return fmt.Errorf("email_subject must not exceed %d characters", diskMonitorMaxEmailSubjectLen)
	}
	return nil
}

// validateDiskEmailSubjectPlaceholders rejects {{name}} references that are
// neither a Disk Monitor placeholder nor a defined config variable.
func validateDiskEmailSubjectPlaceholders(subject string, variables map[string]interface{}) error {
	for _, match := range diskEmailSubjectPlaceholderPattern.FindAllStringSubmatch(subject, -1) {
		name := match[1]
		if _, ok := diskEmailSubjectPlaceholders[name]; ok {
			continue
		}
		if _, ok := variables[name]; ok {
			continue
		}
		return fmt.Errorf("unsupported email_subject placeholder %q (use {{summary}}, {{type}}, {{tripped}}, {{failed}}, {{ok}}, {{volumes}}, {{hosts}}, {{app_host}} or a config variable)", match[0])
	}
	return nil
}

// diskSubjectVars are the values substituted into the subject template.
type diskSubjectVars struct {
	Summary string // generated text, e.g. "CRITICAL: 3 volumes affected on 2 host(s)"
	Type    string // Alert | Recovered | Daily summary | Test email
	Hosts   int
}

// renderDiskEmailSubject fills the subject template. A blank template or a
// blank result falls back to the default template.
func renderDiskEmailSubject(tmpl string, fv diskFleetView, vars diskSubjectVars) string {
	appHost, _ := os.Hostname()
	render := func(t string) string {
		out := strings.NewReplacer(
			"{{summary}}", vars.Summary,
			"{{type}}", vars.Type,
			"{{tripped}}", fmt.Sprint(len(fv.Tripped)),
			"{{failed}}", fmt.Sprint(len(fv.Failed)),
			"{{ok}}", fmt.Sprint(len(fv.OK)),
			"{{volumes}}", fmt.Sprint(len(fv.All)),
			"{{hosts}}", fmt.Sprint(vars.Hosts),
			"{{app_host}}", appHost,
		).Replace(t)
		out = strings.NewReplacer("\r", " ", "\n", " ").Replace(out)
		return strings.TrimSpace(out)
	}
	if strings.TrimSpace(tmpl) != "" {
		if subject := render(tmpl); subject != "" {
			return subject
		}
	}
	return render(diskMonitorDefaultEmailSubject)
}

// diskEmailRow joins a volume's config, its host and its runtime state.
type diskEmailRow struct {
	HostRef   string
	HostAddr  string
	MountPath string
	Volume    DiskVolume
	State     diskVolumeState
}

// diskFleetView buckets enabled volumes by status.
type diskFleetView struct {
	All       []diskEmailRow
	Tripped   []diskEmailRow
	Failed    []diskEmailRow
	Recovered []diskEmailRow
	OK        []diskEmailRow
}

func (fv diskFleetView) problemRows() []diskEmailRow {
	return append(append([]diskEmailRow{}, fv.Tripped...), fv.Failed...)
}

// buildDiskFleetView includes only enabled volumes on existing SSH hosts, so
// disabled volumes never alert or appear in the summary.
func buildDiskFleetView(cfg *Config, settings DiskMonitorConfig, state diskMonitorState) diskFleetView {
	fv := diskFleetView{}
	for _, v := range settings.Volumes {
		if !v.isEnabled() {
			continue
		}
		var host Host
		if cfg != nil {
			h, ok := cfg.Hosts[v.HostRef]
			if !ok || !isSSHHost(h) {
				continue
			}
			host = h
		}
		row := diskEmailRow{HostRef: v.HostRef, HostAddr: host.Host, MountPath: v.MountPath, Volume: v}
		if st, ok := state.Volumes[v.VolumeID]; ok && st != nil {
			row.State = *st
		}
		fv.All = append(fv.All, row)
		if row.State.PendingRecovery {
			fv.Recovered = append(fv.Recovered, row)
		}
		switch row.State.Status {
		case diskStatusTripped:
			fv.Tripped = append(fv.Tripped, row)
		case diskStatusUnknown:
			fv.Failed = append(fv.Failed, row)
		case diskStatusOK:
			fv.OK = append(fv.OK, row)
		}
	}
	sortDiskRows(fv.All)
	sortDiskRows(fv.Tripped)
	sortDiskRows(fv.Failed)
	sortDiskRows(fv.Recovered)
	sortDiskRows(fv.OK)
	return fv
}

func sortDiskRows(rows []diskEmailRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := strings.ToLower(rows[i].HostRef), strings.ToLower(rows[j].HostRef)
		if a != b {
			return a < b
		}
		return rows[i].MountPath < rows[j].MountPath
	})
}

func diskHumanTime(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.Format("2006-01-02 15:04")
}

func diskLocalZoneName() string {
	zone, _ := time.Now().Local().Zone()
	if zone == "" {
		return "local"
	}
	return zone
}

func diskStatusLabel(status string) string {
	switch status {
	case diskStatusOK:
		return "OK"
	case diskStatusTripped:
		return "TRIPPED"
	case diskStatusUnknown:
		return "FAILED"
	case "":
		return "PENDING"
	}
	return status
}

func diskStatusHex(status string) string {
	switch status {
	case diskStatusOK:
		return diskColorOK
	case diskStatusTripped:
		return diskColorTripped
	case diskStatusUnknown:
		return diskColorFailed
	}
	return diskColorMuted
}

func diskPercentFreeOrUsed(st diskVolumeState, barMode string) string {
	if st.LastTotalBytes == 0 {
		return ""
	}
	pctFree := diskPercentFree(st.LastFreeBytes, st.LastTotalBytes)
	if barMode == diskBarModeUsed {
		return fmt.Sprintf("(%.0f%% used)", 100-pctFree)
	}
	return fmt.Sprintf("(%.0f%% free)", pctFree)
}

func uniqueDiskHostCount(slices ...[]diskEmailRow) int {
	seen := make(map[string]bool)
	for _, s := range slices {
		for _, r := range s {
			seen[r.HostRef] = true
		}
	}
	return len(seen)
}

// writeDiskRowsByHost renders a per-host grouped plain-text section.
func writeDiskRowsByHost(b *strings.Builder, rows []diskEmailRow, fmtLine func(diskEmailRow) string) {
	groups := make(map[string][]diskEmailRow)
	order := make([]string, 0)
	for _, r := range rows {
		if _, ok := groups[r.HostRef]; !ok {
			order = append(order, r.HostRef)
		}
		groups[r.HostRef] = append(groups[r.HostRef], r)
	}
	for _, hostRef := range order {
		grp := groups[hostRef]
		fmt.Fprintf(b, "Host: %s (%s)\n", hostRef, grp[0].HostAddr)
		for _, r := range grp {
			fmt.Fprintf(b, "  %-20s %s\n", r.MountPath, fmtLine(r))
		}
		b.WriteString("\n")
	}
}

// buildDiskAlertEmail builds the consolidated alert. Empty strings mean there
// is nothing to report. subjectTemplate is disk_monitor.email_subject.
func buildDiskAlertEmail(fv diskFleetView, sentAt time.Time, barMode, subjectTemplate string) (subject, body, htmlBody string) {
	totalProblems := len(fv.Tripped) + len(fv.Failed)
	if totalProblems == 0 && len(fv.Recovered) == 0 {
		return "", "", ""
	}
	if totalProblems > 0 {
		hosts := uniqueDiskHostCount(fv.Tripped, fv.Failed)
		subject = renderDiskEmailSubject(subjectTemplate, fv, diskSubjectVars{
			Summary: fmt.Sprintf("CRITICAL: %d volumes affected on %d host(s)", totalProblems, hosts),
			Type:    "Alert",
			Hosts:   hosts,
		})
	} else {
		subject = renderDiskEmailSubject(subjectTemplate, fv, diskSubjectVars{
			Summary: fmt.Sprintf("%d volume(s) recovered", len(fv.Recovered)),
			Type:    "Recovered",
			Hosts:   uniqueDiskHostCount(fv.Recovered),
		})
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%d volume(s) below threshold across %d host(s).\n", len(fv.Tripped), uniqueDiskHostCount(fv.Tripped))
	fmt.Fprintf(&b, "%d volume(s) failing checks across %d host(s).\n", len(fv.Failed), uniqueDiskHostCount(fv.Failed))
	fmt.Fprintf(&b, "%d volume(s) recovered.\n\n", len(fv.Recovered))

	if len(fv.Tripped) > 0 {
		b.WriteString("== Below threshold ==\n\n")
		writeDiskRowsByHost(&b, fv.Tripped, func(r diskEmailRow) string {
			return fmt.Sprintf("Free %s  /  Threshold %s    %s  since %s",
				humanBytes1024(r.State.LastFreeBytes), formatDiskThreshold(r.Volume.ThresholdValue, r.Volume.ThresholdUnit),
				diskPercentFreeOrUsed(r.State, barMode), diskHumanTime(r.State.TrippedSince))
		})
		b.WriteString("\n")
	}
	if len(fv.Failed) > 0 {
		b.WriteString("== Check failed ==\n\n")
		writeDiskRowsByHost(&b, fv.Failed, func(r diskEmailRow) string {
			line := r.State.LastError
			if line == "" {
				line = "(no error captured)"
			}
			return fmt.Sprintf("%s\n  since %s", line, diskHumanTime(r.State.TrippedSince))
		})
		b.WriteString("\n")
	}
	if len(fv.Recovered) > 0 {
		b.WriteString("== Recovered ==\n\n")
		writeDiskRowsByHost(&b, fv.Recovered, func(r diskEmailRow) string {
			return fmt.Sprintf("Free %s  /  Threshold %s\n  recovered at %s",
				humanBytes1024(r.State.LastFreeBytes), formatDiskThreshold(r.Volume.ThresholdValue, r.Volume.ThresholdUnit), diskHumanTime(r.State.LastCheckedAt))
		})
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "--\n%s — sent at %s (%s)\n", diskEmailBrand, sentAt.Format("2006-01-02 15:04"), diskLocalZoneName())

	html, err := renderDiskAlertHTML(fv, sentAt, barMode, "Alert")
	if err != nil {
		html = ""
	}
	return subject, b.String(), html
}

// buildDiskDailySummary lists every enabled volume grouped by host.
func buildDiskDailySummary(fv diskFleetView, sentAt time.Time, barMode, subjectTemplate string) (subject, body, htmlBody string) {
	return buildDiskSummaryEmail(fv, sentAt, barMode, "Daily summary",
		renderDiskEmailSubject(subjectTemplate, fv, diskSubjectVars{
			Summary: fmt.Sprintf("Daily summary: %d volumes OK, %d alerting", len(fv.OK), len(fv.Tripped)+len(fv.Failed)),
			Type:    "Daily summary",
			Hosts:   uniqueDiskHostCount(fv.All),
		}))
}

// buildDiskTestEmail is the summary layout with a test subject and intro line.
func buildDiskTestEmail(fv diskFleetView, sentAt time.Time, barMode, subjectTemplate string) (subject, body, htmlBody string) {
	subject, body, htmlBody = buildDiskSummaryEmail(fv, sentAt, barMode, "Test email",
		renderDiskEmailSubject(subjectTemplate, fv, diskSubjectVars{Summary: "Test email", Type: "Test email", Hosts: uniqueDiskHostCount(fv.All)}))
	body = "This is a test email from " + diskEmailBrand + ". Current fleet status follows.\n\n" + body
	return subject, body, htmlBody
}

func buildDiskSummaryEmail(fv diskFleetView, sentAt time.Time, barMode, title, subject string) (string, string, string) {
	var b strings.Builder
	writeDiskRowsByHost(&b, fv.All, func(r diskEmailRow) string {
		return fmt.Sprintf("Free %s  /  Threshold %s    %s    last check %s",
			humanBytes1024(r.State.LastFreeBytes), formatDiskThreshold(r.Volume.ThresholdValue, r.Volume.ThresholdUnit),
			diskStatusLabel(r.State.Status), diskHumanTime(r.State.LastCheckedAt))
	})
	if len(fv.All) == 0 {
		b.WriteString("No volumes are being monitored yet.\n\n")
	}
	fmt.Fprintf(&b, "--\n%s — %s at %s (%s)\n", diskEmailBrand, strings.ToLower(title), sentAt.Format("2006-01-02 15:04"), diskLocalZoneName())
	html, err := renderDiskSummaryHTML(fv, sentAt, barMode, title)
	if err != nil {
		html = ""
	}
	return subject, b.String(), html
}

// ---------------------------------------------------------------------------
// HTML rendering
// ---------------------------------------------------------------------------

type diskHTMLVolume struct {
	MountPath   string
	StatusLabel string
	StatusColor string
	FreeBytes   string
	TotalBytes  string
	Threshold   string
	BarPercent  int
	BarLabel    string
	HasPercent  bool
	LastCheck   string
	Detail      string
	DetailLabel string
}

type diskHTMLGroup struct {
	HostRef  string
	HostAddr string
	Volumes  []diskHTMLVolume
}

type diskHTMLSection struct {
	Title    string
	TitleHex string
	Groups   []diskHTMLGroup
}

type diskHTMLSummaryData struct {
	Brand         string
	HeadlineTitle string
	OKCount       int
	AlertingCount int
	OKColor       string
	AlertColor    string
	Groups        []diskHTMLGroup
	FooterLine    string
	Empty         bool
}

type diskHTMLAlertData struct {
	Brand          string
	HeadlineTitle  string
	TrippedCount   int
	FailedCount    int
	RecoveredCount int
	Sections       []diskHTMLSection
	FooterLine     string
}

func buildDiskHTMLVolume(r diskEmailRow, barMode string) diskHTMLVolume {
	st := r.State
	row := diskHTMLVolume{
		MountPath:   r.MountPath,
		StatusLabel: diskStatusLabel(st.Status),
		StatusColor: diskStatusHex(st.Status),
		FreeBytes:   humanBytes1024(st.LastFreeBytes),
		Threshold:   formatDiskThreshold(r.Volume.ThresholdValue, r.Volume.ThresholdUnit),
		LastCheck:   diskHumanTime(st.LastCheckedAt),
	}
	if st.LastTotalBytes > 0 {
		row.TotalBytes = humanBytes1024(st.LastTotalBytes)
		freeInt := int(diskPercentFree(st.LastFreeBytes, st.LastTotalBytes) + 0.5)
		if barMode == diskBarModeUsed {
			row.BarPercent = 100 - freeInt
			row.BarLabel = fmt.Sprintf("%d%% used", 100-freeInt)
		} else {
			row.BarPercent = freeInt
			row.BarLabel = fmt.Sprintf("%d%% free", freeInt)
		}
		row.HasPercent = true
	}
	return row
}

func groupDiskRowsForHTML(rows []diskEmailRow, barMode string, decorate func(diskEmailRow, *diskHTMLVolume)) []diskHTMLGroup {
	byHost := make(map[string]*diskHTMLGroup)
	order := make([]string, 0)
	for _, r := range rows {
		g, ok := byHost[r.HostRef]
		if !ok {
			g = &diskHTMLGroup{HostRef: r.HostRef, HostAddr: r.HostAddr}
			byHost[r.HostRef] = g
			order = append(order, r.HostRef)
		}
		v := buildDiskHTMLVolume(r, barMode)
		if decorate != nil {
			decorate(r, &v)
		}
		g.Volumes = append(g.Volumes, v)
	}
	out := make([]diskHTMLGroup, 0, len(order))
	for _, id := range order {
		out = append(out, *byHost[id])
	}
	return out
}

func diskFooterLine(sentAt time.Time, what string) string {
	return fmt.Sprintf("%s · %s at %s (%s)", diskEmailBrand, what, sentAt.Format("2006-01-02 15:04"), diskLocalZoneName())
}

func renderDiskSummaryHTML(fv diskFleetView, sentAt time.Time, barMode, title string) (string, error) {
	data := diskHTMLSummaryData{
		Brand:         diskEmailBrand,
		HeadlineTitle: title,
		OKCount:       len(fv.OK),
		AlertingCount: len(fv.Tripped) + len(fv.Failed),
		OKColor:       diskColorOK,
		AlertColor:    diskColorTripped,
		Groups:        groupDiskRowsForHTML(fv.All, barMode, nil),
		FooterLine:    diskFooterLine(sentAt, strings.ToLower(title)),
		Empty:         len(fv.All) == 0,
	}
	var buf bytes.Buffer
	if err := diskSummaryHTMLTpl.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func renderDiskAlertHTML(fv diskFleetView, sentAt time.Time, barMode, title string) (string, error) {
	data := diskHTMLAlertData{
		Brand:          diskEmailBrand,
		HeadlineTitle:  title,
		TrippedCount:   len(fv.Tripped),
		FailedCount:    len(fv.Failed),
		RecoveredCount: len(fv.Recovered),
		FooterLine:     diskFooterLine(sentAt, "sent"),
	}
	if len(fv.Tripped) > 0 {
		data.Sections = append(data.Sections, diskHTMLSection{Title: "Below threshold", TitleHex: diskColorTripped,
			Groups: groupDiskRowsForHTML(fv.Tripped, barMode, func(r diskEmailRow, v *diskHTMLVolume) {
				v.DetailLabel = "since " + diskHumanTime(r.State.TrippedSince)
			})})
	}
	if len(fv.Failed) > 0 {
		data.Sections = append(data.Sections, diskHTMLSection{Title: "Check failed", TitleHex: diskColorFailed,
			Groups: groupDiskRowsForHTML(fv.Failed, barMode, func(r diskEmailRow, v *diskHTMLVolume) {
				line := r.State.LastError
				if line == "" {
					line = "(no error captured)"
				}
				v.Detail = line
				v.DetailLabel = "since " + diskHumanTime(r.State.TrippedSince)
			})})
	}
	if len(fv.Recovered) > 0 {
		data.Sections = append(data.Sections, diskHTMLSection{Title: "Recovered", TitleHex: diskColorOK,
			Groups: groupDiskRowsForHTML(fv.Recovered, barMode, func(r diskEmailRow, v *diskHTMLVolume) {
				v.DetailLabel = "recovered at " + diskHumanTime(r.State.LastCheckedAt)
			})})
	}
	var buf bytes.Buffer
	if err := diskAlertHTMLTpl.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// Inline-styled HTML: no <style> block, no images, outer table for Outlook.
const diskVolumeCardHTML = `{{define "diskVolumeCard"}}
<tr><td style="padding:8px 20px 0 20px;">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="border:1px solid #e5e7eb;border-radius:6px;background:#ffffff;">
<tr><td style="padding:12px 14px;">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0">
<tr>
<td style="font-size:15px;font-weight:600;color:#111827;font-family:ui-monospace,Menlo,Consolas,monospace;">{{.MountPath}}</td>
<td align="right"><span style="display:inline-block;padding:3px 9px;border-radius:999px;background:{{.StatusColor}};color:#ffffff;font-size:12px;font-weight:700;letter-spacing:.03em;">{{.StatusLabel}}</span></td>
</tr>
</table>
{{if .Detail}}
<div style="font-size:13px;color:#111827;margin-top:8px;font-family:ui-monospace,Menlo,Consolas,monospace;word-break:break-word;">{{.Detail}}</div>
{{else}}
<div style="font-size:14px;color:#111827;margin-top:8px;">Free <strong>{{.FreeBytes}}</strong>{{if .TotalBytes}} of {{.TotalBytes}}{{end}}</div>
{{if .HasPercent}}
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="margin-top:8px;">
<tr><td style="background:#e5e7eb;border-radius:999px;height:8px;line-height:8px;font-size:0;">
<table role="presentation" cellpadding="0" cellspacing="0" border="0" style="width:{{.BarPercent}}%;height:8px;"><tr><td style="background:{{.StatusColor}};border-radius:999px;height:8px;line-height:8px;font-size:0;">&nbsp;</td></tr></table>
</td></tr>
</table>
<div style="font-size:12px;color:#6b7280;margin-top:4px;">{{.BarLabel}}</div>
{{end}}
<div style="font-size:13px;color:#6b7280;margin-top:8px;">Threshold {{.Threshold}}</div>
{{if .LastCheck}}<div style="font-size:12px;color:#9ca3af;margin-top:2px;">Last check {{.LastCheck}}</div>{{end}}
{{end}}
{{if .DetailLabel}}<div style="font-size:12px;color:#9ca3af;margin-top:6px;">{{.DetailLabel}}</div>{{end}}
</td></tr>
</table>
</td></tr>
{{end}}`

const diskHTMLHead = `<!DOCTYPE html>
<html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>{{.HeadlineTitle}}</title></head>
<body style="margin:0;padding:0;background:#f3f4f6;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;color:#111827;">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="background:#f3f4f6;">
<tr><td align="center" style="padding:16px 8px;">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" border="0" style="max-width:600px;background:#ffffff;border:1px solid #e5e7eb;border-radius:8px;">
<tr><td style="padding:20px 20px 12px 20px;border-bottom:1px solid #e5e7eb;">
<div style="font-size:13px;color:#6b7280;letter-spacing:.04em;text-transform:uppercase;">{{.Brand}}</div>
<div style="font-size:20px;font-weight:600;margin-top:4px;color:#111827;">{{.HeadlineTitle}}</div>
<div style="margin-top:10px;">`

const diskHTMLFoot = `<tr><td style="padding:20px;border-top:1px solid #e5e7eb;">
<div style="font-size:12px;color:#9ca3af;text-align:center;">{{.FooterLine}}</div>
</td></tr>
</table>
</td></tr>
</table>
</body></html>`

const diskSummaryHTMLSource = diskVolumeCardHTML + diskHTMLHead + `
<span style="display:inline-block;padding:4px 10px;border-radius:999px;background:{{.OKColor}};color:#ffffff;font-size:13px;font-weight:600;">{{.OKCount}} OK</span>
<span style="display:inline-block;padding:4px 10px;border-radius:999px;background:{{if gt .AlertingCount 0}}{{.AlertColor}}{{else}}#9ca3af{{end}};color:#ffffff;font-size:13px;font-weight:600;margin-left:6px;">{{.AlertingCount}} alerting</span>
</div>
</td></tr>
{{if .Empty}}<tr><td style="padding:16px 20px;font-size:14px;color:#6b7280;">No volumes are being monitored yet.</td></tr>{{end}}
{{range .Groups}}
<tr><td style="padding:16px 20px 4px 20px;">
<div style="font-size:15px;font-weight:600;color:#111827;">{{.HostRef}}</div>
<div style="font-size:13px;color:#6b7280;margin-top:2px;">{{.HostAddr}}</div>
</td></tr>
{{range .Volumes}}{{template "diskVolumeCard" .}}{{end}}
{{end}}
` + diskHTMLFoot

const diskAlertHTMLSource = diskVolumeCardHTML + diskHTMLHead + `
{{if gt .TrippedCount 0}}<span style="display:inline-block;padding:4px 10px;border-radius:999px;background:#dc2626;color:#ffffff;font-size:13px;font-weight:600;margin-right:6px;">{{.TrippedCount}} below threshold</span>{{end}}
{{if gt .FailedCount 0}}<span style="display:inline-block;padding:4px 10px;border-radius:999px;background:#d97706;color:#ffffff;font-size:13px;font-weight:600;margin-right:6px;">{{.FailedCount}} failed</span>{{end}}
{{if gt .RecoveredCount 0}}<span style="display:inline-block;padding:4px 10px;border-radius:999px;background:#16a34a;color:#ffffff;font-size:13px;font-weight:600;">{{.RecoveredCount}} recovered</span>{{end}}
</div>
</td></tr>
{{range .Sections}}
<tr><td style="padding:18px 20px 4px 20px;">
<div style="font-size:14px;font-weight:700;color:{{.TitleHex}};text-transform:uppercase;letter-spacing:.05em;">{{.Title}}</div>
</td></tr>
{{range .Groups}}
<tr><td style="padding:8px 20px 0 20px;">
<div style="font-size:15px;font-weight:600;color:#111827;">{{.HostRef}}</div>
<div style="font-size:13px;color:#6b7280;margin-top:2px;">{{.HostAddr}}</div>
</td></tr>
{{range .Volumes}}{{template "diskVolumeCard" .}}{{end}}
{{end}}
{{end}}
` + diskHTMLFoot

var (
	diskSummaryHTMLTpl = template.Must(template.New("disk-summary").Parse(diskSummaryHTMLSource))
	diskAlertHTMLTpl   = template.Must(template.New("disk-alert").Parse(diskAlertHTMLSource))
)
