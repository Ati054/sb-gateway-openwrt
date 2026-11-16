//go:build linux

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

type firewallHarness struct{ root, state, script string }

func newFirewallHarness(t *testing.T) firewallHarness {
	t.Helper()
	h := firewallHarness{root: t.TempDir()}
	h.state, h.script = filepath.Join(h.root, "state"), filepath.Join(h.root, "firewall.sh")
	for _, dir := range []string{h.state, filepath.Join(h.root, "bin"), filepath.Join(h.root, "net/br-lan/bridge")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	body, err := os.ReadFile("../../files/resproxy-firewall")
	if err != nil {
		t.Fatal(err)
	}
	script := strings.ReplaceAll(string(body), "/var/run/resproxy", h.state)
	script = strings.ReplaceAll(script, "/var/run/fw4.lock", filepath.Join(h.root, "fw4.lock"))
	script = strings.ReplaceAll(script, "/sys/class/net/", filepath.Join(h.root, "net")+"/")
	script = strings.ReplaceAll(script, "/usr/bin/resproxyd", filepath.Join(h.root, "bin/resproxyd"))
	script = strings.Replace(script, "flock -x 9", "printf waiting > \"$TEST_STATE/waiting\"\nflock -x 9", 1)
	write(h.script, script)
	write(filepath.Join(h.state, "legacy4"), "")
	write(filepath.Join(h.state, "legacy6"), "")
	write(filepath.Join(h.root, "bin/fw4"), "#!/bin/sh\nexit 0\n")
	write(filepath.Join(h.root, "bin/ip"), `#!/bin/sh
printf '%s\n' "$*" >> "$TEST_STATE/ip-actions"
case "$1" in -4) family=4;; -6) family=6;; *) exit 1;; esac
shift
case "$*" in
 route\ show*) echo 'local default dev lo';;
 rule\ show*)
  [ ! -f "$TEST_STATE/legacy$family" ] || echo '10553: from all fwmark 0x5253 lookup 20253'
  [ ! -f "$TEST_STATE/scoped$family" ] || echo '10552: from all fwmark 0x5253 iif br-lan lookup 20253'
  ;;
 rule\ add*)
  case "$*" in 'rule add iif br-lan fwmark 0x5253/0xffffffff table 20253 priority 10552') ;;
   *) exit 2;; esac
  [ ! -f "$TEST_STATE/scoped$family" ] || exit 2
  touch "$TEST_STATE/scoped$family";;
 rule\ del*)
  case "$*" in *'priority 10553') target="$TEST_STATE/legacy$family";;
   *'priority 10552') target="$TEST_STATE/scoped$family";; *) exit 2;; esac
  [ -f "$target" ] || exit 2
  rm "$target";;
esac
exit 0
`)
	write(filepath.Join(h.root, "bin/nft"), `#!/bin/sh
case "$*" in
 'list tables')
  [ "${TEST_NFT_LIST_FAIL:-0}" = 0 ] || exit 1
  [ "${TEST_FW4:-0}" = 0 ] || echo 'table inet fw4'
  exit 0;;
 '-n -s -t -a list chain inet fw4 input')
  [ "${TEST_INPUT_FAIL:-0}" = 0 ] || exit 1
  exit 0;;
esac
case "$1" in
 list|-a) exit 1;;
 -c) exit 0;;
 -f)
  [ "${TEST_NFT_FAIL:-0}" = 0 ] || exit 1
  if [ "${TEST_EXPECT_MARKERS:-0}" = 1 ];then
   test -e "$TEST_STATE/capture-protected.active" || exit 2
   test -e "$TEST_STATE/crash-fallback.active" || exit 3
  fi
  cp "$TEST_STATE/firewall.nft" "$TEST_STATE/kernel"
  echo nft-committed >> "$TEST_STATE/ip-actions"
  ;;
esac
`)
	write(filepath.Join(h.root, "bin/resproxyd"), `#!/bin/sh
case "$1" in
 capture-restore-rules)
  mode=apply
  test ! -e "$TEST_STATE/capture-protected.active" || mode=guard
  echo "# resproxy-restore $mode br-lan 12345 1053"
  echo "$mode"
  ;;
 capture-rules) echo apply;;
 capture-guard-rules) echo guard;;
 capture-emergency-rules) echo emergency;;
 *) exit 1;;
esac
`)
	return h
}

func TestScopedPolicyRuleMigrationCommitsBeforeRetiringLegacy(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "commit", true: "rollback"}[fail], func(t *testing.T) {
			h := newFirewallHarness(t)
			var extra []string
			if fail {
				extra = append(extra, "TEST_NFT_FAIL=1")
			}
			output, err := h.command("ready", extra...).CombinedOutput()
			if (err != nil) != fail {
				t.Fatalf("migration failure=%v: %v %s", fail, err, output)
			}
			for _, family := range []string{"4", "6"} {
				_, oldErr := os.Stat(filepath.Join(h.state, "legacy"+family))
				_, newErr := os.Stat(filepath.Join(h.state, "scoped"+family))
				if (oldErr == nil) != fail || (newErr == nil) == fail {
					t.Fatalf("family %s migration lost an active rule or leaked rollback: old=%v new=%v", family, oldErr, newErr)
				}
			}
			if !fail {
				trace, _ := os.ReadFile(filepath.Join(h.state, "ip-actions"))
				commit := strings.Index(string(trace), "nft-committed")
				retire := strings.Index(string(trace), "-4 rule del fwmark 0x5253/0xffffffff table 20253 priority 10553")
				if commit < 0 || retire < commit {
					t.Fatal("legacy retired before nft commit", string(trace))
				}
				if out, err := h.command("ready").CombinedOutput(); err != nil {
					t.Fatalf("repeat apply duplicated policy rule: %v %s", err, out)
				}
			}
		})
	}
}

