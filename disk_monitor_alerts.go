package main

// Disk Monitor alert engine: consolidated threshold alerts with a per-volume
// cooldown, and a once-a-day summary. Emails go through the shared SMTP
// settings (sendSMTPMessage) with text + HTML alternatives.

import (
	"errors"
	"fmt"
	"time"

	"gopkg.in/gomail.v2"
)

const (
	diskMonitorAlertTick     = 60 * time.Second
	diskMonitorSummaryTick   = 30 * time.Second
	diskMonitorSummaryWindow = 6 * time.Hour
)

// startDiskMonitorAlertEngine evaluates alerts every minute, right after each
// batch (wake), and checks the daily-summary clock every 30 seconds.
func startDiskMonitorAlertEngine() {
	ticker := time.NewTicker(diskMonitorAlertTick)
	defer ticker.Stop()
	summaryTicker := time.NewTicker(diskMonitorSummaryTick)
	defer summaryTicker.Stop()
	for {
		select {
		case <-ticker.C:
			evaluateDiskAlerts(time.Now())
		case <-diskMonitor.wakeAlert:
			evaluateDiskAlerts(time.Now())
		case <-summaryTicker.C:
			maybeSendDiskDailySummary(time.Now())
		case <-appShutdown.Done():
			return
		}
	}
}

// diskMonitorSMTPReady reports whether alert emails can be sent at all.
func diskMonitorSMTPReady(cfg *Config, settings DiskMonitorConfig) bool {
	return cfg != nil && scheduledJobsOverviewSMTPReady(cfg.SMTP) && len(settings.Recipients.To) > 0
}

func diskMonitorSMTPProblem(cfg *Config, settings DiskMonitorConfig) error {
	if cfg == nil || !scheduledJobsOverviewSMTPReady(cfg.SMTP) {
		return errors.New("SMTP is not configured or disabled (see SMTP Settings)")
	}
	if len(settings.Recipients.To) == 0 {
		return errors.New("no alert recipients configured (Disk Monitor settings)")
	}
	return nil
}

func sendDiskMonitorEmail(cfg *Config, settings DiskMonitorConfig, subject, text, html string) error {
	if err := diskMonitorSMTPProblem(cfg, settings); err != nil {
		return err
	}
	message := gomail.NewMessage()
	message.SetHeader("To", settings.Recipients.To...)
	if len(settings.Recipients.CC) > 0 {
		message.SetHeader("Cc", settings.Recipients.CC...)
	}
	message.SetHeader("Subject", subject)
	message.SetBody("text/plain", text)
	if html != "" {
		message.AddAlternative("text/html", html)
	}
	return sendSMTPMessage(cfg.SMTP, message)
}

func recordDiskEmailFailure(now time.Time, err error) {
	diskStateStore.update(func(st *diskMonitorState) {
		st.LastEmailError = err.Error()
		st.LastEmailErrorAt = now
	})
	logSMTPError(fmt.Sprintf("Disk Monitor email failed: %v", err))
}

func recordDiskEmailSuccess(now time.Time) {
	diskStateStore.update(func(st *diskMonitorState) {
		st.LastEmailError = ""
		st.LastEmailErrorAt = time.Time{}
		st.LastEmailSentAt = now
	})
}

// evaluateDiskAlerts sends one consolidated alert when at least one enabled
// volume is TRIPPED/UNKNOWN and past its cooldown.
func evaluateDiskAlerts(now time.Time) {
	if appShutdown.IsShuttingDown() {
		return
	}
	cfg := cache.GetConfig()
	settings, present := currentDiskMonitorSettings()
	if cfg == nil || !present || !settings.isEnabled() {
		return
	}
	state := diskStateStore.snapshot()
	fleet := buildDiskFleetView(cfg, settings, state)

	cooldown := settings.alertEvery()
	eligible := make([]string, 0)
	for _, row := range fleet.problemRows() {
		if row.State.LastAlertSent.IsZero() || now.Sub(row.State.LastAlertSent) >= cooldown {
			eligible = append(eligible, row.Volume.VolumeID)
		}
	}
	if len(eligible) == 0 {
		// Recoveries alone never trigger an email; they ride along with the
		// next alert or the daily summary.
		return
	}

	if err := diskMonitorSMTPProblem(cfg, settings); err != nil {
		// Do not advance cooldowns: the alert fires as soon as SMTP is fixed.
		diskStateStore.update(func(st *diskMonitorState) {
			st.LastEmailError = "Alerts are not being sent: " + err.Error()
			st.LastEmailErrorAt = now
		})
		return
	}

	subject, text, html := buildDiskAlertEmail(fleet, now, settings.EmailBarMode, settings.EmailSubject)
	if subject == "" {
		return
	}
	appShutdown.RunAsync("disk monitor alert email", func() {
		if err := sendDiskMonitorEmail(cfg, settings, subject, text, html); err != nil {
			recordDiskEmailFailure(time.Now(), err)
			fmt.Printf("[DISK] Alert email failed: %v\n", err)
			return
		}
		sent := time.Now()
		recordDiskEmailSuccess(sent)
		included := make(map[string]bool, len(fleet.Recovered))
		for _, row := range fleet.Recovered {
			included[row.Volume.VolumeID] = true
		}
		diskStateStore.update(func(st *diskMonitorState) {
			for _, id := range eligible {
				if vs, ok := st.Volumes[id]; ok {
					vs.LastAlertSent = sent
				}
			}
			for id := range included {
				if vs, ok := st.Volumes[id]; ok {
					vs.PendingRecovery = false
				}
			}
		})
		fmt.Printf("[DISK] Alert email sent: %s\n", subject)
	})
}

