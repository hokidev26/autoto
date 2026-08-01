package schedules

import (
	"errors"
	"testing"
)

func TestCheckLifecycleCommandBlocksSelfRestart(t *testing.T) {
	for _, prompt := range []string{
		`taskkill /F /IM autoto.exe`,
		`taskkill /IM AUTOTO.EXE && start autoto.exe`,
		`sc stop autoto`,
		`sc.exe delete Autoto`,
		`Stop-Process -Name autoto -Force`,
		`pkill autoto`,
		`pkill -9 -f autoto`,
		`killall autoto`,
		`kill $(pgrep autoto)`,
		`kill -9 $(pgrep -f autoto)`,
		`systemctl restart autoto`,
		`systemctl --user stop autoto.service`,
		`launchctl kickstart -k gui/501/com.autoto.desktop`,
		`launchctl bootout gui/501/com.autoto.desktop`,
		`Deploy the build, then run: systemctl restart autoto`,
	} {
		if err := CheckLifecycleCommand(prompt); !errors.Is(err, ErrLifecycleCommand) {
			t.Errorf("prompt %q was allowed, want ErrLifecycleCommand", prompt)
		}
	}
}

func TestCheckLifecycleCommandAllowsProseAndUnrelatedCommands(t *testing.T) {
	for _, prompt := range []string{
		"",
		`檢查 Autoto 昨晚重啟之後的記憶體用量，並回報有沒有殘留的背景程序`,
		`Summarize why the Autoto gateway restarted last night and whether the schedule worker recovered`,
		`Investigate the Kong API gateway autoscaling and restart behaviour`,
		// Starting is benign: a no-op, an "already running" error, or a
		// legitimate sibling instance.
		`systemctl start autoto`,
		`autoto --config /tmp/other.json`,
		// The command family is present but aimed at something else.
		`pkill -f stale-test-runner`,
		`taskkill /F /IM node.exe`,
		`systemctl restart nginx`,
		`killall java`,
		// Autoto is mentioned, but before the command, so it is not the target.
		`Read the autoto logs, then pkill the leftover chromedriver processes`,
	} {
		if err := CheckLifecycleCommand(prompt); err != nil {
			t.Errorf("prompt %q was rejected: %v", prompt, err)
		}
	}
}