func (h firewallHarness) command(action string, extra ...string) *exec.Cmd {
	cmd := exec.Command("sh", h.script, action, "br-lan", "12345", "1053", "auto")
	cmd.Env = append(os.Environ(), "PATH="+filepath.Join(h.root, "bin")+":"+os.Getenv("PATH"), "TEST_STATE="+h.state)
	cmd.Env = append(cmd.Env, extra...)
	return cmd
}

func TestQueuedFirewallRestoreCannotReinstallStartupGuard(t *testing.T) {
	h := newFirewallHarness(t)
	marker := filepath.Join(h.state, "capture-protected.active")
	if err := os.WriteFile(marker, nil, 0600); err != nil {
		t.Fatal(err)
	}
	lock, err := os.OpenFile(h.state+"-firewall.lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	cmd := h.command("restore")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cmd.ProcessState == nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
	}()
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(filepath.Join(h.state, "waiting")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("restore did not reach transaction lock")
		}
		time.Sleep(time.Millisecond)
	}
	// Startup commits while the older interface event is queued on the lock.
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	kernel, err := os.ReadFile(filepath.Join(h.state, "kernel"))
	if err != nil || !strings.HasSuffix(string(kernel), "\napply\n") {
		t.Fatalf("stale guard restored: %q, %v", kernel, err)
	}
}

func TestReadyFirewallCommitsMarkersOnlyAfterKernelSuccess(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "commit", true: "rollback"}[fail], func(t *testing.T) {
			h := newFirewallHarness(t)
			for _, name := range []string{"capture-protected.active", "crash-fallback.active"} {
				if err := os.WriteFile(filepath.Join(h.state, name), nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(h.state, "firewall.nft"), []byte("guard\n"), 0600); err != nil {
				t.Fatal(err)
			}
			extra := []string{"TEST_EXPECT_MARKERS=1"}
			if fail {
				extra = append(extra, "TEST_NFT_FAIL=1")
			}
			output, err := h.command("ready", extra...).CombinedOutput()
			if (err != nil) != fail {
				t.Fatalf("ready failure=%v: %v %s", fail, err, output)
			}
			for _, name := range []string{"capture-protected.active", "crash-fallback.active"} {
				_, statErr := os.Stat(filepath.Join(h.state, name))
				if (statErr == nil) != fail {
					t.Fatalf("incorrect marker commit %s: %v", name, statErr)
				}
			}
			if fail {
				body, _ := os.ReadFile(filepath.Join(h.state, "firewall.nft"))
				if string(body) != "guard\n" {
					t.Fatalf("failed transaction replaced cache: %q", body)
				}
			}
		})
	}
}

func TestRemoveFirewallClearsPhaseMarkersAfterCommit(t *testing.T) {
	h := newFirewallHarness(t)
	for _, name := range []string{"capture-protected.active", "crash-fallback.active"} {
		if err := os.WriteFile(filepath.Join(h.state, name), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	output, err := h.command("remove", "TEST_EXPECT_MARKERS=1").CombinedOutput()
	if err != nil {
		t.Fatalf("remove: %v %s", err, output)
	}
	for _, name := range []string{"capture-protected.active", "crash-fallback.active"} {
		if _, err := os.Stat(filepath.Join(h.state, name)); !os.IsNotExist(err) {
			t.Fatalf("stopped capture kept %s: %v", name, err)
		}
	}
}

func TestFirewallInspectionFailurePreservesPreviousTransaction(t *testing.T) {
	for _, failure := range []string{"TEST_NFT_LIST_FAIL=1", "TEST_INPUT_FAIL=1"} {
		t.Run(failure, func(t *testing.T) {
			h := newFirewallHarness(t)
			if err := os.WriteFile(filepath.Join(h.state, "firewall.nft"), []byte("previous\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if output, err := h.command("apply", "TEST_FW4=1", failure).CombinedOutput(); err == nil {
				t.Fatalf("inspection failure committed a transaction: %s", output)
			}
			body, err := os.ReadFile(filepath.Join(h.state, "firewall.nft"))
			if err != nil || string(body) != "previous\n" {
				t.Fatalf("inspection failure replaced previous state: %s %v", body, err)
			}
			if _, err := os.Stat(filepath.Join(h.state, "kernel")); !os.IsNotExist(err) {
				t.Fatalf("inspection failure changed the kernel: %v", err)
			}
		})
	}
}