// diskDailySummaryTarget returns today's local instance of HH:MM.
func diskDailySummaryTarget(hhmm string, ref time.Time) (time.Time, error) {
	t, err := time.ParseInLocation("15:04", hhmm, ref.Location())
	if err != nil {
		return time.Time{}, err
	}
	return time.Date(ref.Year(), ref.Month(), ref.Day(), t.Hour(), t.Minute(), 0, 0, ref.Location()), nil
}

// maybeSendDiskDailySummary sends the summary once per day at the configured
// time; the marker is persisted so a restart cannot send it twice.
func maybeSendDiskDailySummary(now time.Time) {
	if appShutdown.IsShuttingDown() {
		return
	}
	cfg := cache.GetConfig()
	settings, present := currentDiskMonitorSettings()
	if cfg == nil || !present || !settings.isEnabled() || !settings.summaryEnabled() {
		return
	}
	target, err := diskDailySummaryTarget(settings.DailySummaryTime, now)
	if err != nil {
		return
	}
	marker := target.Format("2006-01-02")
	state := diskStateStore.snapshot()
	if state.LastDailySummaryDate == marker || now.Before(target) {
		return
	}
	markDone := func() {
		diskStateStore.update(func(st *diskMonitorState) { st.LastDailySummaryDate = marker })
	}
	if now.Sub(target) > diskMonitorSummaryWindow {
		markDone() // too late (e.g. started after the window); wait for tomorrow
		return
	}
	if err := diskMonitorSMTPProblem(cfg, settings); err != nil {
		fmt.Printf("[DISK] Daily summary skipped: %v\n", err)
		markDone()
		return
	}
	if len(settings.Volumes) == 0 {
		markDone()
		return
	}

	fleet := buildDiskFleetView(cfg, settings, state)
	subject, text, html := buildDiskDailySummary(fleet, now, settings.EmailBarMode, settings.EmailSubject)
	appShutdown.RunAsync("disk monitor daily summary", func() {
		if err := sendDiskMonitorEmail(cfg, settings, subject, text, html); err != nil {
			recordDiskEmailFailure(time.Now(), err)
			fmt.Printf("[DISK] Daily summary failed (will retry): %v\n", err)
			return
		}
		recordDiskEmailSuccess(time.Now())
		diskStateStore.update(func(st *diskMonitorState) {
			st.LastDailySummaryDate = marker
			for _, row := range fleet.Recovered {
				if vs, ok := st.Volumes[row.Volume.VolumeID]; ok {
					vs.PendingRecovery = false
				}
			}
		})
		fmt.Printf("[DISK] Daily summary sent: %s\n", subject)
	})
}

// sendDiskMonitorTestEmail sends the current fleet as a summary to the
// configured recipients, synchronously, so the UI can report the outcome.
func sendDiskMonitorTestEmail() error {
	cfg := cache.GetConfig()
	settings, _ := currentDiskMonitorSettings()
	if err := diskMonitorSMTPProblem(cfg, settings); err != nil {
		return err
	}
	now := time.Now()
	fleet := buildDiskFleetView(cfg, settings, diskStateStore.snapshot())
	subject, text, html := buildDiskTestEmail(fleet, now, settings.EmailBarMode, settings.EmailSubject)
	if err := sendDiskMonitorEmail(cfg, settings, subject, text, html); err != nil {
		recordDiskEmailFailure(now, err)
		return fmt.Errorf("test email failed: %v", err)
	}
	recordDiskEmailSuccess(now)
	return nil
}
