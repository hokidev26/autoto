package schedules

import (
	"errors"
	"regexp"
)

// ErrLifecycleCommand is returned when a schedule prompt contains a shell
// command that would stop or restart the Autoto process the scheduler runs in.
//
// The failure it prevents: an Agent schedules "restart Autoto", the schedule
// fires, the server dies mid-run, the desktop autostart entry or an OS
// supervisor brings it back, the interrupted Run resumes, and the resumed turn
// re-issues the same command. That is a restart loop that survives every
// restart, and the only way out is editing the database by hand because the UI
// that would disable the schedule is hosted by the process being killed.
//
// This is defence in depth rather than the only gate. AnalyzeBashCommand
// already classifies the kill and service families as approval-requiring at
// execution time, so an unattended Run cannot silently execute one. Rejecting
// at creation time as well means the Agent gets an immediate, explainable error
// instead of persisting a schedule whose only possible outcome is a blocked
// tool call at 03:00, and it closes the window where a session grant or a
// permissive rule makes the execution-time gate resolve to allow.
var ErrLifecycleCommand = errors.New("schedule prompt contains a command that stops or restarts Autoto itself; run it from a shell outside the server")

// Each branch anchors on a concrete command identifier and requires the Autoto
// target within a bounded distance. A schedule prompt is fed to a model, not to
// a shell, so matching on prose ("check whether Autoto restarted cleanly last
// night") would produce constant false rejections without preventing anything:
// the foot-gun needs an actual command shape to fire.
//
// Starting Autoto is deliberately not matched. Starting a server from inside a
// running one is a no-op or an "address already in use" error, and a schedule
// that starts a sibling instance is legitimate.
var lifecyclePattern = regexp.MustCompile(`(?i)` +
	// Windows: taskkill /IM autoto.exe, sc stop autoto
	`(?:\btaskkill\b[^\n]{0,80}?\bautoto\b)` +
	`|(?:\bsc(?:\.exe)?\s+(?:stop|delete)\b[^\n]{0,80}?\bautoto\b)` +
	// PowerShell: Stop-Process -Name autoto
	`|(?:\bstop-process\b[^\n]{0,80}?\bautoto\b)` +
	// Unix: pkill autoto, killall -9 autoto, kill $(pgrep autoto)
	`|(?:\b(?:pkill|killall)\b[^\n]{0,80}?\bautoto\b)` +
	`|(?:\bkill\b[^\n]{0,40}?\bpgrep\b[^\n]{0,40}?\bautoto\b)` +
	// systemd / launchd units carrying the Autoto identifier
	`|(?:\bsystemctl\b(?:\s+-{1,2}\S+)*\s+(?:restart|stop)\b[^\n]{0,80}?\bautoto\b)` +
	`|(?:\blaunchctl\s+(?:kickstart|unload|bootout|stop)\b[^\n]{0,80}?\bautoto\b)`)

// CheckLifecycleCommand returns ErrLifecycleCommand when prompt contains a
// command that would stop or restart Autoto. Callers should surface the error
// to the creator rather than storing the schedule.
func CheckLifecycleCommand(prompt string) error {
	if prompt != "" && lifecyclePattern.MatchString(prompt) {
		return ErrLifecycleCommand
	}
	return nil
}
