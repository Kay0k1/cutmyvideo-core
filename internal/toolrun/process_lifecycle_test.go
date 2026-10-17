package toolrun

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type descendantReceipt struct {
	Address string
	Token   string
}

func descendantAlive(receipt descendantReceipt) bool {
	connection, err := net.DialTimeout("tcp", receipt.Address, 100*time.Millisecond)
	if err != nil {
		return false
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(time.Second))
	_, _ = io.WriteString(connection, "alive "+receipt.Token+"\n")
	response, _ := bufio.NewReader(connection).ReadString('\n')
	return response == "alive "+receipt.Token+"\n"
}

func stopDescendantFixture(receipt descendantReceipt) {
	connection, err := net.DialTimeout("tcp", receipt.Address, time.Second)
	if err != nil {
		return
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(time.Second))
	_, _ = io.WriteString(connection, "stop "+receipt.Token+"\n")
	_, _ = io.Copy(io.Discard, connection)
}

func TestRunOutputCleansDescendantsAfterParentExit(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"redirected-stdio", "inherited-pipes"} {
		t.Run(mode, func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "descendant.json")
			token := filepath.Base(filepath.Dir(marker)) + "-" + mode
			var receipt descendantReceipt
			t.Cleanup(func() {
				if receipt.Address == "" {
					data, _ := os.ReadFile(marker)
					_ = json.Unmarshal(data, &receipt)
				}
				// A baseline failure must never leave this test's real child alive.
				// The private marker and token target only the owned fixture.
				stopDescendantFixture(receipt)
				deadline := time.Now().Add(2 * time.Second)
				for descendantAlive(receipt) && time.Now().Before(deadline) {
					time.Sleep(10 * time.Millisecond)
				}
				if descendantAlive(receipt) {
					t.Error("owned descendant fixture did not stop during cleanup")
				}
			})
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			result := RunOutput(ctx, executable, []string{"-test.run=^TestRunOutputDescendantFixture$", "--", "cutmy-descendant-parent", marker, token, mode}, io.Discard)
			if mode == "redirected-stdio" && result.ProcessErr != nil {
				t.Fatalf("successful parent lost its raw outcome: %v", result.ProcessErr)
			}
			if mode == "inherited-pipes" && !errors.Is(result.ProcessErr, exec.ErrWaitDelay) {
				t.Fatalf("inherited writers lost the raw WaitDelay outcome: %v", result.ProcessErr)
			}
			data, err := os.ReadFile(marker)
			if err != nil || json.Unmarshal(data, &receipt) != nil || receipt.Address == "" || receipt.Token != token {
				t.Fatalf("parent did not wait for its owned descendant: %v", err)
			}
			t.Logf("parent outcome: %v; elapsed: %v", result.ProcessErr, result.Elapsed)
			deadline := time.Now().Add(2 * time.Second)
			for descendantAlive(receipt) {
				if time.Now().After(deadline) {
					t.Fatal("owned descendant survived the parent process return")
				}
				time.Sleep(10 * time.Millisecond)
			}
		})
	}
}

func TestRunOutputDescendantFixture(t *testing.T) {
	arguments := os.Args
	for len(arguments) > 0 && !strings.HasPrefix(arguments[0], "cutmy-descendant-") {
		arguments = arguments[1:]
	}
	if len(arguments) < 3 {
		return
	}
	mode, marker, token := arguments[0], arguments[1], arguments[2]
	if mode == "cutmy-descendant-parent" {
		executable, err := os.Executable()
		if err != nil || len(arguments) != 4 {
			os.Exit(3)
		}
		cmd := exec.Command(executable, "-test.run=^TestRunOutputDescendantFixture$", "--", "cutmy-descendant-child", marker, token)
		if arguments[3] == "inherited-pipes" {
			cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		}
		if cmd.Start() != nil {
			os.Exit(3)
		}
		_ = cmd.Process.Release()
		deadline := time.Now().Add(8 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(marker); err == nil {
				os.Exit(0)
			}
			time.Sleep(10 * time.Millisecond)
		}
		os.Exit(3)
	}
	if mode != "cutmy-descendant-child" {
		return
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		os.Exit(3)
	}
	receipt, _ := json.Marshal(descendantReceipt{Address: listener.Addr().String(), Token: token})
	if os.WriteFile(marker+".tmp", receipt, 0600) != nil || os.Rename(marker+".tmp", marker) != nil {
		os.Exit(3)
	}
	for {
		connection, err := listener.Accept()
		if err != nil {
			os.Exit(3)
		}
		_ = connection.SetDeadline(time.Now().Add(time.Second))
		command, _ := bufio.NewReader(connection).ReadString('\n')
		if command == "stop "+token+"\n" {
			_ = connection.Close()
			_ = listener.Close()
			os.Exit(0)
		}
		if command == "alive "+token+"\n" {
			_, _ = io.WriteString(connection, "alive "+token+"\n")
		}
		_ = connection.Close()
	}
}
